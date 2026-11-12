/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
package org.minitv;

import android.os.Environment;
import android.util.Log;
import java.io.File;
import java.io.IOException;

/**
 * Display dark and back, for saving power while the TV only listens (radio, voice control). An app may not set
 * the brightness below Android's own minimum; on the Echo Show the root service echoscreen (echo-show-2gen
 * repository) watches this file and sets Android's lowest brightness while it exists. Elsewhere the file does
 * nothing.
 */
final class Screen {
	private static final String TAG = "minitv";

	private Screen() {
	}

	// The card's root by its old name: the Echo Show runs Android 7, and the root service looks there.
	@SuppressWarnings("deprecation")
	static File flag() {
		return new File(new File(Environment.getExternalStorageDirectory(), "minitv"), "screen-off");
	}

	static boolean isOff() {
		return flag().exists();
	}

	static void setOff(boolean off) throws IOException {
		File f = flag();
		if (off) {
			File dir = f.getParentFile();
			if (!dir.isDirectory() && !dir.mkdirs()) throw new IOException("cannot create " + dir);
			if (!f.exists() && !f.createNewFile()) throw new IOException("cannot create " + f);
		} else if (f.exists() && !f.delete()) {
			throw new IOException("cannot delete " + f);
		}
		Log.i(TAG, "screen " + (off ? "off" : "on"));
	}
}
