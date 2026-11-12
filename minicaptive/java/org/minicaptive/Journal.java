/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */
package org.minicaptive;

import android.content.Context;
import android.util.Log;
import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;
import java.text.SimpleDateFormat;
import java.util.ArrayDeque;
import java.util.Date;
import java.util.Locale;

/**
 * What the service did, for logcat, the status screen (same process, so no IPC) and a file on the card, to be read
 * with adb after a trip: /sdcard/minicaptive/minicaptive.log (2 MB, then one older .1 file).
 */
final class Journal {
	static final String TAG = "minicaptive";

	interface Listener {
		void changed();
	}

	private static final ArrayDeque<String> lines = new ArrayDeque<>();
	private static String state = "starting";
	private static String lastResult = "";
	private static Listener listener;
	static final File FILE = new File("/sdcard/minicaptive/minicaptive.log");
	private static final File OLD = new File(FILE.getPath() + ".1");
	private static final long MAX_FILE = 2 << 20;
	private static String fileError;
	/** The "log to file" switch, stored as a file so the service sees it after a reboot too. */
	private static File fileFlag;
	private static boolean fileOn;

	private Journal() {
	}

	static synchronized void log(String msg) {
		Log.i(TAG, msg);
		Date now = new Date();
		lines.addFirst(new SimpleDateFormat("HH:mm:ss", Locale.ROOT).format(now) + "  " + msg);
		toFile(new SimpleDateFormat("yyyy-MM-dd HH:mm:ss", Locale.ROOT).format(now) + "  " + msg + "\n");
		while (lines.size() > 300) {
			lines.removeLast();
		}
		notifyListener();
	}

	static synchronized void state(String s) {
		if (!s.equals(state)) {
			state = s;
			log("state: " + s);
		}
	}

	static synchronized void result(String r) {
		lastResult = new SimpleDateFormat("HH:mm:ss", Locale.ROOT).format(new Date()) + "  " + r;
		log(r);
	}

	/** The file log is for later; when the card is not writable that is shown on the screen, not hidden. */
	static synchronized void init(Context ctx) {
		if (fileFlag == null) {
			fileFlag = new File(ctx.getFilesDir(), "log-to-file");
			fileOn = fileFlag.exists();
		}
	}

	static synchronized boolean fileOn() {
		return fileOn;
	}

	static synchronized void setFileOn(boolean on) throws IOException {
		if (on ? !fileFlag.exists() && !fileFlag.createNewFile() : fileFlag.exists() && !fileFlag.delete()) {
			throw new IOException("cannot switch the log file " + (on ? "on" : "off"));
		}
		fileOn = on;
		log(on ? "log file on: " + FILE : "log file off");
	}

	static synchronized boolean fileExists() {
		return FILE.exists() || OLD.exists();
	}

	/** The log file and what the screen shows of it: deleting the log means it is gone everywhere. */
	static synchronized void deleteFile() throws IOException {
		for (File f : new File[] { FILE, OLD }) {
			if (f.exists() && !f.delete()) {
				throw new IOException("cannot delete " + f);
			}
		}
		lines.clear();
		lastResult = "";
		notifyListener();
	}

	private static void toFile(String line) {
		if (!fileOn) {
			return;
		}
		try {
			File dir = FILE.getParentFile();
			if (!dir.isDirectory() && !dir.mkdirs()) {
				throw new IOException("cannot create " + dir);
			}
			if (FILE.length() > MAX_FILE && !FILE.renameTo(OLD)) {
				throw new IOException("cannot rotate " + FILE);
			}
			try (OutputStream o = new FileOutputStream(FILE, true)) {
				o.write(line.getBytes(StandardCharsets.UTF_8));
			}
			fileError = null;
		} catch (IOException e) {
			if (fileError == null) {
				Log.e(TAG, "log file: " + e);
				lines.addFirst("log file not written: " + e.getMessage());
			}
			fileError = e.toString();
		}
	}

	static synchronized String state() {
		return state;
	}

	static synchronized String lastResult() {
		return lastResult;
	}

	static synchronized String text() {
		return String.join("\n", lines);
	}

	static synchronized void setListener(Listener l) {
		listener = l;
	}

	private static void notifyListener() {
		if (listener != null) {
			listener.changed();
		}
	}
}
