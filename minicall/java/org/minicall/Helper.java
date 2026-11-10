/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
package org.minicall;

import android.util.Log;
import java.io.BufferedReader;
import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;
import org.json.JSONException;
import org.json.JSONObject;

/**
 * The baresip helper process. Commands go to its stdin and events come from its stdout, one JSON object per line
 * (ctrl_stdio); stderr is its log. The pipes are this app's own, so no other app can steer a call.
 */
final class Helper {
	private static final String TAG = "minicall";

	interface Listener {
		/** On the helper's reader thread. */
		void onEvent(JSONObject event);

		void onExit();
	}

	private final Process process;
	private final OutputStream stdin;
	private int token;

	Helper(String exe, File dir, String config, String accounts, Listener listener) throws IOException {
		if (!dir.isDirectory() && !dir.mkdirs()) throw new IOException("cannot create " + dir);
		write(new File(dir, "config"), config);
		write(new File(dir, "accounts"), accounts);
		write(new File(dir, "contacts"), "");
		process = new ProcessBuilder(exe, "-f", dir.getPath()).start();
		stdin = process.getOutputStream();
		thread("helper-events", () -> {
			try (BufferedReader r = reader(process.getInputStream())) {
				for (String line; (line = r.readLine()) != null; ) {
					if (!line.startsWith("{")) {
						Log.i(TAG, "helper: " + line);
						continue;
					}
					try {
						JSONObject o = new JSONObject(line);
						if (o.optBoolean("event")) listener.onEvent(o);
						else if (!o.optBoolean("ok", true)) Log.w(TAG, "command failed: " + line);
					} catch (JSONException e) {
						Log.w(TAG, "helper: bad JSON " + line);
					}
				}
			} catch (IOException ignored) {
				// The helper ended.
			}
			int code;
			try {
				code = process.waitFor();
			} catch (InterruptedException e) {
				Thread.currentThread().interrupt();
				code = -1;
			}
			Log.i(TAG, "helper exited with " + code);
			listener.onExit();
		});
		thread("helper-log", () -> {
			try (BufferedReader r = reader(process.getErrorStream())) {
				for (String line; (line = r.readLine()) != null; ) {
					// The status line of a call is rewritten in place many times a second.
					if (line.indexOf('\r') >= 0) line = line.substring(line.lastIndexOf('\r') + 1);
					// The call statistics line comes several times a second.
					if (line.startsWith("[0:") || line.startsWith("[1:")) continue;
					if (!line.isEmpty()) Log.i(TAG, "helper: " + line.replaceAll("\u001b\\[[0-9;]*m", ""));
				}
			} catch (IOException ignored) {
				// The helper ended.
			}
		});
	}

	void command(String command, String params) {
		send(command, params);
	}

	/** A command whose parameters are an object (webrtc_answer). */
	void command(String command, JSONObject params) {
		send(command, params);
	}

	private synchronized void send(String command, Object params) {
		try {
			JSONObject o = new JSONObject();
			o.put("command", command);
			if (params != null) o.put("params", params);
			o.put("token", Integer.toString(++token));
			stdin.write((o.toString() + "\n").getBytes(StandardCharsets.UTF_8));
			stdin.flush();
		} catch (JSONException | IOException e) {
			Log.w(TAG, "command " + command + " not sent: " + e);
		}
	}

	/** Closing stdin makes the helper hang up and quit; destroy() is the fallback. */
	void stop() {
		try {
			stdin.close();
		} catch (IOException ignored) {
			// Already gone.
		}
		thread("helper-stop", () -> {
			try {
				Thread.sleep(3000);
			} catch (InterruptedException ignored) {
				// Kill it now then.
			}
			process.destroy();
		});
	}

	private static BufferedReader reader(java.io.InputStream in) {
		return new BufferedReader(new InputStreamReader(in, StandardCharsets.UTF_8));
	}

	private static void thread(String name, Runnable r) {
		Thread t = new Thread(r, name);
		t.setDaemon(true);
		t.start();
	}

	private static void write(File f, String text) throws IOException {
		try (OutputStream out = new FileOutputStream(f)) {
			out.write(text.getBytes(StandardCharsets.UTF_8));
		}
	}
}
