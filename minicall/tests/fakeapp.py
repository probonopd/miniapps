#!/usr/bin/env python3
#
# Copyright (c) 2026 Simon Peter
#
# SPDX-License-Identifier: MIT
#
# Stands in for the Android app on a PC: serves the helper's sockaudio and
# sockvideo sockets so that two helpers can call each other in a headless test.
#
#   fakeapp.py PREFIX IN.h264 OUTDIR TONE_HZ
#
# Microphone: a sine of TONE_HZ. Speaker: OUTDIR/speaker.raw. Camera: the
# access units of IN.h264 (Annex B with access unit delimiters) at 30 fps,
# looped. Display: OUTDIR/display.h264 plus OUTDIR/display.log.
import math, os, socket, struct, sys, threading, time

prefix, src_path, outdir, tone = sys.argv[1], sys.argv[2], sys.argv[3], int(sys.argv[4])
os.makedirs(outdir, exist_ok=True)


def serve(name, fn):
	s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
	s.bind('\0' + name)
	s.listen(4)

	def run():
		while True:
			c, _ = s.accept()
			threading.Thread(target=fn, args=(c,), daemon=True).start()
	threading.Thread(target=run, daemon=True).start()


def header_line(c):
	h = b''
	while not h.endswith(b'\n'):
		b = c.recv(1)
		if not b:
			raise OSError('closed')
		h += b
	return h.decode().split()


def mic(c):
	srate, ch, ptime = map(int, header_line(c))
	n = srate * ptime // 1000
	i = 0
	while True:
		buf = b''.join(struct.pack('<h', int(8000 * math.sin(2 * math.pi * tone * (i + k) / srate))) * ch
			for k in range(n))
		i += n
		try:
			c.sendall(buf)
		except OSError:
			return
		time.sleep(ptime / 1000)


def speaker(c):
	srate, ch, ptime = map(int, header_line(c))
	n = srate * ptime // 1000 * ch * 2
	with open(os.path.join(outdir, 'speaker.raw'), 'wb') as f:
		f.write(f'{srate} {ch}\n'.encode())
		while True:
			try:
				c.sendall(b'r')
				d = b''
				while len(d) < n:
					x = c.recv(n - len(d))
					if not x:
						return
					d += x
			except OSError:
				return
			f.write(d)
			f.flush()
			time.sleep(ptime / 1000)


def access_units(data):
	aud = b'\x00\x00\x00\x01\x09'
	parts = data.split(aud)
	return [aud + p for p in parts if p]


def camera(c):
	w, h, fps, br = header_line(c)
	log = open(os.path.join(outdir, 'camera.log'), 'w')
	log.write(f'asked {w}x{h} {fps} fps {br} bit/s\n')
	aus = access_units(open(src_path, 'rb').read())
	c.setblocking(True)
	t0 = time.time()
	i = 0
	while True:
		au = aus[i % len(aus)]
		key = 1 if b'\x00\x00\x01\x65' in au or b'\x00\x00\x00\x01\x65' in au else 0
		ts = int(i * 1e6 / 30)
		try:
			c.sendall(struct.pack('>IBQ', len(au), key, ts) + au)
		except OSError:
			return
		c.setblocking(False)
		try:
			if b'k' in c.recv(64):
				log.write(f'keyframe asked at frame {i}\n')
				log.flush()
		except (BlockingIOError, OSError):
			pass
		c.setblocking(True)
		i += 1
		time.sleep(max(0, t0 + i / 30 - time.time()))


def display(c):
	with open(os.path.join(outdir, 'display.h264'), 'wb') as f, open(os.path.join(outdir, 'display.log'), 'w') as log:
		n = keys = 0
		while True:
			hdr = b''
			while len(hdr) < 13:
				x = c.recv(13 - len(hdr))
				if not x:
					return
				hdr += x
			ln, flags, ts = struct.unpack('>IBQ', hdr)
			d = b''
			while len(d) < ln:
				x = c.recv(ln - len(d))
				if not x:
					return
				d += x
			f.write(d)
			n += 1
			keys += flags & 1
			if n % 30 == 0:
				log.write(f'{n} frames, {keys} keyframes, ts {ts}\n')
				log.flush()


serve(prefix + '.mic', mic)
serve(prefix + '.spk', speaker)
serve(prefix + '.cam', camera)
serve(prefix + '.disp', display)
time.sleep(float(os.environ.get('FAKEAPP_SECONDS', '40')))
