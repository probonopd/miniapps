/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */

/*
 * Raw ALSA capture for the Echo Show 5 2nd gen (cronos).
 *
 *   echocap <device> <out.raw> <seconds> [channels] [rate] [period] [periods]
 *
 * tinycap on this device cannot ask for S24_3LE, which is the only format the
 * mic PCM offers, and no compiler or libasound exists there. So this talks to
 * the kernel directly and needs no libc: it runs on any ARMv7 Android.
 */

#include <stddef.h>
#include <stdint.h>
#include <linux/ioctl.h>
#include <sound/asound.h>

static long sys3(long nr, long a, long b, long c)
{
	register long r7 __asm__("r7") = nr;
	register long r0 __asm__("r0") = a;
	register long r1 __asm__("r1") = b;
	register long r2 __asm__("r2") = c;
	__asm__ volatile("svc 0" : "+r"(r0) : "r"(r7), "r"(r1), "r"(r2) : "memory");
	return r0;
}

#define SYS_exit 1
#define SYS_write 4
#define SYS_open 5
#define SYS_close 6
#define SYS_ioctl 54
#define O_RDWR 2
#define O_WRONLY_CREAT_TRUNC 01101

static unsigned long slen(const char *s)
{
	unsigned long n = 0;
	while (s[n]) {
		n++;
	}
	return n;
}

static void say(const char *s)
{
	sys3(SYS_write, 2, (long)s, slen(s));
}

static void saynum(long v)
{
	char b[24];
	int i = 23;
	int neg = v < 0;
	unsigned long u = neg ? -v : v;
	b[i] = 0;
	do {
		b[--i] = '0' + u % 10;
		u /= 10;
	} while (u);
	if (neg) {
		b[--i] = '-';
	}
	say(b + i);
}

static void die(const char *what, long err)
{
	say(what);
	say(": errno ");
	saynum(-err);
	say("\n");
	sys3(SYS_exit, 1, 0, 0);
}

static long num(const char *s)
{
	long v = 0;
	while (*s >= '0' && *s <= '9') {
		v = v * 10 + (*s++ - '0');
	}
	return v;
}

static void *zero(void *p, unsigned long n)
{
	volatile unsigned char *c = p;
	while (n--) {
		*c++ = 0;
	}
	return p;
}

/* The compiler may emit calls to these for struct initialization. */
void *memset(void *p, int v, unsigned long n)
{
	volatile unsigned char *c = p;
	while (n--) {
		*c++ = v;
	}
	return p;
}

static void any(struct snd_pcm_hw_params *p)
{
	int i;
	zero(p, sizeof(*p));
	for (i = 0; i <= SNDRV_PCM_HW_PARAM_LAST_MASK - SNDRV_PCM_HW_PARAM_FIRST_MASK; i++) {
		int j;
		for (j = 0; j < 8; j++) {
			p->masks[i].bits[j] = 0xffffffffu;
		}
	}
	for (i = 0; i <= SNDRV_PCM_HW_PARAM_LAST_INTERVAL - SNDRV_PCM_HW_PARAM_FIRST_INTERVAL; i++) {
		p->intervals[i].min = 0;
		p->intervals[i].max = 0xffffffffu;
	}
	p->rmask = 0xffffffffu;
}

static void setmask(struct snd_pcm_hw_params *p, int param, unsigned bit)
{
	struct snd_mask *m = &p->masks[param - SNDRV_PCM_HW_PARAM_FIRST_MASK];
	zero(m, sizeof(*m));
	m->bits[bit >> 5] = 1u << (bit & 31);
}

static void setint(struct snd_pcm_hw_params *p, int param, unsigned v)
{
	struct snd_interval *i = &p->intervals[param - SNDRV_PCM_HW_PARAM_FIRST_INTERVAL];
	i->min = i->max = v;
	i->integer = 1;
}

static struct snd_pcm_hw_params hw;
static unsigned char buf[1 << 18];

void run(int argc, char **argv)
{
	char path[32] = "/dev/snd/pcmC0D00c";
	long dev, seconds, ch, rate, period, periods, fd, out, r;
	long frames, done = 0, framebytes;
	struct snd_xferi x;

	if (argc < 4) {
		say("usage: echocap <device> <out.raw> <seconds> [channels] [rate] [period] [periods]\n");
		sys3(SYS_exit, 2, 0, 0);
	}
	dev = num(argv[1]);
	seconds = num(argv[3]);
	ch = argc > 4 ? num(argv[4]) : 4;
	rate = argc > 5 ? num(argv[5]) : 16000;
	period = argc > 6 ? num(argv[6]) : 257;
	periods = argc > 7 ? num(argv[7]) : 10;

	if (dev < 10) {
		path[15] = '0' + dev;
		path[16] = 'c';
		path[17] = 0;
	} else {
		path[15] = '0' + dev / 10;
		path[16] = '0' + dev % 10;
	}
	fd = sys3(SYS_open, (long)path, O_RDWR, 0);
	if (fd < 0) {
		die("open", fd);
	}

	any(&hw);
	setmask(&hw, SNDRV_PCM_HW_PARAM_ACCESS, SNDRV_PCM_ACCESS_RW_INTERLEAVED);
	setmask(&hw, SNDRV_PCM_HW_PARAM_FORMAT, SNDRV_PCM_FORMAT_S24_3LE);
	setint(&hw, SNDRV_PCM_HW_PARAM_CHANNELS, ch);
	setint(&hw, SNDRV_PCM_HW_PARAM_RATE, rate);
	setint(&hw, SNDRV_PCM_HW_PARAM_PERIOD_SIZE, period);
	setint(&hw, SNDRV_PCM_HW_PARAM_PERIODS, periods);
	r = sys3(SYS_ioctl, fd, SNDRV_PCM_IOCTL_HW_PARAMS, (long)&hw);
	if (r < 0) {
		die("hw_params", r);
	}
	r = sys3(SYS_ioctl, fd, SNDRV_PCM_IOCTL_PREPARE, 0);
	if (r < 0) {
		die("prepare", r);
	}
	r = sys3(SYS_ioctl, fd, SNDRV_PCM_IOCTL_START, 0);
	if (r < 0) {
		die("start", r);
	}

	out = sys3(SYS_open, (long)argv[2], O_WRONLY_CREAT_TRUNC, 0644);
	if (out < 0) {
		die("create", out);
	}
	framebytes = ch * 3;
	frames = seconds * rate;
	while (done < frames) {
		x.result = 0;
		x.buf = buf;
		x.frames = period;
		r = sys3(SYS_ioctl, fd, SNDRV_PCM_IOCTL_READI_FRAMES, (long)&x);
		if (r == -32) {
			/* An overrun must not look like continuous audio. */
			die("overrun", r);
		}
		if (r < 0) {
			die("read", r);
		}
		sys3(SYS_write, out, (long)buf, x.result * framebytes);
		done += x.result;
	}
	sys3(SYS_close, out, 0, 0);
	sys3(SYS_close, fd, 0, 0);
	say("frames ");
	saynum(done);
	say("\n");
	sys3(SYS_exit, 0, 0, 0);
}

__asm__(".text\n.global _start\n_start:\n  ldr r0,[sp]\n  add r1,sp,#4\n  bl run\n");
