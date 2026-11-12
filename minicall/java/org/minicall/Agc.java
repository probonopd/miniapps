/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */
package org.minicall;

import android.util.Log;
import java.util.Locale;

/**
 * Automatic gain for 16-bit PCM frames: brings speech to a steady level, whether the person sits close or across the
 * room and whether the far end sends loud or quiet. Down fast (no blasting), up slowly and only while there is
 * signal above the noise gate, and a limiter so that peaks never clip.
 * The gate also closes: in pauses the noise is faded down (by GATE_DB), otherwise the gain raised for the speech
 * carries the room's noise into the far end's loudspeaker. It opens at the first loud frame and stays open for
 * HOLD_MS after the last one, so the soft ends of words are kept.
 * A fixed gain (not auto) only applies that gain, with the same limiter and gate.
 */
final class Agc {
	private static final String TAG = "minicall";
	private static final double LIMIT = 30000;
	private static final double GATE_DB = -20;
	private static final int HOLD_MS = 300;

	private final String name;
	private final boolean auto;
	private final double targetDb, minDb, maxDb, gateDb;
	private double gainDb;
	// The gate's own attenuation (0 open, GATE_DB closed) and the time since the last frame above it.
	private double gateGainDb;
	private int quietMs = HOLD_MS;
	private int frames;
	private double levelSum;
	private int levelCount;

	/** Automatic: aim at targetDb (dBFS), gain between minDb and maxDb, nothing raised below gateDb. */
	static Agc auto(String name, double targetDb, double startDb, double minDb, double maxDb, double gateDb) {
		return new Agc(name, true, targetDb, startDb, minDb, maxDb, gateDb);
	}

	static Agc fixed(String name, double gainDb, double gateDb) {
		return new Agc(name, false, 0, gainDb, gainDb, gainDb, gateDb);
	}

	private Agc(String name, boolean auto, double targetDb, double startDb, double minDb, double maxDb, double gateDb) {
		this.name = name;
		this.auto = auto;
		this.targetDb = targetDb;
		this.gainDb = startDb;
		this.minDb = minDb;
		this.maxDb = maxDb;
		this.gateDb = gateDb;
	}

	/** One frame in place; frameMs is its length, for the speed of the gain changes. */
	void process(byte[] b, int len, int frameMs) {
		int n = len / 2;
		if (n == 0) return;
		double sum = 0;
		int peak = 0;
		for (int i = 0; i < n; i++) {
			int v = (short) ((b[2 * i] & 0xff) | (b[2 * i + 1] << 8));
			sum += (double) v * v;
			peak = Math.max(peak, Math.abs(v));
		}
		double levelDb = 20 * Math.log10(Math.max(Math.sqrt(sum / n), 1) / 32768);
		double before = gainDb + gateGainDb;
		boolean loud = levelDb > gateDb;
		quietMs = loud ? 0 : quietMs + frameMs;
		if (quietMs < HOLD_MS) {
			gateGainDb = 0;
		} else {
			// Closing over about 150 ms: no audible pumping.
			gateGainDb = Math.max(GATE_DB, gateGainDb - 2.6 * frameMs / 20);
		}
		if (auto && loud) {
			double want = Math.max(minDb, Math.min(maxDb, targetDb - levelDb));
			// Down within a few frames, up by 4 dB a second.
			double step = want < gainDb ? Math.max(want - gainDb, -6.0 * frameMs / 20) : Math.min(want - gainDb, 4.0 * frameMs / 1000);
			gainDb += step;
		}
		double g0 = Math.pow(10, before / 20), g1 = Math.pow(10, (gainDb + gateGainDb) / 20);
		// The limiter: this frame no louder than LIMIT at its peak, whatever the gain says.
		double cap = peak > 0 ? LIMIT / peak : Double.MAX_VALUE;
		for (int i = 0; i < n; i++) {
			// The gain glides across the frame, so its steps make no clicks.
			double g = Math.min(g0 + (g1 - g0) * i / n, cap);
			int v = (int) Math.round((short) ((b[2 * i] & 0xff) | (b[2 * i + 1] << 8)) * g);
			v = Math.max(Short.MIN_VALUE, Math.min(Short.MAX_VALUE, v));
			b[2 * i] = (byte) v;
			b[2 * i + 1] = (byte) (v >> 8);
		}
		levelSum += levelDb;
		levelCount++;
		if (++frames * frameMs >= 10000) {
			Log.i(TAG, String.format(Locale.ROOT, "%s: level %.0f dBFS, gain %+.0f dB", name, levelSum / levelCount, gainDb));
			frames = 0;
			levelSum = 0;
			levelCount = 0;
		}
	}
}
