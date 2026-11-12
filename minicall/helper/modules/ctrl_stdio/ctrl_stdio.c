/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

/*
 * Control over the helper's own stdin and stdout, one JSON object per line,
 * in the message format of baresip's ctrl_tcp:
 *
 *   in:  {"command":"dial","params":"sip:a@b","token":"7"}
 *   out: {"response":true,"ok":true,"data":"...","token":"7"}
 *   out: {"event":true,"class":"call","type":"CALL_INCOMING",...}
 *
 * The pipes belong to the app that started the helper, so no other app can
 * place or take a call, which a TCP control port on localhost would allow.
 * Log lines go to stderr from here on, so stdout carries only JSON.
 *
 * WebRTC calls, set up by the app over its own signaling. Answering (a
 * browser or another device calls):
 *
 *   in:  {"command":"webrtc_answer","params":{"type":"offer","sdp":"..."}}
 *   out: {"event":true,"class":"webrtc","type":"ANSWER","sdp":"..."}
 *
 * Calling another device:
 *
 *   in:  {"command":"webrtc_offer"}
 *   out: {"event":true,"class":"webrtc","type":"OFFER","sdp":"..."}
 *   in:  {"command":"webrtc_remote","params":{"type":"answer","sdp":"..."}}
 *
 * then for both:
 *
 *   out: {"event":true,"class":"webrtc","type":"ESTABLISHED"}
 *   out: {"event":true,"class":"webrtc","type":"CLOSED","param":"..."}
 *   in:  {"command":"webrtc_close"}
 *
 * Offers and answers carry all candidates (no trickle); STUN server from the
 * config key webrtc_stun.
 */

#define _GNU_SOURCE
#include <stdio.h>
#include <signal.h>
#include <string.h>
#include <errno.h>
#include <ucontext.h>
#include <unistd.h>
#include <re.h>
#include <baresip.h>


enum { MAX_LINE = 65536 };

static struct mbuf *linebuf;
static struct re_fhs *fhs;
static struct peer_connection *pc;
static struct rtc_configuration pc_config;
static bool pc_media;


static int print_handler(const char *p, size_t size, void *arg)
{
	return mbuf_write_mem(arg, (const uint8_t *)p, size);
}


static int stdout_print(const char *p, size_t size, void *arg)
{
	(void)arg;
	return fwrite(p, 1, size, stdout) == size ? 0 : EIO;
}


/* One write and a flush per object: the app reads whole lines. */
static void emit(struct odict *od)
{
	struct re_printf pf = {stdout_print, NULL};

	if (json_encode_odict(&pf, od) == 0) {
		fputc('\n', stdout);
		fflush(stdout);
	}
}


static void webrtc_event(const char *type, const char *key, const char *val)
{
	struct odict *od = NULL;

	if (odict_alloc(&od, 8))
		return;

	if (!odict_entry_add(od, "event", ODICT_BOOL, true) &&
	    !odict_entry_add(od, "class", ODICT_STRING, "webrtc") &&
	    !odict_entry_add(od, "type", ODICT_STRING, type) &&
	    (!key || !odict_entry_add(od, key, ODICT_STRING, val)))
		emit(od);

	mem_deref(od);
}


static void webrtc_end(int err)
{
	char m[128] = "";

	if (!pc)
		return;

	pc = mem_deref(pc);
	pc_media = false;
	webrtc_event("CLOSED", "param", err ? str_error(err, m, sizeof(m))
				      : "closed");
}


/* All local candidates are known: the offer or answer can go out complete. */
static void pc_gather_handler(void *arg)
{
	bool answer = peerconnection_signaling(pc) == SS_HAVE_REMOTE_OFFER;
	struct mbuf *mb = NULL;
	char *sdp = NULL;
	int err;
	(void)arg;

	err = answer ? peerconnection_create_answer(pc, &mb)
		     : peerconnection_create_offer(pc, &mb);
	if (err)
		goto out;

	mb->pos = 0;
	err = mbuf_strdup(mb, &sdp, mb->end);
	if (err)
		goto out;

	webrtc_event(answer ? "ANSWER" : "OFFER", "sdp", sdp);

	/* The offerer starts its checks once the answer is in. */
	if (answer)
		err = peerconnection_start_ice(pc);

 out:
	mem_deref(sdp);
	mem_deref(mb);
	if (err) {
		warning("ctrl_stdio: webrtc %s failed (%m)\n",
			answer ? "answer" : "offer", err);
		webrtc_end(err);
	}
}


static void pc_estab_handler(struct media_track *media, void *arg)
{
	int err = 0;
	(void)arg;

	switch (mediatrack_kind(media)) {

	case MEDIA_KIND_AUDIO:
		err = mediatrack_start_audio(media, baresip_ausrcl(),
					     baresip_aufiltl());
		break;

	case MEDIA_KIND_VIDEO:
		err = mediatrack_start_video(media);
		break;

	default:
		break;
	}

	if (err) {
		warning("ctrl_stdio: webrtc media start failed (%m)\n", err);
		webrtc_end(err);
		return;
	}

	stream_enable(media_get_stream(media), true);

	if (!pc_media) {
		pc_media = true;
		webrtc_event("ESTABLISHED", NULL, NULL);
	}
}


static void pc_close_handler(int err, void *arg)
{
	(void)arg;

	webrtc_end(err ? err : ECONNRESET);
}


static int webrtc_new(bool offerer)
{
	const struct mnat *mnat = mnat_find(baresip_mnatl(), "ice");
	const struct menc *menc = menc_find(baresip_mencl(), "dtls_srtp");
	int err;

	if (pc)
		return EBUSY;

	if (!mnat || !menc)
		return ENOENT;

	pc_config.offerer = offerer;
	err = peerconnection_new(&pc, &pc_config, mnat, menc,
				 pc_gather_handler, pc_estab_handler,
				 pc_close_handler, NULL);
	if (err)
		return err;

	err  = peerconnection_add_audio_track(pc, conf_config(),
					      baresip_aucodecl(),
					      SDP_SENDRECV);
	err |= peerconnection_add_video_track(pc, conf_config(),
					      baresip_vidcodecl(),
					      baresip_vidfiltl(),
					      SDP_SENDRECV);
	if (err)
		pc = mem_deref(pc);

	return err;
}


static int set_remote(const struct odict *descr, enum sdp_type type)
{
	const char *t = odict_string(descr, "type");
	const char *sdp = odict_string(descr, "sdp");
	struct session_description sd = {type, NULL};
	int err;

	if (!t || !sdp || str_casecmp(t, sdptype_name(type)))
		return EINVAL;

	sd.sdp = mbuf_alloc(str_len(sdp));
	if (!sd.sdp)
		return ENOMEM;
	(void)mbuf_write_str(sd.sdp, sdp);
	sd.sdp->pos = 0;

	err = peerconnection_set_remote_descr(pc, &sd);
	session_description_reset(&sd);

	return err;
}


static int webrtc_answer(const struct odict *offer)
{
	int err = webrtc_new(false);

	if (err)
		return err;

	err = set_remote(offer, SDP_OFFER);
	if (err)
		pc = mem_deref(pc);

	return err;
}


static int webrtc_remote(const struct odict *answer)
{
	int err;

	if (!pc || peerconnection_signaling(pc) != SS_HAVE_LOCAL_OFFER)
		return EPROTO;

	err = set_remote(answer, SDP_ANSWER);
	if (!err)
		err = peerconnection_start_ice(pc);
	if (err)
		webrtc_end(err);

	return err;
}


static void handle_line(const char *line, size_t len)
{
	struct mbuf *resp = mbuf_alloc(1024);
	struct re_printf pf = {print_handler, resp};
	struct odict *od = NULL, *out = NULL;
	const char *cmd, *prm, *tok;
	char buf[1024];
	char *data = NULL;
	int err, cerr;

	if (!resp)
		return;

	err = json_decode_odict(&od, 16, line, len, 8);
	if (err) {
		warning("ctrl_stdio: bad JSON (%m)\n", err);
		goto out;
	}

	cmd = odict_string(od, "command");
	prm = odict_string(od, "params");
	tok = odict_string(od, "token");
	if (!cmd)
		goto out;

	if (!str_casecmp(cmd, "webrtc_answer") ||
	    !str_casecmp(cmd, "webrtc_remote")) {
		const struct odict_entry *e = odict_lookup(od, "params");
		const struct odict *d = (e && odict_entry_type(e) ==
					 ODICT_OBJECT) ? odict_entry_object(e)
						       : NULL;
		cerr = !d ? EINVAL : !str_casecmp(cmd, "webrtc_answer")
			? webrtc_answer(d) : webrtc_remote(d);
	}
	else if (!str_casecmp(cmd, "webrtc_offer")) {
		cerr = webrtc_new(true);
	}
	else if (!str_casecmp(cmd, "webrtc_close")) {
		webrtc_end(0);
		cerr = 0;
	}
	else {
		re_snprintf(buf, sizeof(buf), "%s%s%s", cmd, prm ? " " : "",
			    prm ? prm : "");
		cerr = cmd_process_long(baresip_commands(), buf,
					str_len(buf), &pf, NULL);
	}

	resp->pos = 0;
	err = mbuf_strdup(resp, &data, resp->end);
	err |= odict_alloc(&out, 8);
	if (err)
		goto out;

	err  = odict_entry_add(out, "response", ODICT_BOOL, true);
	err |= odict_entry_add(out, "ok", ODICT_BOOL, (bool)!cerr);
	err |= odict_entry_add(out, "data", ODICT_STRING, data);
	if (tok)
		err |= odict_entry_add(out, "token", ODICT_STRING, tok);
	if (!err)
		emit(out);

 out:
	mem_deref(data);
	mem_deref(out);
	mem_deref(od);
	mem_deref(resp);
}


static void stdin_handler(int flags, void *arg)
{
	uint8_t buf[512];
	ssize_t n;
	(void)flags;
	(void)arg;

	/* An app that died shows as a hangup without FD_READ; the read
	 * then returns 0 and ends the helper instead of spinning. */
	n = read(STDIN_FILENO, buf, sizeof(buf));
	if (n <= 0) {
		/* The app is gone: nobody can end a call any more. */
		info("ctrl_stdio: stdin closed, quitting\n");
		fhs = fd_close(fhs);
		ua_stop_all(false);
		return;
	}

	for (ssize_t i = 0; i < n; i++) {
		if (buf[i] != '\n') {
			(void)mbuf_write_u8(linebuf, buf[i]);
			continue;
		}
		handle_line((const char *)linebuf->buf, linebuf->end);
		mbuf_rewind(linebuf);
	}

	if (linebuf->end > MAX_LINE) {
		warning("ctrl_stdio: line too long, dropped\n");
		mbuf_rewind(linebuf);
	}
}


static void event_handler(enum bevent_ev ev, struct bevent *event, void *arg)
{
	struct odict *od = NULL;
	(void)ev;
	(void)arg;

	if (odict_alloc(&od, 8))
		return;

	if (!odict_entry_add(od, "event", ODICT_BOOL, true) &&
	    !bevent_odict_encode(od, event))
		emit(od);

	mem_deref(od);
}


/*
 * Android's sandbox (seccomp) answers a system call it does not allow with
 * SIGSYS, which would end the helper - on Android 15 that happened when the
 * network changed. The call fails with ENOSYS instead, which musl and libre
 * handle (musl falls back to older calls), and its number goes to the log.
 */
static void sigsys_handler(int sig, siginfo_t *si, void *uc_)
{
	ucontext_t *uc = uc_;
	char msg[64] = "ctrl_stdio: SIGSYS, system call ";
	char num[12];
	int n = si->si_syscall, len = 0;
	size_t ml = strlen(msg);
	(void)sig;

	do {
		num[len++] = (char)('0' + n % 10);
		n /= 10;
	} while (n && len < (int)sizeof(num));
	while (len && ml < sizeof(msg) - 2)
		msg[ml++] = num[--len];
	msg[ml++] = '\n';
	(void)!write(STDERR_FILENO, msg, ml);

#if defined(__aarch64__)
	uc->uc_mcontext.regs[0] = (unsigned long)-ENOSYS;
#elif defined(__arm__)
	uc->uc_mcontext.arm_r0 = (unsigned long)-ENOSYS;
#elif defined(__x86_64__)
	uc->uc_mcontext.gregs[REG_RAX] = -ENOSYS;
#else
	(void)uc;
	_exit(128 + SIGSYS);
#endif
}


/* At program start, before baresip's own setup makes its first calls: the
 * helper is linked statically, so this module's constructor runs then. */
__attribute__((constructor)) static void sigsys_install(void)
{
	struct sigaction sa;

	memset(&sa, 0, sizeof(sa));
	sa.sa_sigaction = sigsys_handler;
	sa.sa_flags = SA_SIGINFO;
	sigaction(SIGSYS, &sa, NULL);
}


static void log_handler(uint32_t level, const char *msg)
{
	(void)level;
	fputs(msg, stderr);
}


static struct log logh = {
	.h = log_handler,
};


static int module_init(void)
{
	int err;

	char stun[256];

	linebuf = mbuf_alloc(512);
	if (!linebuf)
		return ENOMEM;

	if (!conf_get_str(conf_cur(), "webrtc_stun", stun, sizeof(stun))) {
		struct pl pl;

		pl_set_str(&pl, stun);
		err = stunuri_decode(&pc_config.ice_server, &pl);
		if (err)
			warning("ctrl_stdio: webrtc_stun '%s' (%m)\n", stun,
				err);
	}

	log_enable_stdout(false);
	log_register_handler(&logh);

	err = fd_listen(&fhs, STDIN_FILENO, FD_READ, stdin_handler, NULL);
	if (err)
		return err;

	return bevent_register(event_handler, NULL);
}


static int module_close(void)
{
	bevent_unregister(event_handler);
	pc = mem_deref(pc);
	pc_config.ice_server = mem_deref(pc_config.ice_server);
	fhs = fd_close(fhs);
	log_unregister_handler(&logh);
	linebuf = mem_deref(linebuf);

	return 0;
}


const struct mod_export DECL_EXPORTS(ctrl_stdio) = {
	"ctrl_stdio",
	"application",
	module_init,
	module_close
};
