#!/usr/bin/env python3
#
# Copyright (c) 2026 Simon Peter
#
# SPDX-License-Identifier: BSD-2-Clause
#
# Device-to-device WebRTC on this machine: helper A offers (webrtc_offer), helper B
# answers (webrtc_answer), A takes the answer (webrtc_remote), as two minicall
# devices do over MQTT. fakeapp.py is camera, microphone, screen and speaker of
# both. Exits non-zero unless both sides decode video and get audio.
#
#   webrtc-p2p.py WORKDIR [SECONDS]
import json, os, queue, subprocess, sys, threading, time

here = os.path.dirname(os.path.abspath(__file__))
work = sys.argv[1]
seconds = int(sys.argv[2]) if len(sys.argv) > 2 else 12
helper = os.path.join(here, '..', 'helper', 'out', 'host', 'baresip')
os.makedirs(work, exist_ok=True)
cam = os.path.join(work, 'cam.h264')
subprocess.run(['ffmpeg', '-hide_banner', '-loglevel', 'error', '-y', '-f', 'lavfi', '-i', 'testsrc=size=640x480:rate=30',
	'-t', '4', '-c:v', 'libx264', '-profile:v', 'baseline', '-pix_fmt', 'yuv420p', '-g', '30', '-bsf:v',
	'h264_metadata=aud=insert', '-f', 'h264', cam], check=True)


def side(name, port, tone):
	d = os.path.join(work, name)
	os.makedirs(d, exist_ok=True)
	with open(os.path.join(d, 'config'), 'w') as f:
		f.write(f'''sip_listen 127.0.0.1:{port}
audio_source sockaudio,mcp2p{name}.mic
audio_player sockaudio,mcp2p{name}.spk
ausrc_channels 1
auplay_channels 1
video_source sockvideo,mcp2p{name}.cam
video_display fakevideo,nil
sockvideo_display mcp2p{name}.disp
video_size 640x480
video_fps 30
video_bitrate 1000000
webrtc_stun stun:stun.l.google.com:19302
''')
		for m in 'opus auconv auresamp webrtc_aec sockaudio sockvideo fakevideo ice stun dtls_srtp account contact menu ctrl_stdio'.split():
			f.write(f'module {m}.so\n')
	open(os.path.join(d, 'accounts'), 'w').close()
	open(os.path.join(d, 'contacts'), 'w').close()
	fake = subprocess.Popen([sys.executable, '-I', os.path.join(here, 'fakeapp.py'), f'mcp2p{name}', cam,
		os.path.join(d, 'out'), str(tone)], env=dict(os.environ, FAKEAPP_SECONDS=str(seconds + 15)))
	time.sleep(0.5)
	p = subprocess.Popen([helper, '-f', d], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
		stderr=open(os.path.join(d, 'stderr'), 'w'), text=True)
	q = queue.Queue()

	def reader():
		for line in p.stdout:
			if line.startswith('{') and '"webrtc"' in line:
				q.put(json.loads(line))
	threading.Thread(target=reader, daemon=True).start()

	def cmd(c, params=None):
		o = {'command': c}
		if params is not None:
			o['params'] = params
		p.stdin.write(json.dumps(o) + '\n')
		p.stdin.flush()

	def wait(t):
		while True:
			e = q.get(timeout=20)
			if e['type'] == t:
				return e
	return p, fake, cmd, wait, d


pa, fa, cmd_a, wait_a, da = side('a', 5096, 660)
pb, fb, cmd_b, wait_b, db = side('b', 5098, 440)
cmd_a('webrtc_offer')
offer = wait_a('OFFER')
cmd_b('webrtc_answer', {'type': 'offer', 'sdp': offer['sdp']})
answer = wait_b('ANSWER')
cmd_a('webrtc_remote', {'type': 'answer', 'sdp': answer['sdp']})
wait_a('ESTABLISHED')
wait_b('ESTABLISHED')
time.sleep(seconds)
for p in (pa, pb):
	p.stdin.close()
	p.wait(timeout=10)

fail = 0
for name, d in (('a', da), ('b', db)):
	r = subprocess.run(['ffprobe', '-v', 'error', '-count_frames', '-select_streams', 'v:0', '-show_entries',
		'stream=nb_read_frames', '-of', 'csv=p=0', os.path.join(d, 'out', 'display.h264')], capture_output=True, text=True)
	frames = int(r.stdout.strip() or 0) if r.stdout.strip().isdigit() else 0
	audio = os.path.getsize(os.path.join(d, 'out', 'speaker.raw')) if os.path.exists(os.path.join(d, 'out', 'speaker.raw')) else 0
	ok = frames >= seconds * 20 and audio > 300000
	fail |= not ok
	print(f'{"ok  " if ok else "FAIL"} {name}: {frames} video frames decoded, {audio} bytes audio')
sys.exit(fail)
