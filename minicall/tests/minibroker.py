#!/usr/bin/env python3
#
# Copyright (c) 2026 Simon Peter
#
# SPDX-License-Identifier: BSD-2-Clause
#
# A small MQTT 3.1.1 broker for tests in the local network, so that call setup
# (which carries IP addresses) never goes through a public broker. TCP on 1883
# for the device, WebSocket on 8083 for the web page. No authentication, QoS 0
# and 1, retained messages, last will. Not for anything but tests.
#
#   minibroker.py [seconds]
import asyncio, base64, hashlib, struct, sys


class Client:
	def __init__(self, write):
		self.write = write
		self.subs = []
		self.will = None


clients = set()
retained = {}


def match(flt, topic):
	f, t = flt.split('/'), topic.split('/')
	for i, p in enumerate(f):
		if p == '#':
			return True
		if i >= len(t) or (p != '+' and p != t[i]):
			return False
	return len(f) == len(t)


def enc_len(n):
	out = bytearray()
	while True:
		b = n % 128
		n //= 128
		out.append(b | (0x80 if n else 0))
		if not n:
			return bytes(out)


def publish_packet(topic, payload, retain=False):
	t = topic.encode()
	body = struct.pack('>H', len(t)) + t + payload
	return bytes([0x30 | (1 if retain else 0)]) + enc_len(len(body)) + body


async def deliver(topic, payload, retain):
	if retain:
		if payload:
			retained[topic] = payload
		else:
			retained.pop(topic, None)
	for c in list(clients):
		if any(match(f, topic) for f in c.subs):
			await c.write(publish_packet(topic, payload))


async def session(read, write):
	"""read(n) returns exactly n bytes or raises; write(bytes) sends one packet."""
	c = Client(write)
	clients.add(c)
	clean = False
	try:
		while True:
			h = (await read(1))[0]
			n, mul = 0, 1
			while True:
				b = (await read(1))[0]
				n += (b & 0x7f) * mul
				mul *= 128
				if not b & 0x80:
					break
			body = await read(n) if n else b''
			kind = h >> 4
			if kind == 1:  # CONNECT
				p = 2 + struct.unpack('>H', body[:2])[0] + 1
				flags = body[p]
				p += 3
				ln = struct.unpack('>H', body[p:p + 2])[0]
				p += 2 + ln
				if flags & 0x04:
					ln = struct.unpack('>H', body[p:p + 2])[0]
					wt = body[p + 2:p + 2 + ln].decode()
					p += 2 + ln
					ln = struct.unpack('>H', body[p:p + 2])[0]
					c.will = (wt, body[p + 2:p + 2 + ln], bool(flags & 0x20))
				await write(b'\x20\x02\x00\x00')
			elif kind == 3:  # PUBLISH
				qos = (h >> 1) & 3
				ln = struct.unpack('>H', body[:2])[0]
				topic = body[2:2 + ln].decode()
				p = 2 + ln
				if qos:
					pid = body[p:p + 2]
					p += 2
					await write(b'\x40\x02' + pid)
				await deliver(topic, body[p:], bool(h & 1))
			elif kind == 8:  # SUBSCRIBE
				pid, p, codes = body[:2], 2, b''
				while p < len(body):
					ln = struct.unpack('>H', body[p:p + 2])[0]
					flt = body[p + 2:p + 2 + ln].decode()
					p += 2 + ln + 1
					c.subs.append(flt)
					codes += b'\x00'
					for t, pl in retained.items():
						if match(flt, t):
							await write(publish_packet(t, pl, True))
				await write(b'\x90' + enc_len(2 + len(codes)) + pid + codes)
			elif kind == 12:  # PINGREQ
				await write(b'\xd0\x00')
			elif kind == 14:  # DISCONNECT
				clean = True
				return
	except (asyncio.IncompleteReadError, ConnectionError, IndexError):
		pass
	finally:
		clients.discard(c)
		if c.will and not clean:
			await deliver(*c.will)


async def tcp(reader, writer):
	async def write(b):
		writer.write(b)
		await writer.drain()
	await session(reader.readexactly, write)
	writer.close()


async def ws(reader, writer):
	head = await reader.readuntil(b'\r\n\r\n')
	key = [l.split(b':', 1)[1].strip() for l in head.split(b'\r\n') if l.lower().startswith(b'sec-websocket-key')][0]
	acc = base64.b64encode(hashlib.sha1(key + b'258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest())
	writer.write(b'HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n'
		b'Sec-WebSocket-Accept: ' + acc + b'\r\nSec-WebSocket-Protocol: mqtt\r\n\r\n')
	buf = bytearray()

	async def read(n):
		while len(buf) < n:
			b0, b1 = await reader.readexactly(2)
			ln = b1 & 0x7f
			if ln == 126:
				ln = struct.unpack('>H', await reader.readexactly(2))[0]
			elif ln == 127:
				ln = struct.unpack('>Q', await reader.readexactly(8))[0]
			mask = await reader.readexactly(4) if b1 & 0x80 else b'\0\0\0\0'
			data = await reader.readexactly(ln)
			if b0 & 0x0f == 8:
				raise ConnectionError('closed')
			buf.extend(x ^ mask[i % 4] for i, x in enumerate(data))
		out = bytes(buf[:n])
		del buf[:n]
		return out

	async def write(b):
		n = len(b)
		hdr = bytes([0x82, n]) if n < 126 else bytes([0x82, 126]) + struct.pack('>H', n) if n < 65536 \
			else bytes([0x82, 127]) + struct.pack('>Q', n)
		writer.write(hdr + b)
		await writer.drain()
	await session(read, write)
	writer.close()


async def main():
	await asyncio.start_server(tcp, '0.0.0.0', 1883)
	await asyncio.start_server(ws, '0.0.0.0', 8083)
	await asyncio.sleep(float(sys.argv[1]) if len(sys.argv) > 1 else 3600)

asyncio.run(main())
