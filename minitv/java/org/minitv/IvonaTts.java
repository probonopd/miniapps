package org.minitv;

import android.content.Context;
import android.content.Intent;
import android.content.pm.ResolveInfo;
import android.os.Bundle;
import android.speech.tts.TextToSpeech;
import android.speech.tts.Voice;
import android.util.Log;
import android.speech.tts.UtteranceProgressListener;
import java.io.File;
import java.io.IOException;
import java.io.ByteArrayOutputStream;
import java.io.FileInputStream;
import java.io.InputStream;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;

/**
 * Speech from the Ivona text-to-speech engine, when the device has one installed (an Echo Show does): rendered on the
 * device into an audio file, so that the cache and the player of Speech work unchanged and no network is needed.
 */
final class IvonaTts {
	private static final long TIMEOUT_S = 30;
	private static TextToSpeech tts;
	private static String enginePackage;
	private static boolean probed;
	private static volatile List<String[]> voiceList;

	private IvonaTts() {
	}

	/** The package of the installed Ivona engine, or null. */
	static synchronized String engine(Context context) {
		if (probed) return enginePackage;
		probed = true;
		for (ResolveInfo r : context.getPackageManager().queryIntentServices(new Intent(TextToSpeech.Engine.INTENT_ACTION_TTS_SERVICE), 0)) {
			if (r.serviceInfo.packageName.startsWith("com.ivona.")) {
				enginePackage = r.serviceInfo.packageName;
				break;
			}
		}
		return enginePackage;
	}

	/** True when an Ivona engine is installed (engine() has been asked once). */
	static boolean available() {
		return enginePackage != null;
	}

	/** True when the voice is one of the engine's (the online service's voice names look different). */
	static boolean isIvonaVoice(String voice) {
		return !voice.matches(EdgeTts.VOICE_PATTERN);
	}

	/** The voices the engine offers as {name, label}, or null as long as it has not been asked. */
	static List<String[]> voices() {
		return voiceList;
	}

	/** Starts the engine in the background to learn its voices; done runs when they are known. Slow once. */
	static void load(Context context, Runnable done) {
		if (engine(context) == null || voiceList != null) return;
		Thread t = new Thread(() -> {
			try {
				ensure(context);
				done.run();
			} catch (IOException e) {
				MqttService.error("speech", "Ivona engine not started: " + e, "failed");
			}
		}, "ivona-voices");
		t.start();
	}

	private static void listVoices() {
		List<String[]> list = new ArrayList<>();
		for (Voice v : tts.getVoices()) {
			if (v.isNetworkConnectionRequired()) continue;
			Locale l = v.getLocale();
			String name = v.getName().substring(0, 1).toUpperCase(Locale.ROOT) + v.getName().substring(1);
			list.add(new String[] { v.getName(), name + " (" + new Locale(l.getLanguage(), l.getCountry()).getDisplayName(Locale.GERMAN) + ")" });
		}
		java.util.Collections.sort(list, (a, b) -> a[0].compareTo(b[0]));
		voiceList = list;
		Log.i("minitv", "ivona voices: " + list.size());
		for (String[] v : list) Log.i("minitv", "ivona voice " + v[0] + " (" + v[1] + ")");
	}

	private static TextToSpeech ensure(Context context) throws IOException {
		String engine = engine(context);
		if (engine == null) throw new IOException("no Ivona engine installed");
		synchronized (IvonaTts.class) {
			if (tts != null) return tts;
			CountDownLatch ready = new CountDownLatch(1);
			int[] status = {TextToSpeech.ERROR};
			TextToSpeech created = new TextToSpeech(context, s -> {
				status[0] = s;
				ready.countDown();
			}, engine);
			try {
				if (!ready.await(TIMEOUT_S * 4, TimeUnit.SECONDS) || status[0] != TextToSpeech.SUCCESS) {
					created.shutdown();
					throw new IOException("Ivona engine did not start");
				}
			} catch (InterruptedException e) {
				created.shutdown();
				Thread.currentThread().interrupt();
				throw new IOException("interrupted");
			}
			tts = created;
			listVoices();
			return tts;
		}
	}

	/** The text as audio (WAV) in this voice ("ivona" is the German one). Blocks; call it from a worker thread. */
	static synchronized byte[] render(Context context, String text, String voice) throws IOException {
		TextToSpeech engine = ensure(context);
		if ("ivona".equals(voice)) {
			int language = engine.setLanguage(Locale.GERMANY);
			if (language == TextToSpeech.LANG_MISSING_DATA || language == TextToSpeech.LANG_NOT_SUPPORTED) {
				throw new IOException("Ivona engine has no German voice");
			}
		} else {
			Voice chosen = null;
			for (Voice v : engine.getVoices()) if (v.getName().equals(voice)) chosen = v;
			if (chosen == null) throw new IOException("Ivona engine has no voice " + voice);
			if (engine.setVoice(chosen) != TextToSpeech.SUCCESS) throw new IOException("Ivona voice " + voice + " not usable");
		}
		File out = new File(context.getCacheDir(), "ivona.wav");
		out.delete();
		CountDownLatch done = new CountDownLatch(1);
		boolean[] failed = {false};
		engine.setOnUtteranceProgressListener(new UtteranceProgressListener() {
			@Override
			public void onStart(String id) {
			}

			@Override
			public void onDone(String id) {
				done.countDown();
			}

			// Abstract in UtteranceProgressListener: Android 5 and older call only this one.
			@Override
			@SuppressWarnings("deprecation")
			public void onError(String id) {
				failed[0] = true;
				done.countDown();
			}
		});
		if (engine.synthesizeToFile(text, new Bundle(), out, "minitv") != TextToSpeech.SUCCESS) {
			throw new IOException("Ivona engine refused the text");
		}
		try {
			if (!done.await(TIMEOUT_S, TimeUnit.SECONDS)) throw new IOException("Ivona engine timed out");
		} catch (InterruptedException e) {
			Thread.currentThread().interrupt();
			throw new IOException("interrupted");
		}
		if (failed[0] || !out.isFile() || out.length() == 0) throw new IOException("Ivona engine produced no audio");
		try (InputStream in = new FileInputStream(out)) {
			ByteArrayOutputStream all = new ByteArrayOutputStream();
			byte[] buffer = new byte[16384];
			for (int n; (n = in.read(buffer)) > 0;) all.write(buffer, 0, n);
			return all.toByteArray();
		} finally {
			out.delete();
		}
	}
}
