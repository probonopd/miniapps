#!/usr/bin/env python3
#
# Copyright (c) 2026 Simon Peter
#
# SPDX-License-Identifier: BSD-2-Clause
#
# Browser-to-helper call test on this machine: runs the host helper with
# fakeapp.py as its camera, microphone, screen and speaker, and serves
# http://localhost:8099/ with a page that calls it over WebRTC (canvas video,
# oscillator audio, so no camera is needed). POST /offer hands the browser's
# offer to the helper (webrtc_answer over ctrl_stdio) and returns its answer.
#
#   webrtc-test.py WORKDIR SECONDS
import json, os, queue, subprocess, sys, threading, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

here = os.path.dirname(os.path.abspath(__file__))
workdir, seconds = sys.argv[1], int(sys.argv[2])
helper = os.path.join(here, '..', 'helper', 'out', 'host', 'baresip')
os.makedirs(workdir, exist_ok=True)

subprocess.run(['ffmpeg', '-hide_banner', '-loglevel', 'error', '-y', '-f', 'lavfi', '-i',
	'testsrc=size=640x480:rate=30', '-t', '4', '-c:v', 'libx264', '-profile:v', 'baseline', '-pix_fmt', 'yuv420p',
	'-g', '30', '-bsf:v', 'h264_metadata=aud=insert', '-f', 'h264', os.path.join(workdir, 'cam.h264')], check=True)
with open(os.path.join(workdir, 'config'), 'w') as f:
	f.write('''sip_listen 127.0.0.1:5090
audio_source sockaudio,mcweb.mic
audio_player sockaudio,mcweb.spk
ausrc_channels 1
auplay_channels 1
video_source sockvideo,mcweb.cam
video_display fakevideo,nil
sockvideo_display mcweb.disp
video_size 640x480
video_fps 30
video_bitrate 1000000
webrtc_stun stun:stun.l.google.com:19302
''')
	for m in 'opus auconv auresamp sockaudio sockvideo fakevideo ice stun dtls_srtp account contact menu ctrl_stdio'.split():
		f.write(f'module {m}.so\n')
open(os.path.join(workdir, 'accounts'), 'w').close()
open(os.path.join(workdir, 'contacts'), 'w').close()

fake = subprocess.Popen([sys.executable, '-I', os.path.join(here, 'fakeapp.py'), 'mcweb', os.path.join(workdir, 'cam.h264'),
	os.path.join(workdir, 'out'), '440'], env=dict(os.environ, FAKEAPP_SECONDS=str(seconds + 5)))
time.sleep(1)
proc = subprocess.Popen([helper, '-f', workdir], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
	stderr=open(os.path.join(workdir, 'stderr'), 'w'), text=True)
events = queue.Queue()
eventlog = open(os.path.join(workdir, 'events'), 'w')


def reader():
	for line in proc.stdout:
		if line.startswith('{'):
			eventlog.write(line)
			eventlog.flush()
			o = json.loads(line)
			if o.get('class') == 'webrtc':
				events.put(o)
threading.Thread(target=reader, daemon=True).start()

PAGE = '''<!doctype html><meta charset="utf-8"><title>webrtc test</title>
<video id="v" autoplay playsinline muted></video><pre id="log"></pre>
<script>
const log = s => document.getElementById('log').textContent += s + '\\n';
async function call() {
	const c = document.createElement('canvas'); c.width = 640; c.height = 480;
	const g = c.getContext('2d'); let n = 0;
	setInterval(() => { g.fillStyle = `hsl(${n++ % 360},80%,50%)`; g.fillRect(0, 0, 640, 480);
		g.fillStyle = '#fff'; g.font = '80px sans-serif'; g.fillText(String(n), 200, 260); }, 33);
	const ac = new AudioContext(); const o = ac.createOscillator(); o.frequency.value = 660;
	const dst = ac.createMediaStreamDestination(); o.connect(dst); o.start();
	const pc = new RTCPeerConnection({iceServers: [{urls: 'stun:stun.l.google.com:19302'}]});
	window.pc = pc;
	c.captureStream(30).getTracks().forEach(t => pc.addTrack(t));
	dst.stream.getTracks().forEach(t => pc.addTrack(t));
	pc.ontrack = e => { if (e.track.kind === 'video') document.getElementById('v').srcObject = new MediaStream([e.track]); };
	await pc.setLocalDescription(await pc.createOffer());
	await new Promise(r => { if (pc.iceGatheringState === 'complete') r();
		pc.onicegatheringstatechange = () => pc.iceGatheringState === 'complete' && r(); });
	const res = await fetch('/offer', {method: 'POST', body: JSON.stringify(pc.localDescription)});
	await pc.setRemoteDescription(await res.json());
	log('answer applied');
}
call().catch(e => log('error ' + e));
</script>'''


class Handler(BaseHTTPRequestHandler):
	def log_message(self, *a):
		pass

	def do_GET(self):
		self.send_response(200)
		self.send_header('Content-Type', 'text/html')
		self.end_headers()
		self.wfile.write(PAGE.encode())

	def do_POST(self):
		offer = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
		proc.stdin.write(json.dumps({'command': 'webrtc_answer', 'params': offer, 'token': '1'}) + '\n')
		proc.stdin.flush()
		while True:
			e = events.get(timeout=20)
			if e['type'] == 'ANSWER':
				break
		body = json.dumps({'type': 'answer', 'sdp': e['sdp']}).encode()
		self.send_response(200)
		self.send_header('Content-Type', 'application/json')
		self.end_headers()
		self.wfile.write(body)


srv = ThreadingHTTPServer(('127.0.0.1', 8099), Handler)
threading.Thread(target=srv.serve_forever, daemon=True).start()
time.sleep(seconds)
proc.stdin.close()
proc.wait(timeout=10)
srv.shutdown()
fake.wait()
