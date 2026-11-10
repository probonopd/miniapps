/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */

/*
 * Audio source and player over abstract Unix sockets served by the app.
 *
 * The helper is a static musl binary and cannot reach Android's audio
 * (AudioRecord/AudioTrack live in the app's Java process), so the app
 * records and plays and this module only moves PCM:
 *
 *   audio_source  sockaudio,<name>    reads S16LE frames the app sends
 *   audio_player  sockaudio,<name>    sends one frame per 1-byte request
 *
 * After connecting, the module sends one line "srate ch ptime\n" so that
 * the app opens its device with the parameters baresip negotiated.
 * The player is pulled, not pushed: a pushing writer would fill the socket
 * buffer and that buffer would become the call's audio latency.
 */

#include <string.h>
#include <unistd.h>
#include <errno.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <re_atomic.h>
#include <re.h>
#include <rem.h>
#include <baresip.h>


struct ausrc_st {
	struct ausrc_prm prm;
	thrd_t thread;
	RE_ATOMIC bool run;
	int fd;
	size_t sampc;
	ausrc_read_h *rh;
	void *arg;
};

struct auplay_st {
	struct auplay_prm prm;
	thrd_t thread;
	RE_ATOMIC bool run;
	int fd;
	size_t sampc;
	auplay_write_h *wh;
	void *arg;
};


static struct ausrc *ausrc;
static struct auplay *auplay;


/* Abstract namespace: no file to clean up, and the app checks that the
 * peer has its own uid, so other apps cannot feed or tap a call. */
static int sock_connect(const char *name, uint32_t srate, uint8_t ch,
			uint32_t ptime)
{
	struct sockaddr_un sun;
	size_t len = strlen(name);
	char hdr[64];
	int fd, n;

	if (len == 0 || len >= sizeof(sun.sun_path) - 1)
		return -EINVAL;

	memset(&sun, 0, sizeof(sun));
	sun.sun_family = AF_UNIX;
	memcpy(sun.sun_path + 1, name, len);

	fd = socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0);
	if (fd < 0)
		return -errno;

	if (connect(fd, (struct sockaddr *)&sun,
		    (socklen_t)(offsetof(struct sockaddr_un, sun_path) + 1
				+ len)) < 0) {
		int err = errno;
		close(fd);
		return -err;
	}

	n = re_snprintf(hdr, sizeof(hdr), "%u %u %u\n", srate, ch, ptime);
	if (n <= 0 || write(fd, hdr, (size_t)n) != n) {
		close(fd);
		return -EIO;
	}

	return fd;
}


static bool read_full(int fd, uint8_t *p, size_t n)
{
	while (n) {
		ssize_t r = read(fd, p, n);
		if (r < 0 && errno == EINTR)
			continue;
		if (r <= 0)
			return false;
		p += r;
		n -= (size_t)r;
	}
	return true;
}


static bool write_full(int fd, const uint8_t *p, size_t n)
{
	while (n) {
		ssize_t w = write(fd, p, n);
		if (w < 0 && errno == EINTR)
			continue;
		if (w <= 0)
			return false;
		p += w;
		n -= (size_t)w;
	}
	return true;
}


static int src_thread(void *arg)
{
	struct ausrc_st *st = arg;
	size_t bytes = st->sampc * sizeof(int16_t);
	int16_t *sampv = mem_alloc(bytes, NULL);

	if (!sampv)
		return ENOMEM;

	/* The app's AudioRecord sets the pace; a closed socket ends the
	 * source (the call keeps running, silent). */
	while (re_atomic_rlx(&st->run)) {
		struct auframe af;

		if (!read_full(st->fd, (uint8_t *)sampv, bytes)) {
			warning("sockaudio: source closed by the app\n");
			break;
		}

		auframe_init(&af, AUFMT_S16LE, sampv, st->sampc,
			     st->prm.srate, st->prm.ch);
		af.timestamp = tmr_jiffies_usec();
		st->rh(&af, st->arg);
	}

	mem_deref(sampv);
	return 0;
}


static int play_thread(void *arg)
{
	struct auplay_st *st = arg;
	size_t bytes = st->sampc * sizeof(int16_t);
	int16_t *sampv = mem_alloc(bytes, NULL);
	uint8_t req;

	if (!sampv)
		return ENOMEM;

	while (re_atomic_rlx(&st->run)) {
		struct auframe af;

		if (!read_full(st->fd, &req, 1))
			break;

		auframe_init(&af, AUFMT_S16LE, sampv, st->sampc,
			     st->prm.srate, st->prm.ch);
		af.timestamp = tmr_jiffies_usec();
		st->wh(&af, st->arg);

		if (!write_full(st->fd, (uint8_t *)sampv, bytes))
			break;
	}

	mem_deref(sampv);
	return 0;
}


/* shutdown() wakes the thread out of its blocking read, then it is
 * joined before the state goes away. */
static void src_destructor(void *arg)
{
	struct ausrc_st *st = arg;

	if (re_atomic_rlx(&st->run)) {
		re_atomic_rlx_set(&st->run, false);
		shutdown(st->fd, SHUT_RDWR);
		thrd_join(st->thread, NULL);
	}
	if (st->fd >= 0)
		close(st->fd);
}


static void play_destructor(void *arg)
{
	struct auplay_st *st = arg;

	if (re_atomic_rlx(&st->run)) {
		re_atomic_rlx_set(&st->run, false);
		shutdown(st->fd, SHUT_RDWR);
		thrd_join(st->thread, NULL);
	}
	if (st->fd >= 0)
		close(st->fd);
}


static int src_alloc(struct ausrc_st **stp, const struct ausrc *as,
		     struct ausrc_prm *prm, const char *dev,
		     ausrc_read_h *rh, ausrc_error_h *errh, void *arg)
{
	struct ausrc_st *st;
	int err;
	(void)as;
	(void)errh;

	if (!stp || !prm || !rh || !str_isset(dev))
		return EINVAL;

	if (prm->fmt != AUFMT_S16LE) {
		warning("sockaudio: only S16LE (%s asked)\n",
			aufmt_name(prm->fmt));
		return ENOTSUP;
	}

	st = mem_zalloc(sizeof(*st), src_destructor);
	if (!st)
		return ENOMEM;

	st->fd = -1;
	st->prm = *prm;
	st->rh = rh;
	st->arg = arg;
	st->sampc = prm->srate * prm->ch * prm->ptime / 1000;

	st->fd = sock_connect(dev, prm->srate, prm->ch, prm->ptime);
	if (st->fd < 0) {
		err = -st->fd;
		st->fd = -1;
		warning("sockaudio: source %s: %m\n", dev, err);
		goto out;
	}

	re_atomic_rlx_set(&st->run, true);
	err = thread_create_name(&st->thread, "sockaudio_src", src_thread,
				 st);
	if (err)
		re_atomic_rlx_set(&st->run, false);

 out:
	if (err)
		mem_deref(st);
	else
		*stp = st;

	return err;
}


static int play_alloc(struct auplay_st **stp, const struct auplay *ap,
		      struct auplay_prm *prm, const char *dev,
		      auplay_write_h *wh, void *arg)
{
	struct auplay_st *st;
	int err;
	(void)ap;

	if (!stp || !prm || !wh || !str_isset(dev))
		return EINVAL;

	if (prm->fmt != AUFMT_S16LE) {
		warning("sockaudio: only S16LE (%s asked)\n",
			aufmt_name(prm->fmt));
		return ENOTSUP;
	}

	st = mem_zalloc(sizeof(*st), play_destructor);
	if (!st)
		return ENOMEM;

	st->fd = -1;
	st->prm = *prm;
	st->wh = wh;
	st->arg = arg;
	st->sampc = prm->srate * prm->ch * prm->ptime / 1000;

	st->fd = sock_connect(dev, prm->srate, prm->ch, prm->ptime);
	if (st->fd < 0) {
		err = -st->fd;
		st->fd = -1;
		warning("sockaudio: player %s: %m\n", dev, err);
		goto out;
	}

	re_atomic_rlx_set(&st->run, true);
	err = thread_create_name(&st->thread, "sockaudio_play", play_thread,
				 st);
	if (err)
		re_atomic_rlx_set(&st->run, false);

 out:
	if (err)
		mem_deref(st);
	else
		*stp = st;

	return err;
}


static int module_init(void)
{
	int err;

	err  = ausrc_register(&ausrc, baresip_ausrcl(), "sockaudio",
			      src_alloc);
	err |= auplay_register(&auplay, baresip_auplayl(), "sockaudio",
			       play_alloc);

	return err;
}


static int module_close(void)
{
	ausrc = mem_deref(ausrc);
	auplay = mem_deref(auplay);

	return 0;
}


EXPORT_SYM const struct mod_export DECL_EXPORTS(sockaudio) = {
	"sockaudio",
	"sound",
	module_init,
	module_close
};
