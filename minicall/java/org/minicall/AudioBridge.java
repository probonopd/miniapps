/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
package org.minicall;

import android.media.AudioFormat;
import android.media.AudioManager;
import android.media.AudioRecord;
import android.media.AudioTrack;
import android.media.audiofx.AcousticEchoCanceler;
import android.media.audiofx.AudioEffect;
import android.media.audiofx.AutomaticGainControl;
import android.net.LocalSocket;
import android.util.Log;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.util.Arrays;

/**
 * Microphone and speaker for the helper's sockaudio module. The helper connects once per direction when a call
 * starts its audio, announces "srate ch ptime", and the bridge opens the device to match.
 */
final class AudioBridge {
	private static final String TAG = "minicall";
	// The microphone may still be held by the wake word service for a moment after a call starts.
	private static final long MIC_WAIT_MS = 10000;

	final String micName;
	final String speakerName;
	private final SocketServer mic;
	private final SocketServer speaker;
	private final int micSource;
	private final double micGainDb, speakerGainDb;
	private final boolean callStream;
	private final boolean webrtcAudio;
	private final int stream;
	/** Babyphone: the watching phone sends silence (no recording at all), the watched device plays nothing. */
	volatile boolean micSilent;
	volatile boolean speakerSilent;
	/** The microphone the user plugged in or paired; null: the device's own (Android's choice). */
	private volatile android.media.AudioDeviceInfo input;
	private volatile AudioRecord recording;

	/** callStream: playback on the voice call stream, which a phone's echo canceller takes as its reference. */
	/** webrtcAudio: the helper processes the sound (echo, noise, gain), the bridge passes it unchanged. */
	AudioBridge(String prefix, int micSource, double micGainDb, double speakerGainDb, boolean callStream,
			boolean webrtcAudio) throws IOException {
		this.webrtcAudio = webrtcAudio;
		this.micSource = micSource;
		this.micGainDb = micGainDb;
		this.speakerGainDb = speakerGainDb;
		this.callStream = callStream;
		stream = callStream ? AudioManager.STREAM_VOICE_CALL : AudioManager.STREAM_MUSIC;
		micName = prefix + ".mic";
		speakerName = prefix + ".spk";
		mic = new SocketServer(micName, this::runMic);
		speaker = new SocketServer(speakerName, this::runSpeaker);
	}

	/** Switches the call's microphone, also during a recording. */
	void preferInput(android.media.AudioDeviceInfo d) {
		input = d;
		AudioRecord r = recording;
		if (r != null) {
			r.setPreferredDevice(d);
		}
	}

	void close() {
		mic.close();
		speaker.close();
	}

	private static int[] readHeader(InputStream in) throws IOException {
		String[] p = SocketServer.readLine(in).split(" ");
		if (p.length != 3) throw new IOException("bad header");
		return new int[] {Integer.parseInt(p[0]), Integer.parseInt(p[1]), Integer.parseInt(p[2])};
	}

	private AudioRecord openRecord(int srate, int chMask, int bufBytes) throws IOException {
		AudioRecord rec = new AudioRecord(micSource, srate, chMask, AudioFormat.ENCODING_PCM_16BIT, bufBytes);
		if (rec.getState() != AudioRecord.STATE_INITIALIZED) {
			rec.release();
			throw new IOException("AudioRecord not initialized (source " + micSource + ", " + srate + " Hz)");
		}
		if (stream == AudioManager.STREAM_VOICE_CALL) effects(rec.getAudioSessionId());
		rec.setPreferredDevice(input);
		rec.startRecording();
		recording = rec;
		return rec;
	}

	/**
	 * A phone on the table: gain control brings a voice from across the room up to level, the echo canceller keeps
	 * the loudspeaker out. Each only where the device has it.
	 */
	private static void effects(int session) {
		StringBuilder on = new StringBuilder();
		if (AutomaticGainControl.isAvailable()) {
			AutomaticGainControl e = AutomaticGainControl.create(session);
			if (e != null && e.setEnabled(true) == AudioEffect.SUCCESS) on.append(" gain-control");
		}
		// No platform noise suppressor: on the Moto it took the voice down with the noise, a person across the
		// room was hardly heard. The noise gate (Agc) keeps the pauses quiet instead.
		if (AcousticEchoCanceler.isAvailable()) {
			AcousticEchoCanceler e = AcousticEchoCanceler.create(session);
			if (e != null && e.setEnabled(true) == AudioEffect.SUCCESS) on.append(" echo-cancelling");
		}
		Log.i(TAG, "mic effects:" + (on.length() > 0 ? on : " none"));
	}

	private void runMic(LocalSocket s) throws IOException {
		int[] h = readHeader(s.getInputStream());
		int srate = h[0], ch = h[1], ptime = h[2];
		int frameBytes = srate * ch * ptime / 1000 * 2;
		if (micSilent) {
			// Watching as a babyphone: silence without opening the microphone at all.
			Log.i(TAG, "mic: silent (babyphone)");
			byte[] quiet = new byte[frameBytes];
			OutputStream o = s.getOutputStream();
			while (true) {
				o.write(quiet);
				sleep(ptime);
			}
		}
		int chMask = ch == 1 ? AudioFormat.CHANNEL_IN_MONO : AudioFormat.CHANNEL_IN_STEREO;
		int bufBytes = Math.max(AudioRecord.getMinBufferSize(srate, chMask, AudioFormat.ENCODING_PCM_16BIT),
				frameBytes * 4);
		Log.i(TAG, "mic: source " + micSource + ", " + srate + " Hz, " + ch + " ch, " + ptime + " ms");
		byte[] buf = new byte[frameBytes];
		// Speech to about -20 dBFS. The Echo Show's processed stream is quiet (speech about -53 dBFS at a normal
		// distance, room noise about -75); a phone's call microphone with noise suppression on the table is even
		// quieter (-70 to -85 dBFS on average). The gate sits above the noise.
		Agc agc = webrtcAudio ? null : !Double.isNaN(micGainDb) ? Agc.fixed("mic", micGainDb, callStream ? -68 : -64)
				: callStream ? Agc.auto("mic", -20, 12, -6, 36, -68) : Agc.auto("mic", -20, 20, 0, 36, -64);
		OutputStream out = s.getOutputStream();
		long deadline = System.currentTimeMillis() + MIC_WAIT_MS;
		AudioRecord rec = openRecord(srate, chMask, bufBytes);
		try {
			while (true) {
				int n = 0;
				while (n < frameBytes) {
					int r = rec.read(buf, n, frameBytes - n);
					if (r > 0) {
						n += r;
						continue;
					}
					// No capture slot yet: silence for the call meanwhile, then a new recording.
					if (System.currentTimeMillis() > deadline) throw new IOException("AudioRecord.read " + r);
					rec.release();
					Arrays.fill(buf, (byte) 0);
					out.write(buf);
					sleep(ptime);
					rec = openRecord(srate, chMask, bufBytes);
					n = 0;
				}
				if (agc != null) agc.process(buf, frameBytes, ptime);
				out.write(buf);
			}
		} finally {
			rec.release();
		}
	}

	private void runSpeaker(LocalSocket s) throws IOException {
		InputStream in = s.getInputStream();
		OutputStream out = s.getOutputStream();
		int[] h = readHeader(in);
		int srate = h[0], ch = h[1], ptime = h[2];
		int frameBytes = srate * ch * ptime / 1000 * 2;
		if (speakerSilent) {
			// Watched as a babyphone: nothing of the far end may be heard here; the frames are still taken.
			Log.i(TAG, "speaker: silent (babyphone)");
			byte[] skip = new byte[frameBytes];
			while (true) {
				out.write('r');
				SocketServer.readFully(in, skip, frameBytes);
				sleep(ptime);
			}
		}
		int chMask = ch == 1 ? AudioFormat.CHANNEL_OUT_MONO : AudioFormat.CHANNEL_OUT_STEREO;
		int min = AudioTrack.getMinBufferSize(srate, chMask, AudioFormat.ENCODING_PCM_16BIT);
		@SuppressWarnings("deprecation")
		AudioTrack track = new AudioTrack(stream, srate, chMask, AudioFormat.ENCODING_PCM_16BIT,
				Math.max(min, frameBytes * 4), AudioTrack.MODE_STREAM);
		if (track.getState() != AudioTrack.STATE_INITIALIZED) {
			track.release();
			throw new IOException("AudioTrack not initialized");
		}
		Log.i(TAG, "speaker: " + srate + " Hz, " + ch + " ch, " + ptime + " ms");
		byte[] buf = new byte[frameBytes];
		// The far end at a steady level: quiet senders up, loud ones down.
		// With WebRTC's processing the far end arrives at a steady level already, and the echo canceller needs the
		// played sound to be the sound it was given; only a set gain changes it then.
		Agc agc = webrtcAudio && Double.isNaN(speakerGainDb) ? null
				: !Double.isNaN(speakerGainDb) ? Agc.fixed("speaker", speakerGainDb, -58)
				: Agc.auto("speaker", -18, 0, -10, 20, -58);
		track.play();
		try {
			// One request per frame: the blocking AudioTrack.write paces the pulls, so no queue builds up.
			while (true) {
				out.write('r');
				SocketServer.readFully(in, buf, frameBytes);
				if (agc != null) agc.process(buf, frameBytes, ptime);
				track.write(buf, 0, frameBytes);
			}
		} finally {
			track.stop();
			track.release();
		}
	}

	private static void sleep(int ms) {
		try {
			Thread.sleep(ms);
		} catch (InterruptedException e) {
			Thread.currentThread().interrupt();
		}
	}
}
