package org.minitv;

import org.json.JSONObject;

/**
 * How a message is presented: its text, mode (on screen, spoken or both), how long the screen part stays and how often
 * the voice part is spoken. The same keys come from cmd/message, the schedule entries and cmd/say; whatever a message
 * does not say comes from the settings (message_mode, message_seconds, speech_repeat, speech_pause).
 */
final class MessageSpec {
	static final String TEXT = "text", SPEECH = "speech", BOTH = "both";

	final String text;
	final String mode;
	final int showSeconds, repeat, pause;

	private MessageSpec(String text, String mode, int showSeconds, int repeat, int pause) {
		this.text = text;
		this.mode = mode;
		this.showSeconds = showSeconds;
		this.repeat = repeat;
		this.pause = pause;
	}

	static boolean isMode(Object value) {
		return TEXT.equals(value) || SPEECH.equals(value) || BOTH.equals(value);
	}

	/** Null if the optional keys mode, speak, show_seconds, repeat and pause are fine, else what is wrong. */
	static String check(JSONObject o) {
		if (o.has("mode") && !isMode(o.opt("mode"))) return "mode text, speech or both";
		if (o.has("speak") && !(o.opt("speak") instanceof Boolean)) return "speak true or false";
		for (String[] key : new String[][] { { "show_seconds", "message_seconds" }, { "repeat", "speech_repeat" },
				{ "pause", "speech_pause" } }) {
			if (!o.has(key[0])) continue;
			Object v = o.opt(key[0]);
			int min = Settings.numberMin(key[1]), max = Settings.numberMax(key[1]);
			if (!(v instanceof Integer) || (Integer) v < min || (Integer) v > max) return key[0] + " " + min + " to " + max;
		}
		return null;
	}

	/** A checked JSON message or schedule entry, with the settings for what it does not say. */
	static MessageSpec of(JSONObject o) {
		String mode;
		if (o.has("mode")) mode = o.optString("mode");
		// "speak" is the older form of mode, still accepted: true is both, false is text.
		else if (o.has("speak")) mode = o.optBoolean("speak") ? BOTH : TEXT;
		else mode = Settings.messageMode();
		return new MessageSpec(o.optString("text", "").trim(), mode, o.optInt("show_seconds", Settings.messageSeconds()),
				o.optInt("repeat", Settings.speechRepeat()), o.optInt("pause", Settings.speechPause()));
	}

	/** A plain text message: everything from the settings. */
	static MessageSpec of(String text) {
		return new MessageSpec(text == null ? "" : text.trim(), Settings.messageMode(), Settings.messageSeconds(),
				Settings.speechRepeat(), Settings.speechPause());
	}

	boolean screen() {
		return !SPEECH.equals(mode);
	}

	boolean voice() {
		return !TEXT.equals(mode);
	}
}
