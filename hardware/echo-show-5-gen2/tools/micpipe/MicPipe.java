/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

import android.media.AudioAttributes;
import android.media.AudioFormat;
import android.media.AudioManager;
import android.media.AudioRecord;
import android.media.MediaRecorder;
import android.media.ToneGenerator;
import java.io.BufferedReader;
import java.io.FileDescriptor;
import java.io.FileOutputStream;
import java.io.FileReader;
import java.io.IOException;
import java.lang.reflect.Field;

/**
 * Writes the device's processed microphone stream to stdout as 16 kHz mono S16LE. Run with app_process as root:
 * a native client would need the OpenSL ES headers, which need an NDK. This path keeps Amazon's echo cancellation
 * and beamforming, which raw PCM does not have.
 */
public final class MicPipe {
	private static final int RATE = 16000;
	private static final int BEEP_MS = 150;
	// A hardware mute delivers exact zeros, and a stream left open may stay silent after the unmute.
	private static final int ZERO_BLOCKS_BEFORE_REOPEN = 500;

	@SuppressWarnings("deprecation") // prepareMainLooper is the only way on Android 7, which is what runs this
	public static void main(String[] args) throws Exception {
		// Without an ActivityThread, AudioRecord hands a null package name to native code, which crashes.
		android.os.Looper.prepareMainLooper();
		Class.forName("android.app.ActivityThread").getMethod("systemMain").invoke(null);
		if (args.length > 0 && !args[0].equals("-")) {
			beepOnRequest(args[0]);
		}
		int source = args.length > 1 ? Integer.parseInt(args[1]) : MediaRecorder.AudioSource.VOICE_RECOGNITION;
		FileOutputStream out = new FileOutputStream(FileDescriptor.out);
		byte[] block = new byte[320 * 2];
		while (true) {
			int min = AudioRecord.getMinBufferSize(RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT);
			AudioRecord rec = open(source, min * 4);
			if (rec.getState() != AudioRecord.STATE_INITIALIZED) {
				throw new IOException("cannot open the microphone");
			}
			rec.startRecording();
			int zero = 0;
			while (zero < ZERO_BLOCKS_BEFORE_REOPEN) {
				int n = rec.read(block, 0, block.length);
				if (n <= 0) {
					throw new IOException("microphone read failed: " + n);
				}
				out.write(block, 0, n);
				zero = isZero(block, n) ? zero + 1 : 0;
			}
			rec.stop();
			rec.release();
		}
	}

	/** Amazon's sources are not in the public constants, which the plain constructor would replace by the default. */
	private static AudioRecord open(int source, int bytes) throws Exception {
		AudioAttributes attributes = new AudioAttributes.Builder().build();
		Field field = AudioAttributes.class.getDeclaredField("mSource");
		field.setAccessible(true);
		field.setInt(attributes, source);
		AudioFormat format = new AudioFormat.Builder().setSampleRate(RATE)
				.setChannelMask(AudioFormat.CHANNEL_IN_MONO).setEncoding(AudioFormat.ENCODING_PCM_16BIT).build();
		return new AudioRecord(attributes, format, bytes, AudioManager.AUDIO_SESSION_ID_GENERATE);
	}

	/**
	 * Beeps for each line written to a named pipe. This process is already running: starting another one to play
	 * a sound would take a second, too late to tell the person that it is their turn.
	 */
	private static void beepOnRequest(String fifo) {
		Thread thread = new Thread("beep") {
			@Override
			public void run() {
			ToneGenerator tone = new ToneGenerator(AudioManager.STREAM_MUSIC, ToneGenerator.MAX_VOLUME);
			while (true) {
				try (BufferedReader requests = new BufferedReader(new FileReader(fifo))) {
					while (requests.readLine() != null) {
						tone.startTone(ToneGenerator.TONE_PROP_BEEP, BEEP_MS);
					}
				} catch (IOException e) {
					throw new IllegalStateException(e);
				}
			}
			}
		};
		thread.setDaemon(true);
		thread.start();
	}

	private static boolean isZero(byte[] block, int n) {
		for (int i = 0; i < n; i++) {
			if (block[i] != 0) {
				return false;
			}
		}
		return true;
	}
}
