/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
package org.minicaptive;

import android.content.Context;
import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.util.ArrayList;
import java.util.List;
import java.util.Properties;

/**
 * Which way through the portal worked, per WLAN (files/strategies.properties), so the next visit tries that first;
 * the others still follow when it stops working (portals change).
 */
final class Strategies {
	enum Way { API, WISPR, FORM, BROWSER }

	private final File file;
	private final Properties map = new Properties();

	Strategies(Context ctx) throws IOException {
		file = new File(ctx.getFilesDir(), "strategies.properties");
		if (file.exists()) {
			try (InputStream in = new FileInputStream(file)) {
				map.load(in);
			}
		}
	}

	/** The remembered way first, then the rest in the usual order (cheapest first). */
	List<Way> order(String ssid) {
		List<Way> out = new ArrayList<>();
		String r = map.getProperty(ssid);
		for (Way w : Way.values()) {
			if (w.name().equals(r)) {
				out.add(w);
			}
		}
		for (Way w : Way.values()) {
			if (!out.contains(w)) {
				out.add(w);
			}
		}
		return out;
	}

	String remembered(String ssid) {
		return map.getProperty(ssid);
	}

	void worked(String ssid, Way w) throws IOException {
		if (w.name().equals(map.getProperty(ssid))) {
			return;
		}
		map.setProperty(ssid, w.name());
		File tmp = new File(file.getPath() + ".new");
		try (OutputStream o = new FileOutputStream(tmp)) {
			map.store(o, "minicaptive: way through the portal that worked last, per WLAN");
		}
		if (!tmp.renameTo(file)) {
			throw new IOException("cannot replace " + file);
		}
		Journal.log("remembered for \"" + ssid + "\": " + w);
	}
}
