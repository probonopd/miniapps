package org.minitv;

import android.app.Activity;
import android.content.Context;
import android.content.Intent;
import android.media.AudioDeviceInfo;
import android.media.AudioFormat;
import android.media.AudioManager;
import android.media.AudioRecord;
import android.media.MediaRecorder;
import android.media.ToneGenerator;
import android.os.Bundle;
import android.os.SystemClock;
import android.util.Log;
import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.util.concurrent.FutureTask;
import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;
import org.vosk.LibVosk;
import org.vosk.LogLevel;
import org.vosk.Model;
import org.vosk.Recognizer;

/**
 * Answers the remote's voice button (the system starts a web search for it), listens while the button is held,
 * recognizes the channel name on the device and tells the TV screen to switch. Nothing leaves the projector.
 */
public class VoiceActivity extends Activity {
	private static final String TAG = "minitv";
	private static final int RATE = 16000;
	private static final int MAX_BLOCKS = 80;
	private static final int SILENT_BLOCKS = 3;
	private static final int SILENT_RMS = 20;
	private static final double MIN_CONFIDENCE = 0.7;
	private static final int FAILURE_TONE_MS = 500;
	private static final int UNMUTE_MS = 500;

	// This activity runs in its own process (see the manifest) so that the loaded model can stay in memory
	// between presses and Android can drop it, instead of the TV, when memory runs short.
	private static Model cached;

	@Override
	protected void onCreate(Bundle state) {
		super.onCreate(state);
		new Thread(this::listen, "voice").start();
	}

	private void listen() {
		try {
			// This activity has its own process, and a channel number has to mean the same as in the player's.
			Playlist.init(this);
			Playlist playlist = Playlist.load();
			// Loading the model takes a while, so it runs while the person is still speaking.
			FutureTask<Model> model = new FutureTask<>((java.util.concurrent.Callable<Model>) this::model);
			new Thread(model, "voice-model").start();
			byte[] speech = speech();
			saveIfDebugging(speech);
			long start = SystemClock.elapsedRealtime();
			String text = recognize(model.get(), speech, playlist);
			Log.i(TAG, "voice: heard \"" + text + "\" (" + (SystemClock.elapsedRealtime() - start) + " ms to recognize)");
			int channel = ChannelMatcher.match(text, playlist.phrases, playlist.owners);
			String app = handsFree() ? ChannelMatcher.appFor(text, apps()) : null;
			int volumeStep = handsFree() ? ChannelMatcher.volumeStep(text) : 0;
			Boolean screenOff = handsFree() ? ChannelMatcher.screenOff(text) : null;
			// Any command understood brings the display back: someone wants something from the TV. Misheard
			// noise (the failure beep below) leaves it off, so a false wake word does not light the room up.
			boolean quit = handsFree() && ChannelMatcher.isQuit(text);
			// "Ausschalten" means the whole TV: home screen and the display off with it.
			boolean switchOff = quit && ChannelMatcher.isSwitchOff(text);
			if (!Boolean.TRUE.equals(screenOff) && !switchOff
					&& (screenOff != null || volumeStep != 0 || app != null || channel >= 0 || quit)) {
				Screen.setOff(false);
			}
			if (screenOff != null) {
				Log.i(TAG, "voice: screen " + (screenOff ? "off" : "on"));
				Screen.setOff(screenOff);
				finishCapture();
			} else if (volumeStep != 0) {
				AudioManager audio = (AudioManager) getSystemService(AUDIO_SERVICE);
				int max = audio.getStreamMaxVolume(AudioManager.STREAM_MUSIC);
				int volume = ChannelMatcher.nextVolume(audio.getStreamVolume(AudioManager.STREAM_MUSIC), max, volumeStep);
				Log.i(TAG, "voice: volume " + volume + " of " + max);
				audio.setStreamVolume(AudioManager.STREAM_MUSIC, volume, 0);
				finishCapture();
			} else if (app != null) {
				Log.i(TAG, "voice: open " + app);
				finishCapture();
				startActivity(getPackageManager().getLaunchIntentForPackage(app));
			} else if (quit) {
				Log.i(TAG, "voice: quit" + (switchOff ? ", screen off" : ""));
				if (switchOff) Screen.setOff(true);
				// Stopping other apps takes more than this app has, so the wake word service does it on this marker.
				if (!new File(new File(getIntent().getStringExtra("pcm")).getParent(), "wake.quit").createNewFile()) {
					Log.w(TAG, "voice: cannot ask to quit the other apps");
				}
				finishCapture();
				startActivity(new Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_HOME)
						.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK));
			} else if (channel >= 0) {
				Log.i(TAG, "voice: channel " + channel + " " + playlist.names.get(channel));
				finishCapture();
				startActivity(new Intent(this, MainActivity.class).putExtra("channel", channel));
			} else {
				finishCapture();
				beepFailure();
			}
		} catch (Exception e) {
			Log.e(TAG, "voice failed", e);
		} finally {
			runOnUiThread(this::finish);
		}
	}

	/**
	 * Only while the file /sdcard/voice-debug exists: keeps the last ten utterances (voice-0.wav to voice-9.wav in
	 * the app's private storage, readable with root) to find out why something was misheard.
	 */
	private void saveIfDebugging(byte[] speech) throws IOException {
		if (!new File(android.os.Environment.getExternalStorageDirectory(), "voice-debug").exists()) return;
		android.content.SharedPreferences prefs = getSharedPreferences("voice", MODE_PRIVATE);
		int n = prefs.getInt("n", 0);
		prefs.edit().putInt("n", (n + 1) % 10).apply();
		try (OutputStream out = new FileOutputStream(new File(getFilesDir(), "voice-" + n + ".wav"))) {
			byte[] h = new byte[44];
			String head = "RIFF\0\0\0\0WAVEfmt \u0010\0\0\0\u0001\0\u0001\0";
			for (int i = 0; i < head.length(); i++) h[i] = (byte) head.charAt(i);
			le(h, 4, 36 + speech.length);
			le(h, 24, RATE);
			le(h, 28, RATE * 2);
			h[32] = 2;
			h[34] = 16;
			System.arraycopy("data".getBytes(), 0, h, 36, 4);
			le(h, 40, speech.length);
			out.write(h);
			out.write(speech);
		}
	}

	private static void le(byte[] b, int at, int value) {
		for (int i = 0; i < 4; i++) b[at + i] = (byte) (value >> (8 * i));
	}

	/**
	 * A wake word service already has the audio after the wake word in a file (extra "pcm", 16 kHz mono PCM16):
	 * starting this activity and only then recording would miss what was said right after the wake word.
	 */
	private byte[] speech() throws IOException {
		if (!handsFree()) return record();
		String path = getIntent().getStringExtra("pcm");
		ByteArrayOutputStream pcm = new ByteArrayOutputStream();
		byte[] buffer = new byte[65536];
		try (InputStream in = new java.io.FileInputStream(path)) {
			for (int n; (n = in.read(buffer)) > 0;) pcm.write(buffer, 0, n);
		}
		return pcm.toByteArray();
	}

	/** The remote's microphone is the built-in input; the system prefers a USB headset's if one is plugged in. */
	private byte[] record() {
		int min = AudioRecord.getMinBufferSize(RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT);
		AudioRecord rec = new AudioRecord(MediaRecorder.AudioSource.VOICE_RECOGNITION, RATE,
				AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT, min * 4);
		for (AudioDeviceInfo d : ((AudioManager) getSystemService(AUDIO_SERVICE)).getDevices(AudioManager.GET_DEVICES_INPUTS)) {
			if (d.getType() == AudioDeviceInfo.TYPE_BUILTIN_MIC) rec.setPreferredDevice(d);
		}
		rec.startRecording();
		ByteArrayOutputStream pcm = new ByteArrayOutputStream();
		byte[] block = new byte[RATE / 10 * 2];
		boolean heard = false;
		int silent = 0;
		for (int i = 0; i < MAX_BLOCKS; i++) {
			int n = rec.read(block, 0, block.length);
			if (n <= 0) break;
			long sum = 0;
			for (int j = 0; j + 1 < n; j += 2) {
				short sample = (short) ((block[j] & 0xff) | (block[j + 1] << 8));
				sum += (long) sample * sample;
			}
			// While the button is held the remote streams (noise floor around 80 or more); after it is released
			// the level drops to nearly zero.
			boolean quiet = Math.sqrt(sum / (n / 2)) < SILENT_RMS;
			if (quiet) {
				if (heard && ++silent >= SILENT_BLOCKS) break;
			} else {
				heard = true;
				silent = 0;
			}
			pcm.write(block, 0, n);
		}
		rec.stop();
		rec.release();
		Log.i(TAG, "voice: recorded " + pcm.size() / 32 + " ms");
		return pcm.toByteArray();
	}

	private String recognize(Model model, byte[] speech, Playlist playlist) throws IOException, JSONException {
		// Only the words of the channel names (and "unknown") are allowed, which is far faster than free speech.
		java.util.List<String> words = ChannelMatcher.grammarWords(playlist.phrases, playlist.spoken);
		if (handsFree()) {
			words.addAll(ChannelMatcher.QUIT_WORDS);
			words.addAll(ChannelMatcher.VOLUME_WORDS);
			words.addAll(ChannelMatcher.SCREEN_WORDS);
			words.addAll(apps().keySet());
		}
		String grammar = new JSONArray(words).toString();
		try (Recognizer recognizer = new Recognizer(model, RATE, grammar)) {
			recognizer.setWords(true);
			recognizer.acceptWaveForm(speech, speech.length);
			return confidentWords(new JSONObject(recognizer.getFinalResult()));
		}
	}

	/** A doubtful word must not switch to the wrong channel: nothing happening is the better mistake. */
	private static String confidentWords(JSONObject result) throws JSONException {
		JSONArray words = result.optJSONArray("result");
		if (words == null) return "";
		StringBuilder text = new StringBuilder();
		for (int i = 0; i < words.length(); i++) {
			JSONObject word = words.getJSONObject(i);
			if (word.getDouble("conf") < MIN_CONFIDENCE) {
				Log.i(TAG, "voice: dropped \"" + word.getString("word") + "\" at " + word.getDouble("conf"));
			} else {
				text.append(text.length() == 0 ? "" : " ").append(word.getString("word"));
			}
		}
		return text.toString();
	}

	/** Voice commands that open an app, from /sdcard/voice-apps: one "phrase|package" per line. */
	private static java.util.Map<String, String> apps() throws IOException {
		java.util.Map<String, String> apps = new java.util.LinkedHashMap<>();
		File file = new File(android.os.Environment.getExternalStorageDirectory(), "voice-apps");
		if (!file.exists()) return apps;
		try (java.io.BufferedReader reader = new java.io.BufferedReader(new java.io.FileReader(file))) {
			for (String line; (line = reader.readLine()) != null;) {
				String[] parts = line.trim().split("\\|");
				if (parts.length == 2) apps.put(parts[0].trim().toLowerCase(java.util.Locale.ROOT), parts[1].trim());
			}
		}
		return apps;
	}

	private boolean handsFree() {
		return getIntent().getStringExtra("pcm") != null;
	}

	/** The wake word service keeps the speaker muted until this file is gone, at most for a few seconds. */
	private void finishCapture() {
		if (handsFree() && !new File(getIntent().getStringExtra("pcm")).delete()) {
			Log.w(TAG, "voice: cannot delete the capture");
		}
	}

	/** Without a remote in the hand nothing else shows that a wake word command failed. */
	private void beepFailure() {
		if (!handsFree()) return;
		SystemClock.sleep(UNMUTE_MS);
		ToneGenerator tone = new ToneGenerator(AudioManager.STREAM_MUSIC, ToneGenerator.MAX_VOLUME);
		tone.startTone(ToneGenerator.TONE_PROP_NACK);
		SystemClock.sleep(FAILURE_TONE_MS);
		tone.release();
	}

	private synchronized Model model() throws IOException {
		return model(this);
	}

	static synchronized Model model(Context context) throws IOException {
		if (cached == null) cached = loadModel(context);
		return cached;
	}

	static void preload(Context context) throws IOException {
		model(context);
	}

	private static Model loadModel(Context context) throws IOException {
		long start = SystemClock.elapsedRealtime();
		File dir = new File(context.getFilesDir(), "model");
		File ready = new File(context.getFilesDir(), "model.ready");
		if (!ready.exists()) {
			copyAsset(context, "model", dir);
			if (!ready.createNewFile()) throw new IOException("cannot mark the model as unpacked");
		}
		LibVosk.setLogLevel(LogLevel.WARNINGS);
		Model model = new Model(dir.getAbsolutePath());
		Log.i(TAG, "voice: model ready after " + (SystemClock.elapsedRealtime() - start) + " ms");
		return model;
	}

	private static void copyAsset(Context context, String asset, File target) throws IOException {
		String[] children = context.getAssets().list(asset);
		if (children == null || children.length == 0) {
			byte[] buffer = new byte[65536];
			try (InputStream in = context.getAssets().open(asset); OutputStream out = new FileOutputStream(target)) {
				for (int n; (n = in.read(buffer)) > 0;) out.write(buffer, 0, n);
			}
			return;
		}
		if (!target.mkdirs() && !target.isDirectory()) throw new IOException("cannot create " + target);
		for (String child : children) {
			// Android 7 lists a zip's directory entry as a child with an empty name, which would recurse into the same path
			if (!child.isEmpty()) copyAsset(context, asset + "/" + child, new File(target, child));
		}
	}
}
