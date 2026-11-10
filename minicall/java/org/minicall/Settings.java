/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
package org.minicall;

import android.content.Context;
import android.content.pm.PackageManager;
import android.media.MediaRecorder;
import android.os.Build;
import android.util.Log;
import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Locale;

/**
 * minicall.conf: key=value lines, # starts a comment. Copied to /sdcard/minicall.conf, it replaces the app's private
 * copy at the next start and is deleted from the card, because it can hold the SIP password.
 */
final class Settings {
	private static final String TAG = "minicall";
	static final String CARD_FILE = "/sdcard/minicall.conf";

	static final class Contact {
		final String name;
		final String uri;

		Contact(String name, String uri) {
			this.name = name;
			this.uri = uri;
		}
	}

	final String sipUser;
	final String sipPassword;
	final String sipDomain;
	final String sipTransport;
	final String displayName;
	final List<String> stun;
	final List<Contact> contacts;
	final boolean autoAnswerContacts;
	/** NaN: automatic. */
	final double micGainDb;
	final double speakerGainDb;
	final int micSource;
	final boolean callAudio;
	final boolean selfView;
	/** WebRTC's audio processing in the helper (echo, noise, gain); else the app's own gain control and gate. */
	final boolean webrtcAudio;
	final int videoWidth;
	final int videoHeight;
	final int videoFps;
	final int videoBitrate;
	final String mqttHost;
	final int mqttPort;
	final String mqttUser;
	final String mqttPassword;
	final String mqttTopic;
	final boolean mqttTls;
	final String webAnswer;

	private Settings(List<String[]> kv, boolean telephony) throws IOException {
		String user = "", password = "", domain = "", transport = "udp", name = "minicall", answer = "contacts";
		double gain = Double.NaN, speakerGain = Double.NaN;
		String audioMode = "auto", self = "auto", processing = "webrtc";
		int source = -1, width = 0, height = 0, fps = 30, bitrate = 0;
		String mHost = "", mUser = "", mPassword = "", mTopic = "", web = "contacts";
		int mPort = 8883;
		boolean mTls = true;
		List<String> stunList = new ArrayList<>();
		List<Contact> contactList = new ArrayList<>();
		for (String[] e : kv) {
			String k = e[0], v = e[1];
			switch (k) {
				case "sip-user": user = v; break;
				case "sip-password": password = v; break;
				case "sip-domain": domain = v; break;
				case "sip-transport": transport = v.toLowerCase(Locale.ROOT); break;
				case "display-name": name = v; break;
				case "stun": stunList.add(v); break;
				case "auto-answer": answer = v; break;
				case "mic-gain-db": gain = v.equals("auto") ? Double.NaN : Double.parseDouble(v); break;
				case "speaker-gain-db": speakerGain = v.equals("auto") ? Double.NaN : Double.parseDouble(v); break;
				case "mic-source": source = Integer.parseInt(v); break;
				case "audio-mode": audioMode = v; break;
				case "self-view": self = v; break;
				case "audio-processing": processing = v; break;
				case "video-size": {
					if (v.equals("auto")) {
						width = 0;
						height = 0;
						break;
					}
					String[] wh = v.split("x");
					if (wh.length != 2) throw new IOException("video-size: WIDTHxHEIGHT");
					width = Integer.parseInt(wh[0]);
					height = Integer.parseInt(wh[1]);
					break;
				}
				case "video-fps": fps = Integer.parseInt(v); break;
				case "video-bitrate": bitrate = Integer.parseInt(v); break;
				case "mqtt-host": mHost = v; break;
				case "mqtt-port": mPort = Integer.parseInt(v); break;
				case "mqtt-user": mUser = v; break;
				case "mqtt-password": mPassword = v; break;
				case "mqtt-topic": mTopic = v; break;
				case "mqtt-tls": mTls = !v.equals("off"); break;
				case "web-auto-answer": web = v; break;
				case "contact": {
					int eq = v.indexOf('=');
					if (eq <= 0) throw new IOException("contact: NAME=sip:user@host");
					String uri = v.substring(eq + 1).trim();
					if (!uri.startsWith("sip:") && !uri.startsWith("web:") && !uri.startsWith("mqtt:")) {
						throw new IOException("contact " + v + ": address must start with sip:, web: or mqtt:");
					}
					contactList.add(new Contact(v.substring(0, eq).trim(), uri));
					break;
				}
				default: throw new IOException("unknown setting " + k);
			}
		}
		if (!answer.equals("contacts") && !answer.equals("off")) throw new IOException("auto-answer: contacts or off");
		if (!transport.equals("udp") && !transport.equals("tcp") && !transport.equals("tls")) {
			throw new IOException("sip-transport: udp, tcp or tls");
		}
		if (user.isEmpty() != domain.isEmpty()) throw new IOException("sip-user and sip-domain go together");
		if (stunList.isEmpty()) stunList.add("stun.l.google.com:19302");
		if (!self.equals("auto") && !self.equals("on") && !self.equals("off")) throw new IOException("self-view: auto, on or off");
		if (!processing.equals("webrtc") && !processing.equals("simple")) {
			throw new IOException("audio-processing: webrtc or simple");
		}
		webrtcAudio = processing.equals("webrtc");
		if (!audioMode.equals("auto") && !audioMode.equals("call") && !audioMode.equals("media")) {
			throw new IOException("audio-mode: auto, call or media");
		}
		if (!web.equals("contacts") && !web.equals("all") && !web.equals("off")) {
			throw new IOException("web-auto-answer: contacts, all or off");
		}
		if (mHost.isEmpty() != mTopic.isEmpty()) throw new IOException("mqtt-host and mqtt-topic go together");
		if (mTopic.contains("#") || mTopic.contains("+")) throw new IOException("mqtt-topic: no wildcards");
		sipUser = user;
		sipPassword = password;
		sipDomain = domain;
		sipTransport = transport;
		displayName = name;
		stun = Collections.unmodifiableList(stunList);
		contacts = Collections.unmodifiableList(contactList);
		autoAnswerContacts = answer.equals("contacts");
		// Phones cancel their echo in call mode (voice call source and stream, speakerphone); the Echo Show's call
		// path is silent, it has its own echo-cancelled source 6, a quiet one.
		callAudio = audioMode.equals("call") || (audioMode.equals("auto") && telephony);
		micSource = source >= 0 ? source : callAudio ? MediaRecorder.AudioSource.VOICE_COMMUNICATION : 6;
		micGainDb = gain;
		speakerGainDb = speakerGain;
		// The Echo Show (an Amazon device) shows only the far end; elsewhere the own picture is in a corner.
		selfView = self.equals("on") || (self.equals("auto") && !"Amazon".equalsIgnoreCase(Build.MANUFACTURER));
		videoWidth = width;
		videoHeight = height;
		videoFps = fps;
		videoBitrate = bitrate;
		mqttHost = mHost;
		mqttPort = mPort;
		mqttUser = mUser;
		mqttPassword = mPassword;
		mqttTopic = mTopic;
		mqttTls = mTls;
		webAnswer = web;
	}

	boolean hasMqtt() {
		return !mqttHost.isEmpty();
	}

	/** The device contact whose MQTT topic this is. */
	Contact deviceContact(String topic) {
		for (Contact c : contacts) {
			if (c.uri.startsWith("mqtt:") && c.uri.substring(5).equals(topic)) return c;
		}
		return null;
	}

	/** The web contact a page of this name stands for. */
	Contact webContact(String pageName) {
		for (Contact c : contacts) {
			if (c.uri.startsWith("web:") && c.uri.substring(4).equalsIgnoreCase(pageName.trim())) return c;
		}
		return null;
	}

	boolean hasAccount() {
		return !sipUser.isEmpty();
	}

	/** The contact whose address is the caller's, comparing user and host only (no port, parameters or display name). */
	Contact contactFor(String peerUri) {
		String peer = userHost(peerUri);
		for (Contact c : contacts) {
			if (c.uri.startsWith("sip:") && userHost(c.uri).equals(peer)) return c;
		}
		return null;
	}

	Contact contactNamed(String name) {
		for (Contact c : contacts) {
			if (c.name.equalsIgnoreCase(name.trim())) return c;
		}
		return null;
	}

	static String userHost(String uri) {
		String u = uri.trim();
		int lt = u.indexOf('<');
		if (lt >= 0) u = u.substring(lt + 1);
		if (u.startsWith("sip:")) u = u.substring(4);
		else if (u.startsWith("sips:")) u = u.substring(5);
		int cut = u.length();
		for (char c : new char[] {'>', ';', '?'}) {
			int i = u.indexOf(c);
			if (i >= 0 && i < cut) cut = i;
		}
		u = u.substring(0, cut);
		// Drop a port, but not inside an IPv6 literal.
		int colon = u.lastIndexOf(':');
		if (colon > u.lastIndexOf(']') && colon > u.indexOf('@')) u = u.substring(0, colon);
		return u.toLowerCase(Locale.ROOT);
	}

	private static boolean telephony(Context ctx) {
		return ctx.getPackageManager().hasSystemFeature(PackageManager.FEATURE_TELEPHONY);
	}

	static Settings load(Context ctx) throws IOException {
		File priv = new File(ctx.getFilesDir(), "minicall.conf");
		File card = new File(CARD_FILE);
		if (card.isFile()) {
			byte[] data = readAll(card);
			// Parse first: a broken file must not replace a working one.
			parse(data, telephony(ctx));
			try (OutputStream out = new FileOutputStream(priv)) {
				out.write(data);
			}
			if (!card.delete()) Log.w(TAG, "could not delete " + card);
			Log.i(TAG, "settings taken from " + card);
		}
		if (!priv.isFile()) throw new IOException("no settings: copy minicall.conf to " + CARD_FILE);
		return parse(readAll(priv), telephony(ctx));
	}

	private static Settings parse(byte[] data, boolean telephony) throws IOException {
		List<String[]> kv = new ArrayList<>();
		int n = 0;
		for (String line : new String(data, StandardCharsets.UTF_8).split("\n")) {
			n++;
			// A comment after a value needs a space before its #, so that a password may contain one.
			String l = line.split("\\s+#", 2)[0].trim();
			if (l.isEmpty() || l.startsWith("#")) continue;
			int eq = l.indexOf('=');
			if (eq <= 0) throw new IOException("line " + n + ": key=value expected");
			kv.add(new String[] {l.substring(0, eq).trim(), l.substring(eq + 1).trim()});
		}
		try {
			return new Settings(kv, telephony);
		} catch (NumberFormatException e) {
			throw new IOException("bad number: " + e.getMessage());
		}
	}

	// java.nio.file is missing on Android 7.
	static byte[] readAll(File f) throws IOException {
		try (InputStream in = new FileInputStream(f)) {
			ByteArrayOutputStream out = new ByteArrayOutputStream();
			byte[] buf = new byte[8192];
			for (int r; (r = in.read(buf)) > 0; ) {
				out.write(buf, 0, r);
			}
			return out.toByteArray();
		}
	}
}
