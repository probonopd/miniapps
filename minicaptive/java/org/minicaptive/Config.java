/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */
package org.minicaptive;

import android.content.Context;
import java.io.File;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;

/**
 * minicaptive.conf in the app's files: key=value lines, # starts a comment. Edited on the status screen, or copied
 * to /sdcard/minicaptive.conf (adb push), which replaces it at the next round and is deleted from the card, because
 * it can hold a password. Read anew for every round, so a change needs no restart.
 */
final class Config {
	static final String FILE = "minicaptive.conf";
	static final File CARD_FILE = new File("/sdcard/minicaptive.conf");
	static final String DEFAULT_TEXT = "# minicaptive - keeps a public WLAN online without anyone touching the device\n"
			+ "# ssid=Hotel Guest        WLAN to stay on (empty: whatever the device is connected to)\n"
			+ "# bssid=                  access point to prefer (empty: any)\n"
			+ "# interval=60             seconds between checks while online\n"
			+ "# probe=http://connectivitycheck.gstatic.com/generate_204   (several allowed)\n"
			+ "# user= / password=       for WISPr and login forms that need them\n"
			+ "# name= / email= / room=  for portals that ask (left empty when not set)\n"
			+ "# webview=on              second stage for portals that need JavaScript\n"
			+ "# mac-rotate=off          new random MAC when a portal cannot be passed (root helper, Echo Show)\n"
			+ "# skip=*Homespot*         WLANs never to try (several allowed; default *Homespot* and *_FON*)\n"
			+ "# rule=*.example.com click Kostenlos surfen\n"
			+ "# rule=*.example.com fill room=101\n";

	/** A per-portal rule: host glob, then click TEXT or fill NAME=VALUE. */
	static final class Rule {
		final String hostGlob;
		final String action;
		final String arg;

		Rule(String hostGlob, String action, String arg) {
			this.hostGlob = hostGlob;
			this.action = action;
			this.arg = arg;
		}

		boolean matches(String host) {
			String re = "\\Q" + hostGlob.toLowerCase(Locale.ROOT).replace("*", "\\E.*\\Q") + "\\E";
			return host != null && host.toLowerCase(Locale.ROOT).matches(re);
		}
	}

	final String ssid;

	static String ssidOf(String text) {
		return new Config(text).ssid;
	}
	final String bssid;
	final int interval;
	final List<String> probes;
	final String user;
	final String password;
	/** Values for form fields by kind (name, email, room), only those set. */
	final Map<String, String> person;
	final boolean webview;
	final boolean macRotate;
	final List<Rule> rules;
	/** Open WLANs that need an account (community hotspots of home routers): never tried. */
	final List<String> skip;

	private Config(String text) {
		String s = "", b = "", u = "", p = "";
		int iv = 60;
		boolean wv = true, mac = false;
		List<String> pr = new ArrayList<>();
		Map<String, String> pers = new LinkedHashMap<>();
		List<Rule> rl = new ArrayList<>();
		List<String> sk = new ArrayList<>();
		for (String line : text.split("\n")) {
			line = line.trim();
			int eq = line.indexOf('=');
			if (line.isEmpty() || line.startsWith("#") || eq < 1) {
				continue;
			}
			String k = line.substring(0, eq).trim().toLowerCase(Locale.ROOT), v = line.substring(eq + 1).trim();
			switch (k) {
			case "ssid": s = v; break;
			case "bssid": b = v.toLowerCase(Locale.ROOT); break;
			case "interval": iv = Math.max(10, Integer.parseInt(v)); break;
			case "probe": pr.add(v); break;
			case "user": u = v; break;
			case "password": p = v; break;
			case "name": case "email": case "room": if (!v.isEmpty()) { pers.put(k, v); } break;
			case "webview": wv = onOff(k, v); break;
			case "mac-rotate": mac = onOff(k, v); break;
			case "rule": rl.add(rule(v)); break;
			case "skip": sk.add(v); break;
			default: throw new IllegalArgumentException("unknown setting " + k);
			}
		}
		if (pr.isEmpty()) {
			pr.add("http://connectivitycheck.gstatic.com/generate_204");
			// A second, independent probe: some portals let Google's probe through but nothing else, or the reverse.
			pr.add("http://captive.apple.com/hotspot-detect.html");
		}
		ssid = s;
		bssid = b;
		interval = iv;
		probes = Collections.unmodifiableList(pr);
		user = u;
		password = p;
		person = Collections.unmodifiableMap(pers);
		webview = wv;
		macRotate = mac;
		rules = Collections.unmodifiableList(rl);
		if (sk.isEmpty()) {
			sk.add("*Homespot*");
			sk.add("*_FON*");
		}
		skip = Collections.unmodifiableList(sk);
	}

	private static boolean onOff(String k, String v) {
		if (v.equals("on")) {
			return true;
		}
		if (v.equals("off")) {
			return false;
		}
		throw new IllegalArgumentException(k + " must be on or off");
	}

	private static Rule rule(String v) {
		String[] p = v.split("\\s+", 3);
		if (p.length < 3 || !(p[1].equals("click") || (p[1].equals("fill") && p[2].indexOf('=') > 0))) {
			throw new IllegalArgumentException("rule must be HOST click TEXT or HOST fill NAME=VALUE: " + v);
		}
		return new Rule(p[0], p[1], p[2]);
	}

	boolean skipped(String ssid) {
		for (String g : skip) {
			if (new Rule(g, "skip", "").matches(ssid)) {
				return true;
			}
		}
		return false;
	}

	List<Rule> rulesFor(String host) {
		List<Rule> r = new ArrayList<>();
		for (Rule x : rules) {
			if (x.matches(host)) {
				r.add(x);
			}
		}
		return r;
	}

	static String readText(Context ctx) throws IOException {
		File f = new File(ctx.getFilesDir(), FILE);
		return f.exists() ? new String(Files.readAllBytes(f.toPath()), StandardCharsets.UTF_8) : DEFAULT_TEXT;
	}

	/** Checks the text before it replaces the file, so a typo never stops the service. */
	static void writeText(Context ctx, String text) throws IOException {
		new Config(text);
		File f = new File(ctx.getFilesDir(), FILE), tmp = new File(ctx.getFilesDir(), FILE + ".new");
		Files.write(tmp.toPath(), text.getBytes(StandardCharsets.UTF_8));
		if (!tmp.renameTo(f)) {
			throw new IOException("cannot replace " + f);
		}
	}

	/** text with KEY=VALUE replacing the key's line, or added; VALUE empty removes it. */
	static String withSetting(String text, String key, String value) {
		StringBuilder b = new StringBuilder();
		for (String line : text.split("\n", -1)) {
			if (!line.trim().startsWith(key + "=")) {
				b.append(line).append('\n');
			}
		}
		String t = b.toString().replaceAll("\n+$", "\n");
		return value.isEmpty() ? t : t + key + "=" + value + "\n";
	}

	/** Takes over a settings file from the card, if one is there. */
	static void importCard(Context ctx) throws IOException {
		if (CARD_FILE.isFile()) {
			writeText(ctx, new String(Files.readAllBytes(CARD_FILE.toPath()), StandardCharsets.UTF_8));
			if (!CARD_FILE.delete()) {
				throw new IOException("cannot delete " + CARD_FILE);
			}
			Journal.log("settings taken from " + CARD_FILE);
		}
	}

	static Config load(Context ctx) throws IOException {
		importCard(ctx);
		return new Config(readText(ctx));
	}
}
