#!/usr/bin/env python3
#
# Copyright (c) 2026 Simon Peter
#
# SPDX-License-Identifier: MIT
#
# A fake captive portal on this machine, to test minicaptive without a hotspot. Set on the device
#   probe=http://THIS-PC:8099/generate_204
# The probe gets 204 once the device's address has passed the portal, until /reset.
#
#   portal-sim.py [KIND] [PORT]     KIND: form meta link js fancy wispr (default form)
#   curl http://THIS-PC:8099/reset?kind=js      forget all clients, switch kind
import http.server, json, secrets, sys, urllib.parse

state = {'kind': sys.argv[1] if len(sys.argv) > 1 else 'form', 'ok': set(), 'tokens': {}}
port = int(sys.argv[2]) if len(sys.argv) > 2 else 8099

PAGE = '<!doctype html><html><head><meta name="viewport" content="width=device-width"><title>Hotel WLAN</title>%s</head><body>%s</body></html>'


class H(http.server.BaseHTTPRequestHandler):
	def send(self, code, body='', ctype='text/html; charset=utf-8', headers=()):
		b = body.encode()
		self.send_response(code)
		for k, v in headers:
			self.send_header(k, v)
		if code != 204:
			self.send_header('Content-Type', ctype)
			self.send_header('Content-Length', str(len(b)))
		self.end_headers()
		if code != 204:
			self.wfile.write(b)

	def token(self):
		t = state['tokens'].setdefault(self.client_address[0], secrets.token_hex(8))
		return t

	def grant(self, how):
		state['ok'].add(self.client_address[0])
		print(f'*** {self.client_address[0]} passed the {state["kind"]} portal ({how})', flush=True)

	def body(self):
		n = int(self.headers.get('Content-Length') or 0)
		return self.rfile.read(n).decode()

	def do_GET(self):
		u = urllib.parse.urlparse(self.path)
		q = dict(urllib.parse.parse_qsl(u.query))
		ip, kind, tok = self.client_address[0], state['kind'], self.token()
		if u.path == '/reset':
			state['ok'].clear()
			state['tokens'].clear()
			state['kind'] = q.get('kind', kind)
			return self.send(200, 'reset, kind ' + state['kind'] + '\n', 'text/plain')
		if u.path == '/generate_204':
			if ip in state['ok']:
				return self.send(204)
			return self.send(302, headers=[('Location', f'/portal/{kind}?orig=generate_204&mac=00:00:5e:00:53:01')])
		if u.path == '/portal/form':
			return self.send(200, PAGE % ('', f'''<h1>Welcome</h1><a href="/impressum">Impressum</a>
<form method="post" action="/login"><input type="hidden" name="token" value="{tok}">
<input type="email" name="email" placeholder="E-Mail (optional)">
<label><input type="checkbox" name="agb" value="yes"> Ich akzeptiere die AGB</label>
<label><input type="checkbox" name="news" value="yes"> Newsletter mit Angeboten abonnieren</label>
<button type="submit" name="action" value="go">Jetzt verbinden</button></form>
<form action="/search"><input name="q"><input type="submit" value="Suchen"></form>'''),
				headers=[('Set-Cookie', f'sid={tok}; Path=/')])
		if u.path == '/portal/meta':
			return self.send(200, PAGE % ('<meta http-equiv="refresh" content="0; URL=/portal/frame">', 'Please wait...'))
		if u.path == '/portal/frame':
			return self.send(200, '<html><frameset rows="100%"><frame src="/portal/form"></frameset></html>')
		if u.path == '/portal/link':
			return self.send(200, PAGE % ('', f'''<p>Free WLAN for our guests.</p><a href="/lang?en">English</a>
<a href="/premium">Premium kaufen</a> <a href="/grant?t={tok}">Kostenlos surfen</a>'''))
		if u.path == '/grant' and q.get('t') == tok:
			self.grant('link')
			return self.send(200, PAGE % ('', 'You are online.'))
		if u.path == '/portal/js':
			return self.send(200, PAGE % ('', f'''<div id="app">Loading...</div><script>
var T = "{tok}";
setTimeout(function () {{
  var app = document.getElementById('app');
  app.innerHTML = '<p>Terms of use ...</p><input type="checkbox" id="tos"> <span>I have read the terms</span><br>'
    + '<div class="btn" id="go" style="padding:10px;background:#08c">Accept and connect</div>';
  document.getElementById('go').onclick = function () {{
    if (!document.getElementById('tos').checked) {{ alert('Please accept the terms'); return; }}
    fetch('/api/grant', {{method: 'POST', headers: {{'Content-Type': 'application/json'}},
      body: JSON.stringify({{token: T, tos: true}})}}).then(function () {{ app.innerHTML = 'Connected!'; }});
  }};
}}, 1000);
</script>'''))
		if u.path == '/portal/fancy':
			return self.send(200, PAGE % ('<style>.box input{display:none}.box span{display:inline-block;width:20px;height:20px;border:1px solid #888}'
				'.go{cursor:pointer;background:#0a0;color:#fff;padding:12px}</style>', f'''<div id="app"></div><script>
var T = "{tok}", priv = false;
setTimeout(function () {{
  var app = document.getElementById('app');
  app.innerHTML = '<label class="box"><input type="checkbox" id="tos"><span></span> J\\'accepte les conditions</label><br>'
    + '<div role="checkbox" aria-checked="false" id="priv" tabindex="0">Datenschutzerklaerung gelesen</div><br>'
    + '<label><input type="checkbox" id="mkt"> Send me offers by email</label><br>'
    + '<div class="go" id="go">Akzeptieren und verbinden</div>';
  document.getElementById('priv').addEventListener('click', function () {{ priv = true; this.setAttribute('aria-checked', 'true'); }});
  document.getElementById('go').addEventListener('click', function () {{
    var tos = document.getElementById('tos').checked, mkt = document.getElementById('mkt').checked;
    fetch('/api/grant', {{method: 'POST', headers: {{'Content-Type': 'application/json'}},
      body: JSON.stringify({{token: T, tos: tos && priv && !mkt}})}});
  }});
}}, 1500);
</script>'''))
		if u.path == '/portal/wispr':
			host = self.headers.get('Host')
			return self.send(200, PAGE % ('', f'''<!--
<?xml version="1.0" encoding="UTF-8"?>
<WISPAccessGatewayParam xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:noNamespaceSchemaLocation="http://www.acmewisp.com/WISPAccessGatewayParam.xsd">
<Redirect><AccessProcedure>1.0</AccessProcedure><AccessLocation>sim</AccessLocation><LocationName>Simulated Hotel</LocationName>
<LoginURL>http://{host}/wispr/login?res=login&amp;uamip=10.0.0.1</LoginURL><MessageType>100</MessageType><ResponseCode>0</ResponseCode></Redirect>
</WISPAccessGatewayParam>
-->Please log in with a smart client.'''))
		return self.send(404, PAGE % ('', 'not found'))

	def do_POST(self):
		u = urllib.parse.urlparse(self.path)
		ip, tok = self.client_address[0], self.token()
		data = self.body()
		if u.path == '/login':
			f = dict(urllib.parse.parse_qsl(data, keep_blank_values=True))
			cookie = self.headers.get('Cookie') or ''
			print(f'login {f} cookie={cookie!r}', flush=True)
			if f.get('token') == tok and f.get('agb') == 'yes' and 'news' not in f and f'sid={tok}' in cookie and f.get('action') == 'go':
				self.grant('form')
				return self.send(200, PAGE % ('', 'You are online.'))
			return self.send(200, PAGE % ('', 'Please accept the terms. <a href="/portal/form">Back</a>'))
		if u.path == '/api/grant':
			j = json.loads(data or '{}')
			if j.get('token') == tok and j.get('tos'):
				self.grant('js')
				return self.send(200, '{"ok":true}', 'application/json')
			return self.send(403, '{"ok":false}', 'application/json')
		if u.path == '/wispr/login':
			f = dict(urllib.parse.parse_qsl(data, keep_blank_values=True))
			print(f'wispr {f}', flush=True)
			if 'UserName' in f and 'Password' in f and u.query == 'res=login&uamip=10.0.0.1':
				self.grant('wispr')
				code = 50
			else:
				code = 100
			return self.send(200, f'''<html><!--<?xml version="1.0" encoding="UTF-8"?><WISPAccessGatewayParam>
<AuthenticationReply><MessageType>120</MessageType><ResponseCode>{code}</ResponseCode></AuthenticationReply>
</WISPAccessGatewayParam>--></html>''')
		return self.send(404)

	def log_message(self, fmt, *a):
		xrw = self.headers.get('X-Requested-With') if self.headers else None
		print(self.client_address[0], fmt % a, f'X-Requested-With: {xrw}' if xrw else '', flush=True)


http.server.ThreadingHTTPServer(('0.0.0.0', port), H).serve_forever()
