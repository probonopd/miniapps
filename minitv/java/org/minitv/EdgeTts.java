package org.minitv;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.net.Socket;
import java.net.SocketTimeoutException;
import java.nio.ByteBuffer;
import java.nio.ByteOrder;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.security.SecureRandom;
import java.text.SimpleDateFormat;
import java.util.ArrayList;
import java.util.Base64;
import java.util.Date;
import java.util.List;
import java.util.Locale;
import java.util.TimeZone;
import java.util.UUID;
import javax.net.ssl.SSLParameters;
import javax.net.ssl.SSLSocket;
import javax.net.ssl.SSLSocketFactory;

/**
 * Text to speech with the "read aloud" voices of the Microsoft Edge browser, ported from the python package edge-tts
 * (constants.py, drm.py, communicate.py). The interface is unofficial and undocumented: it may change, stop working
 * or block clients at any time. Plain Java without Android classes, so that it can be tried on a PC.
 */
final class EdgeTts {
	// A German-only voice: the multilingual ones guess the language of each text and read short German texts and spelled
	// names in French or English, and this endpoint rejects the SSML (lang, say-as, phoneme) that would tell them.
	static final String DEFAULT_VOICE = "de-DE-KatjaNeural";
	static final String VOICE_PATTERN = "^[a-z]{2}-[A-Z]{2}-[A-Za-z]+Neural$";
	static final String RATE_PATTERN = "^[+-]\\d{1,2}%$";
	// Longer texts are rendered in parts: the service rejects big requests, and a part is a sentence or more anyway.
	static final int PART_MAX = 2000;
	static final int CONNECT_TIMEOUT_MS = 10000, IDLE_TIMEOUT_MS = 20000;
	// The service sends audio only about 1.4 times faster than it plays (measured: 39 s of speech for 500 characters
	// took 29 s), so a fixed limit for the whole answer would cut off every longer text.
	static final int TOTAL_MS_PER_CHAR = 100;

	private static final String HOST = "speech.platform.bing.com";
	private static final String PATH = "/consumer/speech/synthesize/readaloud/edge/v1";
	private static final String TRUSTED_CLIENT_TOKEN = "6A5AA1D4EAFF4E9FB37E23D68491D6F4";
	private static final String CHROMIUM_FULL_VERSION = "143.0.3650.75";
	private static final String CHROMIUM_MAJOR_VERSION = "143";
	private static final String USER_AGENT = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"
			+ " (KHTML, like Gecko) Chrome/" + CHROMIUM_MAJOR_VERSION + ".0.0.0 Safari/537.36 Edg/" + CHROMIUM_MAJOR_VERSION + ".0.0.0";
	private static final long WIN_EPOCH_S = 11644473600L;
	private static final SecureRandom RANDOM = new SecureRandom();
	// The service rejects a token from a clock that is off; edge-tts then corrects by the server's Date header.
	private static volatile long clockSkewMs;

	/** Thrown when the service answered, but not with audio (a wrong voice, a rejected token, ...). */
	static final class RejectedException extends IOException {
		private static final long serialVersionUID = 1L;

		RejectedException(String message) {
			super(message);
		}
	}

	private EdgeTts() {
	}

	/** The text as MP3 (24 kHz mono, 48 kbit/s); long texts are rendered in parts that are joined. */
	static byte[] render(String text, String voice, String rate) throws IOException {
		if (!voice.matches(VOICE_PATTERN)) throw new IllegalArgumentException("voice " + voice);
		if (!rate.matches(RATE_PATTERN)) throw new IllegalArgumentException("rate " + rate);
		ByteArrayOutputStream audio = new ByteArrayOutputStream();
		for (String part : split(clean(text), PART_MAX)) {
			byte[] ssml = ssml(voice, rate, escape(part));
			try {
				audio.write(renderPart(ssml));
			} catch (SkewException e) {
				clockSkewMs += e.skewMs;
				audio.write(renderPart(ssml));
			}
		}
		if (audio.size() == 0) throw new RejectedException("no audio received");
		return audio.toByteArray();
	}

	/** The service fails on some control characters (vertical tab in OCR texts, for one). */
	static String clean(String text) {
		StringBuilder b = new StringBuilder(text.length());
		for (int i = 0; i < text.length(); i++) {
			char c = text.charAt(i);
			b.append(c <= 8 || c == 11 || c == 12 || (c >= 14 && c <= 31) ? ' ' : c);
		}
		return b.toString();
	}

	static String escape(String text) {
		return text.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;");
	}

	/** Parts of at most max characters, cut after a sentence if possible, else between words. */
	static List<String> split(String text, int max) {
		List<String> parts = new ArrayList<>();
		String rest = text.trim();
		while (rest.length() > max) {
			int cut = -1;
			for (int i = max - 1; i > 0 && cut < 0; i--) {
				char c = rest.charAt(i);
				if ((c == '.' || c == '!' || c == '?' || c == ';' || c == ':' || c == '\n') && Character.isWhitespace(rest.charAt(i + 1))) cut = i + 1;
			}
			if (cut < max / 2) {
				int space = rest.lastIndexOf(' ', max);
				if (space > max / 2 || cut < 0) cut = space;
			}
			if (cut <= 0) cut = Character.isHighSurrogate(rest.charAt(max - 1)) ? max - 1 : max;
			String part = rest.substring(0, cut).trim();
			if (!part.isEmpty()) parts.add(part);
			rest = rest.substring(cut).trim();
		}
		if (!rest.isEmpty()) parts.add(rest);
		return parts;
	}

	private static byte[] ssml(String voice, String rate, String escapedText) {
		// "de-DE-SeraphinaMultilingualNeural" -> "Microsoft Server Speech Text to Speech Voice (de-DE, SeraphinaMultilingualNeural)"
		String locale = voice.substring(0, 5), name = voice.substring(6);
		String ssml = "<speak version='1.0' xmlns='http://www.w3.org/2001/10/synthesis' xml:lang='" + locale + "'>"
				+ "<voice name='Microsoft Server Speech Text to Speech Voice (" + locale + ", " + name + ")'>"
				+ "<prosody pitch='+0Hz' rate='" + rate + "' volume='+0%'>" + escapedText + "</prosody></voice></speak>";
		String message = "X-RequestId:" + connectId() + "\r\nContent-Type:application/ssml+xml\r\n"
				// The "Z" after a date that already names its zone is what Edge sends.
				+ "X-Timestamp:" + jsDate() + "Z\r\nPath:ssml\r\n\r\n" + ssml;
		return message.getBytes(StandardCharsets.UTF_8);
	}

	private static final class SkewException extends IOException {
		private static final long serialVersionUID = 1L;
		final long skewMs;

		SkewException(long skewMs) {
			super("clock skew " + skewMs + " ms");
			this.skewMs = skewMs;
		}
	}

	private static byte[] renderPart(byte[] ssml) throws IOException {
		long deadline = System.currentTimeMillis() + IDLE_TIMEOUT_MS + (long) TOTAL_MS_PER_CHAR * ssml.length;
		Socket raw = new Socket();
		try {
			raw.connect(new InetSocketAddress(HOST, 443), CONNECT_TIMEOUT_MS);
			raw.setSoTimeout(CONNECT_TIMEOUT_MS);
			SSLSocket tls = (SSLSocket) ((SSLSocketFactory) SSLSocketFactory.getDefault()).createSocket(raw, HOST, 443, true);
			// SSLSocket checks the certificate chain but not that it names the host; this asks for that too.
			SSLParameters params = tls.getSSLParameters();
			params.setEndpointIdentificationAlgorithm("HTTPS");
			tls.setSSLParameters(params);
			tls.startHandshake();
			WebSocket ws = new WebSocket(tls, deadline);
			ws.open();
			ws.sendText(("X-Timestamp:" + jsDate() + "\r\nContent-Type:application/json; charset=utf-8\r\n"
					+ "Path:speech.config\r\n\r\n"
					+ "{\"context\":{\"synthesis\":{\"audio\":{\"metadataoptions\":{"
					+ "\"sentenceBoundaryEnabled\":\"true\",\"wordBoundaryEnabled\":\"false\"},"
					+ "\"outputFormat\":\"audio-24khz-48kbitrate-mono-mp3\"}}}}\r\n").getBytes(StandardCharsets.UTF_8));
			ws.sendText(ssml);
			return ws.receiveAudio();
		} finally {
			raw.close();
		}
	}

	/** A minimal RFC 6455 client: one connection, text frames out, text and binary frames in, no extensions. */
	private static final class WebSocket {
		private final Socket socket;
		private final InputStream in;
		private final OutputStream out;
		private final long deadline;

		WebSocket(Socket socket, long deadline) throws IOException {
			this.socket = socket;
			this.in = socket.getInputStream();
			this.out = socket.getOutputStream();
			this.deadline = deadline;
		}

		void open() throws IOException {
			byte[] nonce = new byte[16];
			RANDOM.nextBytes(nonce);
			String key = Base64.getEncoder().encodeToString(nonce);
			String request = "GET " + PATH + "?TrustedClientToken=" + TRUSTED_CLIENT_TOKEN + "&ConnectionId=" + connectId()
					+ "&Sec-MS-GEC=" + secMsGec(System.currentTimeMillis() + clockSkewMs)
					+ "&Sec-MS-GEC-Version=2-" + CHROMIUM_FULL_VERSION + " HTTP/1.1\r\n"
					+ "Host: " + HOST + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
					+ "Sec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\n"
					+ "Pragma: no-cache\r\nCache-Control: no-cache\r\n"
					+ "Origin: chrome-extension://jdiccldimpdaibmpdkjnbmckianbfold\r\n"
					+ "User-Agent: " + USER_AGENT + "\r\nAccept-Encoding: gzip, deflate, br, zstd\r\n"
					+ "Accept-Language: en-US,en;q=0.9\r\nCookie: muid=" + muid() + ";\r\n\r\n";
			out.write(request.getBytes(StandardCharsets.US_ASCII));
			out.flush();
			String[] lines = readHead().split("\r\n");
			String status = lines[0];
			String accept = null, date = null;
			for (String line : lines) {
				int colon = line.indexOf(':');
				if (colon < 0) continue;
				String name = line.substring(0, colon).trim().toLowerCase(Locale.ROOT), value = line.substring(colon + 1).trim();
				if (name.equals("sec-websocket-accept")) accept = value;
				if (name.equals("date")) date = value;
			}
			if (status.contains(" 403") && date != null) {
				try {
					SimpleDateFormat f = new SimpleDateFormat("EEE, dd MMM yyyy HH:mm:ss zzz", Locale.US);
					throw new SkewException(f.parse(date).getTime() - (System.currentTimeMillis() + clockSkewMs));
				} catch (java.text.ParseException e) {
					throw new RejectedException("handshake: " + status + ", unreadable date " + date);
				}
			}
			if (!status.contains(" 101")) throw new RejectedException("handshake: " + status);
			if (!expectedAccept(key).equals(accept)) throw new RejectedException("handshake: wrong Sec-WebSocket-Accept");
		}

		private String readHead() throws IOException {
			ByteArrayOutputStream head = new ByteArrayOutputStream();
			while (head.size() < 16384) {
				head.write(readByte());
				byte[] b = head.toByteArray();
				int n = b.length;
				if (n >= 4 && b[n - 4] == '\r' && b[n - 3] == '\n' && b[n - 2] == '\r' && b[n - 1] == '\n') {
					return new String(b, 0, n - 4, StandardCharsets.ISO_8859_1);
				}
			}
			throw new RejectedException("handshake: header too long");
		}

		void sendText(byte[] payload) throws IOException {
			sendFrame(1, payload);
		}

		private void sendFrame(int opcode, byte[] payload) throws IOException {
			ByteArrayOutputStream frame = new ByteArrayOutputStream(payload.length + 14);
			frame.write(0x80 | opcode);
			// Client frames are always masked (RFC 6455 5.3).
			if (payload.length < 126) {
				frame.write(0x80 | payload.length);
			} else if (payload.length < 65536) {
				frame.write(0x80 | 126);
				frame.write(payload.length >>> 8);
				frame.write(payload.length & 0xff);
			} else {
				frame.write(0x80 | 127);
				frame.write(ByteBuffer.allocate(8).putLong(payload.length).array(), 0, 8);
			}
			byte[] mask = new byte[4];
			RANDOM.nextBytes(mask);
			frame.write(mask, 0, 4);
			for (int i = 0; i < payload.length; i++) frame.write(payload[i] ^ mask[i & 3]);
			out.write(frame.toByteArray());
			out.flush();
		}

		/** Reads messages until turn.end and returns the audio of all "Path:audio" frames. */
		byte[] receiveAudio() throws IOException {
			ByteArrayOutputStream audio = new ByteArrayOutputStream();
			ByteArrayOutputStream message = new ByteArrayOutputStream();
			int messageOpcode = 0;
			while (true) {
				int b0 = readByte(), b1 = readByte();
				int opcode = b0 & 0x0f;
				long length = b1 & 0x7f;
				if (length == 126) length = (readByte() << 8) | readByte();
				else if (length == 127) length = ByteBuffer.wrap(readBytes(8)).getLong();
				if (length < 0 || length > 4 * 1024 * 1024) throw new RejectedException("frame too large");
				byte[] mask = (b1 & 0x80) != 0 ? readBytes(4) : null;
				byte[] payload = readBytes((int) length);
				if (mask != null) for (int i = 0; i < payload.length; i++) payload[i] ^= mask[i & 3];
				if (opcode == 8) throw new RejectedException("closed by the service" + closeReason(payload));
				if (opcode == 9) {
					sendFrame(10, payload);
					continue;
				}
				if (opcode == 10) continue;
				if (opcode != 0) {
					messageOpcode = opcode;
					message.reset();
				}
				message.write(payload, 0, payload.length);
				if ((b0 & 0x80) == 0) continue;
				byte[] data = message.toByteArray();
				if (messageOpcode == 1) {
					String text = new String(data, StandardCharsets.UTF_8);
					int end = text.indexOf("\r\n\r\n");
					String path = header(end < 0 ? text : text.substring(0, end), "Path");
					if ("turn.end".equals(path)) return audio.toByteArray();
					if (!"turn.start".equals(path) && !"response".equals(path) && !"audio.metadata".equals(path)) {
						throw new RejectedException("unknown message " + path);
					}
				} else if (messageOpcode == 2) {
					if (data.length < 2) throw new RejectedException("binary message without header length");
					int headerLength = ((data[0] & 0xff) << 8) | (data[1] & 0xff);
					if (headerLength + 2 > data.length) throw new RejectedException("binary header length beyond the data");
					String headers = new String(data, 2, headerLength, StandardCharsets.UTF_8);
					if (!"audio".equals(header(headers, "Path"))) throw new RejectedException("binary message that is not audio");
					String type = header(headers, "Content-Type");
					int audioLength = data.length - 2 - headerLength;
					if (type == null && audioLength == 0) continue;
					if (!"audio/mpeg".equals(type) || audioLength == 0) throw new RejectedException("unexpected audio message " + type);
					audio.write(data, 2 + headerLength, audioLength);
				}
			}
		}

		private int readByte() throws IOException {
			socket.setSoTimeout(remaining());
			int b = in.read();
			if (b < 0) throw new IOException("connection closed by the service");
			return b;
		}

		private byte[] readBytes(int n) throws IOException {
			byte[] b = new byte[n];
			for (int off = 0; off < n;) {
				socket.setSoTimeout(remaining());
				int got = in.read(b, off, n - off);
				if (got < 0) throw new IOException("connection closed by the service");
				off += got;
			}
			return b;
		}

		private int remaining() throws SocketTimeoutException {
			long left = deadline - System.currentTimeMillis();
			if (left <= 0) throw new SocketTimeoutException("the answer took too long");
			return (int) Math.min(left, IDLE_TIMEOUT_MS);
		}
	}

	private static String closeReason(byte[] payload) {
		if (payload.length < 2) return "";
		return " (" + (((payload[0] & 0xff) << 8) | (payload[1] & 0xff)) + " "
				+ new String(payload, 2, payload.length - 2, StandardCharsets.UTF_8) + ")";
	}

	private static String header(String headers, String name) {
		for (String line : headers.split("\r\n")) {
			int colon = line.indexOf(':');
			if (colon > 0 && line.substring(0, colon).equals(name)) return line.substring(colon + 1);
		}
		return null;
	}

	private static String expectedAccept(String key) {
		try {
			byte[] sha1 = MessageDigest.getInstance("SHA-1")
					.digest((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").getBytes(StandardCharsets.US_ASCII));
			return Base64.getEncoder().encodeToString(sha1);
		} catch (NoSuchAlgorithmException e) {
			throw new IllegalStateException(e);
		}
	}

	private static String connectId() {
		return UUID.randomUUID().toString().replace("-", "");
	}

	private static String muid() {
		byte[] b = new byte[16];
		RANDOM.nextBytes(b);
		return hex(b);
	}

	/** JavaScript's Date.toString() in UTC, which is what Edge puts in X-Timestamp. */
	private static String jsDate() {
		SimpleDateFormat f = new SimpleDateFormat("EEE MMM dd yyyy HH:mm:ss 'GMT+0000 (Coordinated Universal Time)'", Locale.US);
		f.setTimeZone(TimeZone.getTimeZone("UTC"));
		return f.format(new Date());
	}

	static String hex(byte[] bytes) {
		StringBuilder b = new StringBuilder(bytes.length * 2);
		for (byte x : bytes) b.append(Character.forDigit((x >> 4) & 0xf, 16)).append(Character.forDigit(x & 0xf, 16));
		return b.toString().toUpperCase(Locale.ROOT);
	}

	// ---- Sec-MS-GEC, version 2 (drm.py generate_sec_ms_gec_v2) ----
	// The token hashes "<windows file time ticks, rounded down to 5 minutes><trusted client token>" with a SHA-256 whose
	// first block starts with 57 bytes that Edge keeps to itself: the constants below are that block's state after round
	// 14 and the parts of the message schedule that come from the hidden bytes. A second digest (CS64) is keyed by the
	// first one. All arithmetic is modulo 2^32, which Java's int gives for free.

	private static final int[] SHA256_INITIAL = { 0x6A09E667, 0xBB67AE85, 0x3C6EF372, 0xA54FF53A, 0x510E527F, 0x9B05688C,
			0x1F83D9AB, 0x5BE0CD19 };
	private static final int[] SHA256_STATE = { 0xFD9FBEA2, 0x91243CE7, 0xBE499094, 0xCD2E1E0A, 0x6C006F49, 0x04BD35AB,
			0xFEB0A7E4, 0x03EFFBD2 };
	private static final int[] SHA256_SCHEDULE = { 0xEB24BF9E, 0x1558691F, 0xA0C09EE2, 0x0CFA32BA, 0xA622EE8C, 0xD1DD9491,
			0xC0DD25A0, 0xF9E70AB6, 0xF0C31573, 0x7058CE4E, 0x7188E606, 0x00E5AB33, 0xD258B060, 0x72766564 };
	private static final int[] K = { 0x428A2F98, 0x71374491, 0xB5C0FBCF, 0xE9B5DBA5, 0x3956C25B, 0x59F111F1, 0x923F82A4,
			0xAB1C5ED5, 0xD807AA98, 0x12835B01, 0x243185BE, 0x550C7DC3, 0x72BE5D74, 0x80DEB1FE, 0x9BDC06A7, 0xC19BF174,
			0xE49B69C1, 0xEFBE4786, 0x0FC19DC6, 0x240CA1CC, 0x2DE92C6F, 0x4A7484AA, 0x5CB0A9DC, 0x76F988DA, 0x983E5152,
			0xA831C66D, 0xB00327C8, 0xBF597FC7, 0xC6E00BF3, 0xD5A79147, 0x06CA6351, 0x14292967, 0x27B70A85, 0x2E1B2138,
			0x4D2C6DFC, 0x53380D13, 0x650A7354, 0x766A0ABB, 0x81C2C92E, 0x92722C85, 0xA2BFE8A1, 0xA81A664B, 0xC24B8B70,
			0xC76C51A3, 0xD192E819, 0xD6990624, 0xF40E3585, 0x106AA070, 0x19A4C116, 0x1E376C08, 0x2748774C, 0x34B0BCB5,
			0x391C0CB3, 0x4ED8AA4A, 0x5B9CCA4F, 0x682E6FF3, 0x748F82EE, 0x78A5636F, 0x84C87814, 0x8CC70208, 0x90BEFFFA,
			0xA4506CEB, 0xBEF9A3F7, 0xC67178F2 };
	private static final int SHA256_ADJUSTMENT = 0xC9DC06A7;
	private static final int SIGMA0_ADJUSTMENT = 0x059C0B80, SIGMA1_ADJUSTMENT = 0x000B92C0;
	private static final int[][] CS64_COEFFICIENTS = { { 0xE82E91B1, 0xB32700D0 }, { 0x44DE24C6, 0x88DFF51D },
			{ 0x3BEB65BC, 0x29514038 }, { 0xC89C5933, 0xB328CBDF }, { 0x8D1EC211, 0xD405B4D5 }, { 0x314EF5D5, 0x26E55F3C },
			{ 0xE92491B1, 0x607801C6 }, { 0xF0A5B9D5, 0x82B2C1FB }, { 0x06064CF3, 0xFA51A3DA }, { 0xDA79FA60, 0x207CC464 },
			{ 0xBED20560, 0x3C3C74F3 }, { 0x89F9C9F8, 0xD2D14B61 }, { 0x35FA38C6, 0xBB04A2C1 }, { 0xCB49E3C6, 0xC5626167 } };
	private static final int CS64_FACTOR = 0x82893933, CS64_ADJUSTMENT0 = 0xF2F90B1A, CS64_ADJUSTMENT1 = 0x0B1A0000;

	/** The Sec-MS-GEC value for a Unix time in milliseconds: "<16 hex>-<64 hex>". */
	static String secMsGec(long unixMs) {
		long seconds = Math.floorDiv(unixMs, 1000L) + WIN_EPOCH_S;
		seconds -= seconds % 300;
		byte[] data = (Long.toString(seconds * 10000000L) + TRUSTED_CLIENT_TOKEN).getBytes(StandardCharsets.US_ASCII);
		byte[] digest = sha256(data);
		ByteBuffer d = ByteBuffer.wrap(digest).order(ByteOrder.LITTLE_ENDIAN);
		int[] cs64 = cs64(data, d.getInt(0), d.getInt(4));
		byte[] cs = ByteBuffer.allocate(8).order(ByteOrder.LITTLE_ENDIAN).putInt(cs64[0]).putInt(cs64[1]).array();
		return hex(cs) + "-" + hex(digest);
	}

	private static int sigma0(int x) {
		return Integer.rotateRight(x, 7) ^ Integer.rotateRight(x, 18) ^ (x >>> 3);
	}

	private static int sigma1(int x) {
		return Integer.rotateRight(x, 17) ^ Integer.rotateRight(x, 19) ^ (x >>> 10);
	}

	private static void expand(int[] w, int first) {
		for (int i = first; i < 64; i++) w[i] = sigma1(w[i - 2]) + w[i - 7] + sigma0(w[i - 15]) + w[i - 16];
	}

	/** SHA-256 rounds from round "first" on, added to the state. */
	private static int[] rounds(int[] state, int[] w, int first, int[] k) {
		int a = state[0], b = state[1], c = state[2], d = state[3], e = state[4], f = state[5], g = state[6], h = state[7];
		for (int i = first; i < 64; i++) {
			int t1 = h + (Integer.rotateRight(e, 6) ^ Integer.rotateRight(e, 11) ^ Integer.rotateRight(e, 25))
					+ (g ^ (e & (f ^ g))) + k[i] + w[i];
			int t2 = (Integer.rotateRight(a, 2) ^ Integer.rotateRight(a, 13) ^ Integer.rotateRight(a, 22))
					+ ((a & b) | (c & (a | b)));
			h = g;
			g = f;
			f = e;
			e = d + t1;
			d = c;
			c = b;
			b = a;
			a = t1 + t2;
		}
		return new int[] { a, b, c, d, e, f, g, h };
	}

	private static int[] add(int[] x, int[] y) {
		int[] r = new int[8];
		for (int i = 0; i < 8; i++) r[i] = x[i] + y[i];
		return r;
	}

	/** The state after the first block: the hidden 57 bytes and the first 7 bytes of data. */
	private static int[] sha256Init(byte[] data) {
		int[] w = new int[64];
		w[14] = ((data[0] & 0xff) << 16) | ((data[1] & 0xff) << 8) | (data[2] & 0xff);
		w[15] = ByteBuffer.wrap(data, 3, 4).getInt();
		for (int i = 16; i < 30; i++) {
			int v = SHA256_SCHEDULE[i - 16];
			if (i - 2 >= 14) v += i == 16 ? sigma1(w[i - 2]) ^ SIGMA1_ADJUSTMENT : sigma1(w[i - 2]);
			if (i - 7 >= 14) v += w[i - 7];
			if (i - 15 >= 14) v += i == 29 ? sigma0(w[i - 15]) ^ SIGMA0_ADJUSTMENT : sigma0(w[i - 15]);
			w[i] = v;
		}
		w[30] = sigma1(w[28]) + w[23] + sigma0(w[15]) + w[14] + SHA256_ADJUSTMENT - K[14];
		expand(w, 31);
		int[] k = K.clone();
		k[14] = SHA256_ADJUSTMENT;
		return add(SHA256_INITIAL, rounds(SHA256_STATE, w, 14, k));
	}

	private static byte[] sha256(byte[] data) {
		if (data.length < 7) throw new IllegalArgumentException("token data shorter than 7 bytes");
		long length = 57L + data.length;
		int zeros = (int) Math.floorMod(55 - length, 64L);
		ByteBuffer padded = ByteBuffer.allocate(data.length - 7 + 1 + zeros + 8);
		padded.put(data, 7, data.length - 7).put((byte) 0x80).put(new byte[zeros]).putLong(length * 8);
		int[] state = sha256Init(data);
		padded.flip();
		while (padded.remaining() >= 64) {
			int[] w = new int[64];
			for (int i = 0; i < 16; i++) w[i] = padded.getInt();
			expand(w, 16);
			state = add(state, rounds(state, w, 0, K));
		}
		ByteBuffer out = ByteBuffer.allocate(32);
		for (int x : state) out.putInt(x);
		return out.array();
	}

	private static int rol16(int x) {
		return Integer.rotateLeft(x, 16);
	}

	private static int[] cs64(byte[] data, int keyA, int keyB) {
		keyA |= 1;
		keyB |= 1;
		int[] factors = { keyA * CS64_FACTOR, keyB * CS64_FACTOR };
		// The data with a zero byte in front, as little-endian words, padded to an even number of words.
		int words = (data.length + 1 + 3) / 4;
		if (words % 2 != 0) words++;
		ByteBuffer bytes = ByteBuffer.allocate(words * 4).order(ByteOrder.LITTLE_ENDIAN);
		bytes.put((byte) 0).put(data);
		int n = CS64_COEFFICIENTS.length + words;
		int[] terms = new int[n], offsets = new int[n];
		for (int i = 0; i < CS64_COEFFICIENTS.length; i++) {
			terms[i] = CS64_COEFFICIENTS[i][0] * factors[i % 2];
			offsets[i] = CS64_COEFFICIENTS[i][1];
		}
		for (int i = 0; i < words; i++) {
			int word = bytes.getInt(i * 4);
			int term = word * (i % 2 == 0 ? keyA : keyB);
			int offset = rol16(word) * (i % 2 == 0 ? 0xEF0569FB : 0xC31713DB);
			if (i == 0) {
				term += CS64_ADJUSTMENT0 * factors[0];
				offset += CS64_ADJUSTMENT1;
			}
			terms[CS64_COEFFICIENTS.length + i] = term;
			offsets[CS64_COEFFICIENTS.length + i] = offset;
		}
		int swapAcc = 0, swapLast = 0, state = 0, acc = 0;
		for (int i = 0; i < n; i += 2) {
			int x = terms[i], y = terms[i + 1];
			int t = rol16(offsets[i] + x);
			int q = t * 0x76F50F34;
			swapAcc += rol16(q) * 0xC3EFEA97 + q;
			t = rol16(offsets[i + 1] + y);
			q = t * 0x1881CE3C;
			swapLast = rol16(q) * 0x35BD1EC9 + q;
			swapAcc += swapLast;
			state = state * keyA + x;
			state = rol16(state) * 0xCF98B111;
			state = rol16(state) * 0x87085B9F;
			state = rol16(state) * 0x12CEB96D;
			state = rol16(state) * 0x257E1D83;
			acc += state;
			t = state * keyB + y;
			t = rol16(t) * 0xA27416F5;
			t = rol16(t) * 0xD38396FF;
			state = rol16(rol16(t) * 0x7C932B89) * 0xBFA49F69;
			acc += state;
		}
		return new int[] { swapLast ^ state, swapAcc ^ acc };
	}
}
