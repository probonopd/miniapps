/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
package org.minicall;

import android.net.LocalServerSocket;
import android.net.LocalSocket;
import android.os.Process;
import android.util.Log;
import java.io.IOException;
import java.io.InputStream;

/**
 * An abstract Unix socket the helper connects to, one thread per connection. Abstract names are visible to every
 * app, so the name is random and a peer that is not this app's own uid is dropped.
 */
final class SocketServer {
	private static final String TAG = "minicall";

	interface Handler {
		void handle(LocalSocket s) throws IOException;
	}

	private final LocalServerSocket server;
	private volatile boolean running = true;

	SocketServer(String name, Handler handler) throws IOException {
		server = new LocalServerSocket(name);
		Thread t = new Thread(() -> {
			while (running) {
				LocalSocket s;
				try {
					s = server.accept();
				} catch (IOException e) {
					return;
				}
				try {
					if (s.getPeerCredentials().getUid() != Process.myUid()) {
						Log.w(TAG, name + ": foreign uid " + s.getPeerCredentials().getUid());
						close(s);
						continue;
					}
				} catch (IOException e) {
					close(s);
					continue;
				}
				Thread io = new Thread(() -> {
					try {
						handler.handle(s);
					} catch (IOException | RuntimeException e) {
						Log.i(TAG, name + ": " + e.getMessage());
					} finally {
						close(s);
					}
				}, name);
				io.start();
			}
		}, "accept " + name);
		t.setDaemon(true);
		t.start();
	}

	void close() {
		running = false;
		try {
			server.close();
		} catch (IOException ignored) {
			// Closing only wakes the accept thread.
		}
	}

	static String readLine(InputStream in) throws IOException {
		StringBuilder sb = new StringBuilder();
		for (int c; (c = in.read()) != '\n'; ) {
			if (c < 0 || sb.length() > 80) throw new IOException("bad header");
			sb.append((char) c);
		}
		return sb.toString().trim();
	}

	static void readFully(InputStream in, byte[] buf, int len) throws IOException {
		int n = 0;
		while (n < len) {
			int r = in.read(buf, n, len - n);
			if (r < 0) throw new IOException("closed by the helper");
			n += r;
		}
	}

	private static void close(LocalSocket s) {
		try {
			s.close();
		} catch (IOException ignored) {
			// Nothing left to do with a broken socket.
		}
	}
}
