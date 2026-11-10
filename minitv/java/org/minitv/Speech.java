package org.minitv;

import android.content.Context;
import android.media.AudioAttributes;
import android.media.AudioManager;
import android.media.MediaPlayer;
import android.media.audiofx.LoudnessEnhancer;
import android.os.Handler;
import android.os.Looper;
import android.os.Process;
import android.os.SystemClock;
import android.util.Log;
import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.Arrays;
import java.util.List;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.atomic.AtomicInteger;
import org.json.JSONException;
import org.json.JSONObject;

/**
 * Spoken text: rendered by the Edge read-aloud service (EdgeTts) and kept on disk, so that a text is fetched once and
 * then plays at once and without a network. One utterance at a time; the TV sound is lowered while it plays.
 */
final class Speech {
	static final int TEXT_MAX = 1000;
	// Least recently used files go when the cache is above either limit (the size limit is the setting speech_cache_mb).
	static final int CACHE_FILES_MAX = 2000;
	// Part of every cache key: raised whenever the way a text is rendered changes, so that old renders are not reused.
	static final int CACHE_VERSION = 2;
	// The cache must never fill the data partition: below this much free space a render is played once, not kept.
	static final long FREE_BYTES_MIN = 300L * 1024 * 1024;
	private static final long LOW_SPACE_REPORT_MS = 3600 * 1000L;
	// Up to this pause the TV stays lowered between the repeats of a series; after a longer one it comes back up for the
	// pause and goes down again shortly before the next play (the fade down takes 150 ms).
	static final int DUCK_HOLD_MAX_PAUSE_S = 2;
	private static final int DUCK_AHEAD_MS = 250;
	private static long lowSpaceReported = -LOW_SPACE_REPORT_MS;

	private static Context app;
	private static File dir;
	private static final Handler MAIN = new Handler(Looper.getMainLooper());
	// A say must not wait behind a long render or a prefetch: each gets its own thread, stale ones are dropped.
	private static final ExecutorService SAY = Executors.newCachedThreadPool();
	private static final ExecutorService PREFETCH = Executors.newSingleThreadExecutor(r -> new Thread(() -> {
		Process.setThreadPriority(Process.THREAD_PRIORITY_BACKGROUND);
		r.run();
	}, "speech-prefetch"));
	private static final AtomicInteger GENERATION = new AtomicInteger();
	private static final Object CACHE_LOCK = new Object();
	// Main thread only.
	private static MediaPlayer player;
	private static LoudnessEnhancer boost;
	// A render that could not be cached, played once from this file and then deleted.
	private static File temporary;
	// The plays of the current series that are still to come, and the silence before each.
	private static int repeatsLeft, pauseSeconds;
	private static final Runnable REPEAT = Speech::repeat;
	private static final Runnable DUCK_AGAIN = () -> setDucked(true);
	// The generation of the series that is being rendered or played (also in its pauses), 0 when there is none.
	private static volatile int busyGeneration;
	// The generation of the station name being rendered or spoken, 0 when there is none.
	private static volatile int announcement;
	private static volatile boolean speaking, ducked, inCall, allowedByConfig = true;
	// Called on the main thread when a series starts or ends, or the TV is to be lowered or raised.
	static volatile Runnable onChange;

	/** Told on the main thread when a series has ended. */
	interface Ended {
		/**
		 * rendered is false when the text could not be rendered (no cache and no network, or the service refused it);
		 * true when it was spoken to the end, or stopped or replaced (by a key, cmd/say-stop, another say, a call).
		 */
		void ended(boolean rendered);
	}

	// The listener of the current series.
	private static final Object ENDED_LOCK = new Object();
	private static Ended ended;

	/** What a prefetch did. */
	interface PrefetchResult {
		void done(int rendered, int cached, int failed);
	}

	private Speech() {
	}

	static synchronized void init(Context context) {
		if (app != null) return;
		app = context.getApplicationContext();
		dir = new File(app.getFilesDir(), "tts");
		dir.mkdirs();
		File marker = new File(dir, "version-" + CACHE_VERSION);
		// A render that was cut off by the process dying leaves its temporary file behind.
		PREFETCH.execute(() -> {
			File[] temps = dir.listFiles((d, name) -> name.contains(".tmp"));
			if (temps != null) for (File f : temps) f.delete();
			// Files of an older way of rendering (the multilingual voice that read names in French) are never played again.
			if (!marker.exists()) {
				File[] old = dir.listFiles((d, name) -> name.endsWith(".mp3") || name.startsWith("version-"));
				if (old != null) for (File f : old) f.delete();
				try {
					if (!marker.createNewFile()) Log.w("minitv", "speech: cache marker not created");
				} catch (IOException e) {
					Log.w("minitv", "speech: cache marker: " + e);
				}
				Log.i("minitv", "speech: cache cleared for version " + CACHE_VERSION);
			}
		});
	}

	/** True while audio of a series plays or pauses between its repeats. */
	static boolean speaking() {
		return speaking;
	}

	/** True while the TV is to be lowered. */
	static boolean ducked() {
		return ducked;
	}

	/** True from a say until its series has ended, failed or was stopped (while it is rendered, too). */
	static boolean busy() {
		return busy(busyGeneration);
	}

	/** The same for the series that say returned this generation for. */
	static boolean busy(int generation) {
		return generation != 0 && generation == busyGeneration && generation == GENERATION.get();
	}

	/** mqtt.conf speech=off: no speech at all (messages that were to be heard are shown instead). */
	static void setAllowedByConfig(boolean allowed) {
		allowedByConfig = allowed;
	}

	/** Why nothing can be spoken now, or null. */
	static String refusal() {
		if (!Settings.speech()) return "speech is off (setting speech)";
		if (!allowedByConfig) return "speech is off (speech=off in mqtt.conf)";
		if (inCall) return "not during a video call";
		return null;
	}

	/**
	 * Speaks the text repeat times with pause seconds between, replacing whatever is spoken; returns at once with the
	 * generation of this series (0 when nothing can be spoken, and then ended is not called).
	 */
	static int say(String text, String voice, int repeat, int pause, Ended ended) {
		return start(text, voice, repeat, pause, ended, true);
	}

	/**
	 * Speaks a station name once. Unlike a say it is not a series: busy() stays false, so no key is taken for it, and
	 * it never replaces a running series. Nothing happens when speech is not possible.
	 */
	static void announce(String name) {
		if (busy()) return;
		Log.i("minitv", "speech: announce " + name);
		start(name, Settings.speechVoice(), 1, 0, null, false);
	}

	/** Stops the station name (also one still being rendered, which is then dropped); a series goes on. */
	static void stopAnnouncement() {
		int a = announcement;
		if (a != 0 && a == GENERATION.get()) stop();
	}

	private static int start(String text, String voice, int repeat, int pause, Ended listener, boolean series) {
		String t = text == null ? "" : text.trim();
		if (t.isEmpty() || refusal() != null) return 0;
		String rate = Settings.speechRate();
		int generation = GENERATION.incrementAndGet();
		if (series) {
			endSeries(true);
			busyGeneration = generation;
			synchronized (ENDED_LOCK) {
				ended = listener;
			}
		} else {
			announcement = generation;
		}
		MAIN.post(() -> {
			release();
			changed();
		});
		SAY.execute(() -> {
			long start = SystemClock.elapsedRealtime();
			Clip clip = obtain(t, voice, rate);
			if (clip == null) {
				MAIN.post(() -> {
					if (generation != GENERATION.get()) return;
					busyGeneration = 0;
					changed();
					endSeries(false);
				});
				return;
			}
			Log.i("minitv", "speech: ready after " + (SystemClock.elapsedRealtime() - start) + " ms");
			MAIN.post(() -> {
				if (generation == GENERATION.get() && !inCall) play(clip, repeat, pause);
				else if (clip.once) clip.file.delete();
			});
		});
		return generation;
	}

	/** Stops whatever is spoken, rendered or waiting in a pause. */
	static void stop() {
		GENERATION.incrementAndGet();
		busyGeneration = 0;
		endSeries(true);
		MAIN.post(() -> {
			release();
			changed();
		});
	}

	/** No speech while a video call is open: it would talk over the people in it. */
	static void setInCall(boolean active) {
		inCall = active;
		if (active) stop();
	}

	/** Renders and caches the texts with the voice and rate of the settings, without playing them. */
	static void prefetch(List<String> texts, PrefetchResult result) {
		String voice = Settings.speechVoice(), rate = Settings.speechRate();
		PREFETCH.execute(() -> {
			int rendered = 0, cached = 0, failed = 0;
			for (String text : texts) {
				String t = text.trim();
				if (t.isEmpty()) continue;
				if (cached(t, voice, rate) != null) {
					cached++;
					continue;
				}
				Clip clip = obtain(t, voice, rate);
				if (clip != null && !clip.once) {
					rendered++;
				} else {
					failed++;
					if (clip != null) clip.file.delete();
				}
			}
			Log.i("minitv", "speech prefetch: " + rendered + " rendered, " + cached + " cached, " + failed + " failed");
			if (result != null) result.done(rendered, cached, failed);
		});
	}

	/** {"files":n,"bytes":n} of the cache. */
	static JSONObject cacheStats() throws JSONException {
		long bytes = 0;
		File[] files = mp3s();
		for (File f : files) bytes += f.length();
		return new JSONObject().put("files", files.length).put("bytes", bytes);
	}

	private static File[] mp3s() {
		File[] files = dir == null ? null : dir.listFiles((d, name) -> name.endsWith(".mp3"));
		return files == null ? new File[0] : files;
	}

	private static File file(String text, String voice, String rate) {
		try {
			byte[] sha = MessageDigest.getInstance("SHA-256").digest(("v" + CACHE_VERSION + "|" + voice + "|" + rate + "|" + text).getBytes(StandardCharsets.UTF_8));
			return new File(dir, EdgeTts.hex(sha).toLowerCase(java.util.Locale.ROOT) + ".mp3");
		} catch (NoSuchAlgorithmException e) {
			throw new IllegalStateException(e);
		}
	}

	/** The cached file, marked as just used; null when it is not cached. */
	private static File cached(String text, String voice, String rate) {
		File f = file(text, voice, rate);
		if (!f.isFile() || f.length() == 0) return null;
		f.setLastModified(System.currentTimeMillis());
		return f;
	}

	/** Rendered audio: the cached file, or a file that goes after one play when there was no room to keep it. */
	private static final class Clip {
		final File file;
		final boolean once;

		Clip(File file, boolean once) {
			this.file = file;
			this.once = once;
		}
	}

	/** The cached file, rendered first when needed; null (and an error event) when that fails. */
	private static Clip obtain(String text, String voice, String rate) {
		File hit = cached(text, voice, rate);
		if (hit != null) {
			Log.i("minitv", "speech: cache hit " + hit.getName());
			return new Clip(hit, false);
		}
		File f = file(text, voice, rate);
		long start = SystemClock.elapsedRealtime();
		byte[] mp3;
		try {
			// An installed Ivona engine speaks on the device; the online service only when there is none.
			mp3 = IvonaTts.available() && IvonaTts.isIvonaVoice(voice) ? IvonaTts.render(app, text, voice) : EdgeTts.render(text, voice, rate);
		} catch (EdgeTts.RejectedException e) {
			MqttService.error("speech", "rejected by the service: " + e.getMessage(), "rejected");
			return null;
		} catch (IOException e) {
			MqttService.error("speech", "not rendered (network or service down): " + e, "network");
			return null;
		} catch (RuntimeException e) {
			MqttService.error("speech", "not rendered: " + e, "failed");
			return null;
		}
		Log.i("minitv", "speech: rendered " + text.length() + " characters to " + mp3.length + " bytes in "
				+ (SystemClock.elapsedRealtime() - start) + " ms");
		boolean room = dir.getUsableSpace() - mp3.length >= FREE_BYTES_MIN;
		if (!room) reportLowSpace();
		File temp = new File(dir, f.getName() + ".tmp" + Thread.currentThread().getId());
		try (FileOutputStream out = new FileOutputStream(temp)) {
			out.write(mp3);
			out.getFD().sync();
		} catch (IOException e) {
			temp.delete();
			MqttService.error("speech", "cache not written: " + e, "cache");
			return null;
		}
		if (!room) return new Clip(temp, true);
		synchronized (CACHE_LOCK) {
			if (!temp.renameTo(f)) {
				temp.delete();
				MqttService.error("speech", "cache not written: rename failed", "cache");
				return null;
			}
			evict(f);
		}
		return new Clip(f, false);
	}

	private static void reportLowSpace() {
		long now = SystemClock.elapsedRealtime();
		synchronized (CACHE_LOCK) {
			if (now - lowSpaceReported < LOW_SPACE_REPORT_MS) return;
			lowSpaceReported = now;
		}
		MqttService.error("speech", "not cached: less than " + FREE_BYTES_MIN / (1024 * 1024) + " MB free on the data partition",
				"low-space");
	}

	/** Applies the cache limits now (after speech_cache_mb was lowered). */
	static void trim() {
		PREFETCH.execute(() -> {
			synchronized (CACHE_LOCK) {
				evict(null);
			}
		});
	}

	private static void evict(File keep) {
		File[] files = mp3s();
		long[] modified = new long[files.length];
		Integer[] order = new Integer[files.length];
		long bytes = 0, maxBytes = Settings.speechCacheMb() * 1024L * 1024;
		for (int i = 0; i < files.length; i++) {
			// lastModified is read once: it changes under the sort when a file is used meanwhile.
			modified[i] = files[i].lastModified();
			order[i] = i;
			bytes += files[i].length();
		}
		Arrays.sort(order, (a, b) -> Long.compare(modified[a], modified[b]));
		int count = files.length;
		for (int i = 0; i < order.length && (count > CACHE_FILES_MAX || bytes > maxBytes); i++) {
			File f = files[order[i]];
			if (f.equals(keep)) continue;
			long length = f.length();
			if (f.delete()) {
				count--;
				bytes -= length;
				Log.i("minitv", "speech: evicted " + f.getName());
			}
		}
	}

	private static void play(Clip clip, int repeat, int pause) {
		release();
		repeatsLeft = repeat - 1;
		pauseSeconds = pause;
		MediaPlayer p = new MediaPlayer();
		try {
			p.setAudioAttributes(new AudioAttributes.Builder().setUsage(AudioAttributes.USAGE_MEDIA)
					.setContentType(AudioAttributes.CONTENT_TYPE_SPEECH).build());
			try (FileInputStream in = new FileInputStream(clip.file)) {
				p.setDataSource(in.getFD());
			}
			p.setOnPreparedListener(prepared -> {
				Log.i("minitv", "speech: playing, " + repeatsLeft + " more");
				prepared.start();
			});
			p.setOnCompletionListener(done -> {
				if (done != player) return;
				if (repeatsLeft <= 0) {
					finish();
					return;
				}
				Log.i("minitv", "speech: pause " + pauseSeconds + " s");
				// A short pause keeps the TV lowered: the series is one announcement. A long one gives the programme back.
				if (pauseSeconds > DUCK_HOLD_MAX_PAUSE_S) {
					setDucked(false);
					MAIN.postDelayed(DUCK_AGAIN, pauseSeconds * 1000L - DUCK_AHEAD_MS);
				}
				MAIN.postDelayed(REPEAT, pauseSeconds * 1000L);
			});
			p.setOnErrorListener((failed, what, extra) -> {
				MqttService.error("speech", "playback failed (" + what + ", " + extra + ")", "playback");
				if (failed == player) finish();
				return true;
			});
			p.prepareAsync();
		} catch (IOException | RuntimeException e) {
			p.release();
			if (clip.once) clip.file.delete();
			MqttService.error("speech", "playback failed: " + e, "playback");
			finish();
			return;
		}
		player = p;
		applyVolume();
		if (clip.once) temporary = clip.file;
		attachBoost(p.getAudioSessionId());
		speaking = true;
		setDucked(true);
		changed();
	}

	/** The series is over: everything goes, and the listeners hear of it. */
	private static void finish() {
		release();
		busyGeneration = 0;
		changed();
		endSeries(true);
	}

	/** Tells the listener of the current series (once) that it has ended. */
	private static void endSeries(boolean rendered) {
		Ended listener;
		synchronized (ENDED_LOCK) {
			listener = ended;
			ended = null;
		}
		if (listener != null) MAIN.post(() -> listener.ended(rendered));
	}

	/** The voices come out around -21 LUFS, quieter than the TV with its own boost: lift them by speech_boost_db. */
	private static void attachBoost(int session) {
		int db = Settings.speechBoostDb();
		if (db == 0) return;
		try {
			boost = new LoudnessEnhancer(session);
			boost.setTargetGain(db * 100);
			boost.setEnabled(true);
		} catch (RuntimeException e) {
			// No effect engine: speak without the lift rather than not at all.
			boost = null;
			MqttService.error("speech", "no loudness boost: " + e, "boost");
		}
	}

	private static void repeat() {
		if (player == null) return;
		repeatsLeft--;
		Log.i("minitv", "speech: playing, " + repeatsLeft + " more");
		player.seekTo(0);
		player.start();
	}

	/**
	 * The speech follows the system volume like the TV does (see Volume), scaled by speech_volume. Called on the main
	 * thread whenever the volume, mute or the output changes.
	 */
	static void applyVolume() {
		if (player == null) return;
		float v = Volume.gain((AudioManager) app.getSystemService(Context.AUDIO_SERVICE)) * Settings.speechVolume() / 100f;
		player.setVolume(v, v);
	}

	/** Ends the playback of the current series; whoever calls it says whether the series is over (finish, stop). */
	private static void release() {
		MAIN.removeCallbacks(REPEAT);
		MAIN.removeCallbacks(DUCK_AGAIN);
		repeatsLeft = 0;
		if (boost != null) {
			boost.release();
			boost = null;
		}
		if (player != null) {
			player.release();
			player = null;
		}
		if (temporary != null) {
			temporary.delete();
			temporary = null;
		}
		if (speaking) Log.i("minitv", "speech: silent");
		speaking = false;
		setDucked(false);
	}

	private static void setDucked(boolean on) {
		if (ducked == on) return;
		ducked = on;
		if (on) Log.i("minitv", "speech: speaking");
		changed();
	}

	private static void changed() {
		Runnable listener = onChange;
		if (listener != null) listener.run();
		MqttService.speakingChanged();
	}
}
