package org.minitv;

import android.content.Context;
import android.content.SharedPreferences;
import java.util.List;
import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

/**
 * The settings that can be changed while the TV runs, and over MQTT (<base>/config, <base>/set/config and one
 * topic per setting). They are kept in the app's private preferences.
 *
 * footer: the programme text and the clock on a bar at the bottom after a channel change (off by default).
 * footer_seconds: how long it stays.
 * header: the channel name at the top after a channel change (off by default); header_mode: "picture" (until the first
 * picture is up, and as long as the stream does not start) or "seconds" (header_seconds); header_number: with the
 * channel number (off by default).
 * schedule: messages that come up by themselves (see checkSchedule), a JSON array.
 * clock: the time, shown permanently at the top right (off by default).
 * guide: the programme guide (channel list and programme texts); the playlist header decides until it is set.
 * jitsi_room: the family's video call room, joined with the green key (empty: the key does nothing).
 * jitsi_max_minutes: a call ends by itself after this long (0: never).
 * speech: spoken text at all (on by default); speech_voice, speech_rate: how it sounds; speech_volume: 0 to 100;
 * speech_boost_db: loudness lift of the speech; speech_duck_percent: the TV volume while speech plays;
 * speech_cache_mb: size limit of the speech cache; speech_repeat, speech_pause: how often a text is spoken and the
 * silence between, where a command or schedule entry does not say.
 * no_picture_seconds: the red screen after this long without a picture, the yellow one (no internet) after twice that.
 * speak_channel: the station name is spoken after a channel change (off by default).
 * message_mode: how pushed and scheduled messages are presented where they do not say (text, speech or both);
 * message_seconds: how long their screen part stays where they do not say (0: until a key is pressed).
 */
final class Settings {
	static final int SECONDS_MIN = 1, SECONDS_MAX = 60;
	static final int CALL_MINUTES_MAX = 1440;
	static final String SPEECH_RATE = "-5%";
	static final int SPEECH_CACHE_MB_MAX = 200;
	// The number settings: name, lowest, highest, default, unit (for the message when a value is wrong).
	private static final Object[][] NUMBERS = {
			{ "footer_seconds", SECONDS_MIN, SECONDS_MAX, 6, "seconds" },
			{ "header_seconds", SECONDS_MIN, SECONDS_MAX, 3, "seconds" },
			{ "jitsi_max_minutes", 0, CALL_MINUTES_MAX, 120, "minutes" },
			{ "message_seconds", 0, 3600, 0, "seconds" },
			{ "no_picture_seconds", 5, 120, 20, "seconds" },
			{ "speech_volume", 0, 100, 100, "percent" },
			{ "speech_boost_db", 0, 15, 10, "dB" },
			{ "speech_repeat", 1, 10, 1, "times" },
			{ "speech_pause", 0, 60, 5, "seconds" },
			{ "speech_duck_percent", 0, 100, 8, "percent" },
			{ "speech_cache_mb", 10, SPEECH_CACHE_MB_MAX, SPEECH_CACHE_MB_MAX, "MB" } };
	private static SharedPreferences prefs;
	// Called (on any thread) after a setting changed, so that the screen can follow.
	static volatile Runnable onChange;

	private Settings() {
	}

	static synchronized void init(Context context) {
		if (prefs != null) return;
		prefs = context.getApplicationContext().getSharedPreferences("settings", Context.MODE_PRIVATE);
		IvonaTts.engine(context);
		// speak_messages became message_mode: keep what was chosen.
		if (prefs.contains("speak_messages")) {
			SharedPreferences.Editor e = prefs.edit();
			if (!prefs.contains("message_mode")) e.putString("message_mode", prefs.getBoolean("speak_messages", false) ? MessageSpec.BOTH : MessageSpec.TEXT);
			e.remove("speak_messages").apply();
		}
	}

	static boolean footer() {
		return prefs.getBoolean("footer", false);
	}

	static int footerSeconds() {
		return number("footer_seconds");
	}

	static boolean clock() {
		return prefs.getBoolean("clock", false);
	}

	static boolean header() {
		return prefs.getBoolean("header", false);
	}

	static boolean headerUntilPicture() {
		return "picture".equals(prefs.getString("header_mode", "picture"));
	}

	static int headerSeconds() {
		return number("header_seconds");
	}

	/** The scheduled messages, an array of {"id","text","from":"21:00","to":"05:00","every":30,"enabled", and the keys of MessageSpec}. */
	static JSONArray schedule() {
		try {
			return new JSONArray(prefs.getString("schedule", "[]"));
		} catch (JSONException e) {
			return new JSONArray();
		}
	}

	/** Null if the schedule is fine, else what is wrong with it. */
	static String checkSchedule(JSONArray entries) {
		if (entries.length() > 20) return "schedule: 20 entries at most";
		for (int i = 0; i < entries.length(); i++) {
			JSONObject o = entries.optJSONObject(i);
			if (o == null) return "schedule[" + i + "]: not an object";
			String text = o.optString("text", "");
			if (text.trim().isEmpty() || text.length() > 500) return "schedule[" + i + "]: text 1 to 500 characters";
			if (minutes(o.optString("from", "")) < 0) return "schedule[" + i + "]: from as HH:mm";
			if (minutes(o.optString("to", "")) < 0) return "schedule[" + i + "]: to as HH:mm";
			int every = o.optInt("every", 0);
			if (every < 1 || every > 1440) return "schedule[" + i + "]: every 1 to 1440 minutes";
			if (o.has("enabled") && !(o.opt("enabled") instanceof Boolean)) return "schedule[" + i + "]: enabled true or false";
			String presentation = MessageSpec.check(o);
			if (presentation != null) return "schedule[" + i + "]: " + presentation;
		}
		return null;
	}

	static int speechRepeat() {
		return number("speech_repeat");
	}

	static int speechPause() {
		return number("speech_pause");
	}

	/** "HH:mm" as minutes since midnight, or -1. */
	static int minutes(String text) {
		if (!text.matches("([01]?\\d|2[0-3]):[0-5]\\d")) return -1;
		String[] p = text.split(":");
		return Integer.parseInt(p[0]) * 60 + Integer.parseInt(p[1]);
	}

	static boolean headerNumber() {
		return prefs.getBoolean("header_number", false);
	}

	/** The programme guide: what was set, else what the playlist header says. */
	static boolean guide(boolean playlistDefault) {
		return prefs.getBoolean("guide", playlistDefault);
	}

	static String jitsiRoom() {
		return prefs.getString("jitsi_room", "");
	}

	static int jitsiMaxMinutes() {
		return number("jitsi_max_minutes");
	}

	private static Object[] numberSpec(String key) {
		for (Object[] n : NUMBERS) if (n[0].equals(key)) return n;
		return null;
	}

	private static int number(String key) {
		return prefs.getInt(key, (Integer) numberSpec(key)[3]);
	}

	static int numberMin(String key) {
		return (Integer) numberSpec(key)[1];
	}

	static int numberMax(String key) {
		return (Integer) numberSpec(key)[2];
	}

	static boolean speech() {
		return prefs.getBoolean("speech", true);
	}

	static String speechVoice() {
		// An installed Ivona engine is preferred; "ivona" is its German voice.
		return prefs.getString("speech_voice", IvonaTts.available() ? "ivona" : EdgeTts.DEFAULT_VOICE);
	}

	/** Fallback for the device name: the name= of mqtt.conf, set by the MQTT service. */
	static volatile String defaultName = "TV";

	/** What the TV is called: in Home Assistant and in video calls. */
	static String deviceName() {
		String n = prefs.getString("device_name", "");
		return n.isEmpty() ? defaultName : n;
	}

	/** A voice the app can speak with: an online voice, or one of an installed Ivona engine. */
	static boolean validVoice(String voice) {
		if (voice.matches(EdgeTts.VOICE_PATTERN)) return true;
		if (!IvonaTts.available()) return false;
		if (voice.equals("ivona")) return true;
		List<String[]> known = IvonaTts.voices();
		if (known == null) return voice.matches("[A-Za-z0-9_-]{3,40}");
		for (String[] v : known) if (v[0].equals(voice)) return true;
		return false;
	}

	static String speechRate() {
		return prefs.getString("speech_rate", SPEECH_RATE);
	}

	static int speechVolume() {
		return number("speech_volume");
	}

	static int speechBoostDb() {
		return number("speech_boost_db");
	}

	static int speechDuckPercent() {
		return number("speech_duck_percent");
	}

	static int speechCacheMb() {
		return number("speech_cache_mb");
	}

	/** The red screen comes when a selected channel has had no picture for this long (the yellow one after twice that). */
	static int noPictureSeconds() {
		return number("no_picture_seconds");
	}

	/** The station name is spoken after a channel change. */
	static boolean speakChannel() {
		return prefs.getBoolean("speak_channel", false);
	}

	static String messageMode() {
		return prefs.getString("message_mode", MessageSpec.TEXT);
	}

	static int messageSeconds() {
		return number("message_seconds");
	}

	/** The texts of the enabled schedule entries that will be read aloud. */
	static java.util.List<String> spokenSchedule() {
		java.util.List<String> texts = new java.util.ArrayList<>();
		JSONArray entries = schedule();
		for (int i = 0; i < entries.length(); i++) {
			JSONObject o = entries.optJSONObject(i);
			if (o != null && o.optBoolean("enabled", true) && speech() && MessageSpec.of(o).voice()) texts.add(o.optString("text", ""));
		}
		return texts;
	}

	/** [[name, label], ...] of the installed Ivona engine; empty without one or while it is still starting. */
	private static JSONArray ivonaVoices() {
		JSONArray a = new JSONArray();
		List<String[]> voices = IvonaTts.voices();
		if (voices != null) for (String[] v : voices) a.put(new JSONArray().put(v[0]).put(v[1]));
		return a;
	}

	static JSONObject toJson(boolean playlistGuide) throws JSONException {
		return new JSONObject().put("footer", footer()).put("footer_seconds", footerSeconds()).put("header", header())
				.put("header_mode", prefs.getString("header_mode", "picture")).put("header_seconds", headerSeconds())
				.put("header_number", headerNumber()).put("clock", clock()).put("schedule", schedule())
				.put("guide", guide(playlistGuide)).put("jitsi_room", jitsiRoom()).put("jitsi_max_minutes", jitsiMaxMinutes())
				.put("device_name", deviceName()).put("speech", speech()).put("speech_voice", speechVoice()).put("ivona", IvonaTts.available()).put("ivona_voices", ivonaVoices()).put("speech_rate", speechRate())
				.put("speech_volume", speechVolume()).put("speech_boost_db", speechBoostDb())
				.put("speech_duck_percent", speechDuckPercent()).put("speech_cache_mb", speechCacheMb())
				.put("speech_repeat", speechRepeat()).put("speech_pause", speechPause())
				.put("no_picture_seconds", noPictureSeconds()).put("speak_channel", speakChannel()).put("message_mode", messageMode()).put("message_seconds", messageSeconds());
	}

	/** Sets one setting from text ("on"/"off", a number); null if done, else what was wrong. */
	static String set(String key, String value) {
		return apply(key, value, true);
	}

	/** The same without changing anything: null if the value would be accepted. */
	static String check(String key, String value) {
		return apply(key, value, false);
	}

	private static String apply(String key, String value, boolean commit) {
		SharedPreferences.Editor e = prefs.edit();
		switch (key) {
		case "footer":
		case "header":
		case "header_number":
		case "clock":
		case "guide":
		case "speech":
		case "speak_channel": {
			Boolean on = bool(value);
			if (on == null) return key + ": expected on or off";
			e.putBoolean(key, on);
			break;
		}
		case "schedule": {
			JSONArray entries;
			try {
				entries = new JSONArray(value);
			} catch (JSONException x) {
				return "schedule: not a JSON array";
			}
			String problem = checkSchedule(entries);
			if (problem != null) return problem;
			e.putString(key, entries.toString());
			break;
		}
		case "header_mode":
			if (!value.trim().equals("picture") && !value.trim().equals("seconds")) return key + ": picture or seconds";
			e.putString(key, value.trim());
			break;
		case "device_name": {
			String name = value.trim();
			if (name.length() > 40 || name.matches(".*\\p{Cntrl}.*")) return key + ": 1 to 40 characters, empty for the name of mqtt.conf";
			// Empty (or the name of mqtt.conf itself, which a whole-config write sends back) leaves the name of mqtt.conf in force.
			if (name.isEmpty() || name.equals(defaultName)) e.remove(key);
			else e.putString(key, name);
			break;
		}
		case "jitsi_room":
			if (!value.trim().isEmpty() && !value.trim().matches(VideoCall.ROOM)) {
				return key + ": empty, or letters, digits, - _ . (64 at most)";
			}
			e.putString(key, value.trim());
			break;
		case "message_mode":
			if (!MessageSpec.isMode(value.trim())) return key + ": text, speech or both";
			e.putString(key, value.trim());
			break;
		case "speech_voice":
			if (!validVoice(value.trim())) return key + ": a voice like " + EdgeTts.DEFAULT_VOICE + (IvonaTts.available() ? " or one of ivona_voices" : "");
			e.putString(key, value.trim());
			break;
		case "speech_rate":
			if (!value.trim().matches(EdgeTts.RATE_PATTERN)) return key + ": a percentage like -5% or +10%";
			e.putString(key, value.trim());
			break;
		default: {
			Object[] spec = numberSpec(key);
			if (spec == null) return "unknown setting " + key;
			int min = (Integer) spec[1], max = (Integer) spec[2];
			int n;
			try {
				n = Integer.parseInt(value.trim());
			} catch (NumberFormatException x) {
				return key + ": expected a number of " + spec[4];
			}
			if (n < min || n > max) return key + ": " + min + " to " + max;
			e.putInt(key, n);
			break;
		}
		}
		if (!commit) return null;
		e.apply();
		Runnable changed = onChange;
		if (changed != null) changed.run();
		return null;
	}

	private static Boolean bool(String value) {
		switch (value.trim().toLowerCase()) {
		case "on": case "true": case "1": return Boolean.TRUE;
		case "off": case "false": case "0": return Boolean.FALSE;
		default: return null;
		}
	}
}
