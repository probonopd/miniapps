package org.minitv;

import android.os.Process;
import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.Locale;
import java.util.TimeZone;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import org.json.JSONArray;
import org.json.JSONObject;

/**
 * The optional programme guide: what is on now, from the public Zapp backend of the MediathekView project
 * (https://github.com/mediathekview/zapp-backend). One small request per look, only for channels whose
 * #EXTINF line has zapp-id="...".
 */
final class Guide {
	private static final String URL = "https://api.zapp.mediathekview.de/v1/shows/";
	private static final int TIMEOUT_MS = 8000;
	// One background thread at low priority: a request never competes with the player for the CPU, and requests
	// never run in parallel.
	private static final ExecutorService WORKER = Executors.newSingleThreadExecutor(task -> new Thread(() -> {
		Process.setThreadPriority(Process.THREAD_PRIORITY_BACKGROUND);
		task.run();
	}, "guide"));

	/** What is on: the time span, the title and the subtitle (empty if none). */
	static final class Show {
		final String span, title, subtitle;
		// When the programme ends (milliseconds since 1970): the answer is worth keeping until then.
		final long endMs;

		Show(long endMs, String span, String title, String subtitle) {
			this.endMs = endMs;
			this.span = span;
			this.title = title;
			this.subtitle = subtitle;
		}

		String line() {
			return span + "  " + title;
		}

		String full() {
			return subtitle.isEmpty() ? line() : line() + "\n" + subtitle;
		}
	}

	interface Callback {
		/** The programme, or null when it could not be had (no network, server down, unexpected answer). */
		void onShow(Show show);
	}

	/** Fetches the current programme on a worker thread. */
	static void now(String zappId, Callback callback) {
		if (zappId == null) return;
		WORKER.execute(() -> callback.onShow(fetch(zappId)));
	}

	/** A text of the answer, empty when absent or JSON null (org.json would turn that into the word "null"). */
	private static String text(JSONObject object, String key) {
		if (object.isNull(key)) return "";
		String value = object.optString(key, "").trim();
		return value.equalsIgnoreCase("null") ? "" : value;
	}

	private static Show fetch(String zappId) {
		HttpURLConnection connection = null;
		try {
			connection = (HttpURLConnection) new URL(URL + zappId).openConnection();
			connection.setConnectTimeout(TIMEOUT_MS);
			connection.setReadTimeout(TIMEOUT_MS);
			StringBuilder body = new StringBuilder();
			try (InputStream in = connection.getInputStream()) {
				byte[] buffer = new byte[4096];
				for (int n; (n = in.read(buffer)) > 0;) body.append(new String(buffer, 0, n, "UTF-8"));
			}
			JSONArray shows = new JSONObject(body.toString()).optJSONArray("shows");
			if (shows == null || shows.length() == 0) return null;
			JSONObject show = shows.getJSONObject(0);
			SimpleDateFormat iso = new SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss.SSS'Z'", Locale.US);
			iso.setTimeZone(TimeZone.getTimeZone("UTC"));
			SimpleDateFormat clock = new SimpleDateFormat("HH:mm", Locale.GERMANY);
			Date start = iso.parse(show.getString("startTime"));
			Date end = iso.parse(show.getString("endTime"));
			String title = text(show, "title");
			if (title.isEmpty()) return null;
			return new Show(end.getTime(), clock.format(start) + "-" + clock.format(end), title, text(show, "subtitle"));
		} catch (Exception e) {
			// No network or an unexpected answer: the guide is optional, so show nothing.
			return null;
		} finally {
			if (connection != null) connection.disconnect();
		}
	}
}
