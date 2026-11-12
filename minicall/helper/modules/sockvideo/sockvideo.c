/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT AND BSD-3-Clause
 *
 * The H.264 depacketizer and the SDP format code follow baresip's avcodec
 * module, Copyright (C) 2010 Alfred E. Heggestad, under these terms:
 *
 *   Redistribution and use in source and binary forms, with or without
 *   modification, are permitted provided that the following conditions are met:
 *   1. Redistributions of source code must retain the above copyright notice,
 *      this list of conditions and the following disclaimer.
 *   2. Redistributions in binary form must reproduce the above copyright
 *      notice, this list of conditions and the following disclaimer in the
 *      documentation and/or other materials provided with the distribution.
 *   3. Neither the name of the copyright holder nor the names of its
 *      contributors may be used to endorse or promote products derived from
 *      this software without specific prior written permission.
 *
 *   THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS
 *   IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO,
 *   THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR
 *   PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR
 *   CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL,
 *   EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO,
 *   PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS;
 *   OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY,
 *   WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR
 *   OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF
 *   ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
 */

/*
 * H.264 video coded by the app, over abstract Unix sockets.
 *
 * The device's hardware codecs are only reachable through MediaCodec in the
 * app's Java process; software VP8 cost the Echo Show most of a core at
 * 640x360. So the app encodes the camera and decodes onto the screen, and
 * this module only moves access units between the app and RTP:
 *
 *   video_source      sockvideo,<name>   encoded camera from the app
 *   sockvideo_display <name>             access units to the app's decoder
 *
 * Both sockets carry frames of a 13-byte header (u32 length, u8 flags with
 * bit 0 = keyframe, u64 timestamp in microseconds, all big endian) and the
 * access unit in Annex B. Towards the helper, a single 'k' byte asks for a
 * keyframe: on the source socket the app's encoder makes one, on the display
 * socket the decoder lost track and the far end is asked for one.
 * On connecting, the source sends "width height fps bitrate\n".
 */

#include <string.h>
#include <unistd.h>
#include <errno.h>
#include <poll.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <sys/un.h>
#include <re_atomic.h>
#include <re.h>
#include <rem.h>
#include <baresip.h>


enum {
	HDR_SIZE = 13,
	MAX_AU = 1024 * 1024,
	DECODE_MAXSZ = 524288,
};


struct vidsrc_st {
	thrd_t thread;
	RE_ATOMIC bool run;
	int fd;
	vidsrc_packet_h *packeth;
	vidsrc_error_h *errorh;
	void *arg;
};

struct videnc_state {
	struct videnc_param encprm;
	videnc_packet_h *pkth;
	const struct video *vid;
};

struct viddec_state {
	struct mbuf *mb;
	size_t frag_start;
	bool frag;
	uint16_t frag_seq;
	bool got_keyframe;
	int fd;
	/* for the log: where frames go */
	unsigned n_sent, n_nokey, n_lost;
};


static struct vidsrc *vidsrc;

/* The source that gets keyframe requests: one call at a time. */
static mtx_t src_lock;
static struct vidsrc_st *cur_src;


static int sock_connect(const char *name)
{
	struct sockaddr_un sun;
	size_t len = strlen(name);
	int fd;

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
		ssize_t w = send(fd, p, n, MSG_NOSIGNAL);
		if (w < 0 && errno == EINTR)
			continue;
		if (w <= 0)
			return false;
		p += w;
		n -= (size_t)w;
	}
	return true;
}


/* True when the app sent a 'k' since the last look. */
static bool key_requested(int fd)
{
	uint8_t c;
	bool req = false;

	while (recv(fd, &c, 1, MSG_DONTWAIT) == 1) {
		if (c == 'k')
			req = true;
	}

	return req;
}


/*
 * Source
 */

static int src_thread(void *arg)
{
	struct vidsrc_st *st = arg;
	uint8_t hdr[HDR_SIZE];
	uint8_t *buf = NULL;
	size_t cap = 0;

	while (re_atomic_rlx(&st->run)) {
		struct vidpacket pkt;
		uint32_t len;
		uint64_t ts = 0;

		if (!read_full(st->fd, hdr, sizeof(hdr)))
			break;

		len = (uint32_t)hdr[0] << 24 | (uint32_t)hdr[1] << 16 |
		      (uint32_t)hdr[2] << 8 | hdr[3];
		if (len == 0 || len > MAX_AU) {
			warning("sockvideo: bad frame length %u\n", len);
			break;
		}
		for (int i = 0; i < 8; i++)
			ts = ts << 8 | hdr[5 + i];

		if (len > cap) {
			/* libre's mem_realloc() does not take NULL */
			uint8_t *nb = buf ? mem_realloc(buf, len)
					  : mem_alloc(len, NULL);
			if (!nb) {
				warning("sockvideo: out of memory\n");
				break;
			}
			buf = nb;
			cap = len;
		}
		if (!read_full(st->fd, buf, len))
			break;

		pkt.buf = buf;
		pkt.size = len;
		pkt.timestamp = ts;
		pkt.keyframe = hdr[4] & 1;
		pkt.picup = false;
		st->packeth(&pkt, st->arg);
	}

	mem_deref(buf);

	if (re_atomic_rlx(&st->run))
		warning("sockvideo: source closed by the app\n");

	return 0;
}


static void src_destructor(void *arg)
{
	struct vidsrc_st *st = arg;

	mtx_lock(&src_lock);
	if (cur_src == st)
		cur_src = NULL;
	mtx_unlock(&src_lock);

	if (re_atomic_rlx(&st->run)) {
		re_atomic_rlx_set(&st->run, false);
		shutdown(st->fd, SHUT_RDWR);
		thrd_join(st->thread, NULL);
	}
	if (st->fd >= 0)
		close(st->fd);
}


static int src_alloc(struct vidsrc_st **stp, const struct vidsrc *vs,
		     struct vidsrc_prm *prm, const struct vidsz *size,
		     const char *fmt, const char *dev,
		     vidsrc_frame_h *frameh, vidsrc_packet_h *packeth,
		     vidsrc_error_h *errorh, void *arg)
{
	struct vidsrc_st *st;
	char line[64];
	int n, err = 0;
	(void)vs;
	(void)fmt;
	(void)frameh;

	if (!stp || !prm || !size || !packeth || !str_isset(dev))
		return EINVAL;

	st = mem_zalloc(sizeof(*st), src_destructor);
	if (!st)
		return ENOMEM;

	st->packeth = packeth;
	st->errorh = errorh;
	st->arg = arg;

	st->fd = sock_connect(dev);
	if (st->fd < 0) {
		err = -st->fd;
		st->fd = -1;
		warning("sockvideo: source %s: %m\n", dev, err);
		goto out;
	}

	n = re_snprintf(line, sizeof(line), "%u %u %u %u\n", size->w,
			size->h, (unsigned)prm->fps,
			conf_config()->video.bitrate);
	if (n <= 0 || !write_full(st->fd, (uint8_t *)line, (size_t)n)) {
		err = EIO;
		goto out;
	}

	re_atomic_rlx_set(&st->run, true);
	err = thread_create_name(&st->thread, "sockvideo_src", src_thread,
				 st);
	if (err) {
		re_atomic_rlx_set(&st->run, false);
		goto out;
	}

	mtx_lock(&src_lock);
	cur_src = st;
	mtx_unlock(&src_lock);

	info("sockvideo: source %ux%u, %.0f fps\n", size->w, size->h,
	     prm->fps);

 out:
	if (err)
		mem_deref(st);
	else
		*stp = st;

	return err;
}


/*
 * Encoder: the app has encoded already, only RTP packets are made here.
 */

static int enc_update(struct videnc_state **vesp, const struct vidcodec *vc,
		      struct videnc_param *prm, const char *fmtp,
		      videnc_packet_h *pkth, const struct video *vid)
{
	struct videnc_state *st;
	(void)vc;
	(void)fmtp;

	if (!vesp || !prm || !pkth)
		return EINVAL;

	if (*vesp)
		st = *vesp;
	else {
		st = mem_zalloc(sizeof(*st), NULL);
		if (!st)
			return ENOMEM;
		*vesp = st;
	}

	st->encprm = *prm;
	st->pkth = pkth;
	st->vid = vid;

	return 0;
}


/* Raw frames never come: the source delivers encoded packets only. */
static int enc_encode(struct videnc_state *ves, bool update,
		      const struct vidframe *frame, uint64_t timestamp)
{
	(void)ves;
	(void)update;
	(void)frame;
	(void)timestamp;

	return ENOTSUP;
}


static int enc_packetize(struct videnc_state *ves,
			 const struct vidpacket *packet)
{
	if (!ves || !packet)
		return EINVAL;

	if (packet->picup) {
		mtx_lock(&src_lock);
		if (cur_src) {
			const uint8_t k = 'k';
			(void)send(cur_src->fd, &k, 1,
				   MSG_DONTWAIT | MSG_NOSIGNAL);
		}
		mtx_unlock(&src_lock);
	}

	return h264_packetize(video_calc_rtp_timestamp_fix(packet->timestamp),
			      packet->buf, packet->size, ves->encprm.pktsize,
			      (h264_packet_h *)ves->pkth, (void *)ves->vid);
}


/*
 * Decoder: RTP to whole access units for the app's MediaCodec.
 */

static void dec_destructor(void *arg)
{
	struct viddec_state *st = arg;

	mem_deref(st->mb);
	if (st->fd >= 0)
		close(st->fd);
}


static int dec_update(struct viddec_state **vdsp, const struct vidcodec *vc,
		      const char *fmtp, const struct video *vid)
{
	struct viddec_state *st;
	char name[128];
	struct timeval tv = {0, 200000};
	int err = 0;
	(void)vc;
	(void)fmtp;
	(void)vid;

	if (!vdsp)
		return EINVAL;

	if (*vdsp)
		return 0;

	if (conf_get_str(conf_cur(), "sockvideo_display", name,
			 sizeof(name))) {
		warning("sockvideo: sockvideo_display not configured\n");
		return EINVAL;
	}

	st = mem_zalloc(sizeof(*st), dec_destructor);
	if (!st)
		return ENOMEM;

	st->mb = mbuf_alloc(1024);
	if (!st->mb) {
		err = ENOMEM;
		goto out;
	}

	st->fd = sock_connect(name);
	if (st->fd < 0) {
		err = -st->fd;
		st->fd = -1;
		warning("sockvideo: display %s: %m\n", name, err);
		goto out;
	}

	/* A stuck app costs at most this per frame, never the call. */
	(void)setsockopt(st->fd, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof(tv));

 out:
	if (err)
		mem_deref(st);
	else
		*vdsp = st;

	return err;
}


static inline void fragment_rewind(struct viddec_state *st)
{
	st->mb->pos = st->frag_start;
	st->mb->end = st->frag_start;
}


static bool has_idr(const uint8_t *p, size_t len)
{
	const uint8_t *end = p + len;
	const uint8_t *r = h264_find_startcode(p, end);

	while (r < end) {
		while (r < end && !*(r++))
			;
		if (r < end && (r[0] & 0x1f) == H264_NALU_IDR_SLICE)
			return true;
		r = h264_find_startcode(r, end);
	}

	return false;
}


static bool send_au(struct viddec_state *st, const uint8_t *p, size_t len,
		    bool key, uint64_t ts)
{
	uint8_t hdr[HDR_SIZE];

	hdr[0] = (uint8_t)(len >> 24);
	hdr[1] = (uint8_t)(len >> 16);
	hdr[2] = (uint8_t)(len >> 8);
	hdr[3] = (uint8_t)len;
	hdr[4] = key ? 1 : 0;
	for (int i = 0; i < 8; i++)
		hdr[5 + i] = (uint8_t)(ts >> (56 - 8 * i));

	return write_full(st->fd, hdr, sizeof(hdr)) &&
	       write_full(st->fd, p, len);
}


static int dec_decode(struct viddec_state *st, struct vidframe *frame,
		      struct viddec_packet *pkt)
{
	static const uint8_t nal_seq[3] = {0, 0, 1};
	struct h264_nal_header h264_hdr;
	struct mbuf *src;
	bool key;
	int err;
	(void)frame;

	if (!st || !pkt || !pkt->mb)
		return EINVAL;

	pkt->intra = false;
	src = pkt->mb;

	err = h264_nal_header_decode(&h264_hdr, src);
	if (err)
		return err;

	if (h264_hdr.f)
		return EBADMSG;

	if (st->frag && h264_hdr.type != H264_NALU_FU_A) {
		fragment_rewind(st);
		st->frag = false;
		++st->n_lost;
	}

	if (1 <= h264_hdr.type && h264_hdr.type <= 23) {
		--src->pos;
		err  = mbuf_write_mem(st->mb, nal_seq, 3);
		err |= mbuf_write_mem(st->mb, mbuf_buf(src),
				      mbuf_get_left(src));
		if (err)
			goto out;
	}
	else if (h264_hdr.type == H264_NALU_FU_A) {
		struct h264_fu fu;

		err = h264_fu_hdr_decode(&fu, src);
		if (err)
			return err;
		h264_hdr.type = fu.type;

		if (fu.s) {
			if (st->frag)
				fragment_rewind(st);

			st->frag_start = st->mb->pos;
			st->frag = true;

			err  = mbuf_write_mem(st->mb, nal_seq, 3);
			err |= h264_nal_header_encode(st->mb, &h264_hdr);
			if (err)
				goto out;
		}
		else {
			if (!st->frag)
				return 0;

			if (rtp_seq_diff(st->frag_seq, pkt->hdr->seq) != 1) {
				fragment_rewind(st);
				st->frag = false;
				++st->n_lost;
				return 0;
			}
		}

		err = mbuf_write_mem(st->mb, mbuf_buf(src),
				     mbuf_get_left(src));
		if (err)
			goto out;

		if (fu.e)
			st->frag = false;

		st->frag_seq = pkt->hdr->seq;
	}
	else if (h264_hdr.type == H264_NALU_STAP_A) {
		err = h264_stap_decode_annexb(st->mb, src);
		if (err)
			goto out;
	}
	else {
		return EBADMSG;
	}

	if (!pkt->hdr->m) {
		if (st->mb->end > DECODE_MAXSZ) {
			err = ENOMEM;
			goto out;
		}
		return 0;
	}

	if (st->frag) {
		err = EPROTO;
		goto out;
	}

	/* A whole access unit. Until the decoder has a keyframe, everything
	 * else is useless to it; EPROTO makes the core ask the far end for
	 * one. */
	if (key_requested(st->fd))
		st->got_keyframe = false;

	key = has_idr(st->mb->buf, st->mb->end);
	if (key)
		st->got_keyframe = true;

	if (!st->got_keyframe) {
		++st->n_nokey;
		err = EPROTO;
		goto out;
	}

	pkt->intra = key;
	if (!send_au(st, st->mb->buf, st->mb->end, key, pkt->timestamp))
		warning("sockvideo: display did not take a frame\n");

	if (++st->n_sent % 150 == 0)
		info("sockvideo: display: %u frames, %u dropped waiting for"
		     " a keyframe, %u broken by lost packets\n",
		     st->n_sent, st->n_nokey, st->n_lost);

 out:
	mbuf_rewind(st->mb);
	st->frag = false;

	return err;
}


/*
 * SDP
 */

static uint32_t packetization_mode(const char *fmtp)
{
	struct pl pl, mode;

	if (!fmtp)
		return 0;

	pl_set_str(&pl, fmtp);

	if (fmt_param_get(&pl, "packetization-mode", &mode))
		return pl_u32(&mode);

	return 0;
}


/* Constrained baseline, level 3.1: what hardware encoders of old devices
 * produce, and what every SIP and WebRTC client decodes. */
static int fmtp_enc(struct mbuf *mb, const struct sdp_format *fmt,
		    bool offer, void *arg)
{
	struct vidcodec *vc = arg;
	(void)offer;

	if (!mb || !fmt || !vc)
		return 0;

	return mbuf_printf(mb, "a=fmtp:%s %s;profile-level-id=42e01f\r\n",
			   fmt->id, vc->variant);
}


static bool fmtp_cmp(const char *lfmtp, const char *rfmtp, void *arg)
{
	const struct vidcodec *vc = arg;
	(void)lfmtp;

	if (!vc)
		return false;

	return packetization_mode(vc->variant) == packetization_mode(rfmtp);
}


static struct vidcodec h264 = {
	.name       = "H264",
	.variant    = "packetization-mode=1",
	.encupdh    = enc_update,
	.ench       = enc_encode,
	.decupdh    = dec_update,
	.dech       = dec_decode,
	.fmtp_ench  = fmtp_enc,
	.fmtp_cmph  = fmtp_cmp,
	.packetizeh = enc_packetize,
};


static int module_init(void)
{
	int err;

	err = mtx_init(&src_lock, mtx_plain) != thrd_success;
	if (err)
		return ENOMEM;

	vidcodec_register(baresip_vidcodecl(), &h264);

	return vidsrc_register(&vidsrc, baresip_vidsrcl(), "sockvideo",
			       src_alloc, NULL);
}


static int module_close(void)
{
	vidsrc = mem_deref(vidsrc);
	vidcodec_unregister(&h264);
	mtx_destroy(&src_lock);

	return 0;
}


EXPORT_SYM const struct mod_export DECL_EXPORTS(sockvideo) = {
	"sockvideo",
	"vidcodec",
	module_init,
	module_close
};
