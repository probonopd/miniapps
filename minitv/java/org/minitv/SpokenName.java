package org.minitv;

import java.util.HashMap;
import java.util.Map;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * How a station name is said when it is announced, for a playlist entry without spoken="...". The multilingual voices
 * read "ARD" with English letter names, so abbreviations are written out with the German letter names, digits as
 * German words ("3Sat": "Drei Sat"). Plain Java, so that it can be tested on a PC.
 */
final class SpokenName {
	private static final String[] LETTERS = { "A", "Be", "Ze", "De", "E", "Eff", "Ge", "Ha", "I", "Jot", "Ka", "El", "Em",
			"En", "O", "Pe", "Ku", "Er", "Es", "Te", "U", "Fau", "We", "Ix", "Ypsilon", "Zet" };
	private static final String[] DIGITS = { "Null", "Eins", "Zwei", "Drei", "Vier", "Fünf", "Sechs", "Sieben", "Acht", "Neun" };
	// Names that are said as words although they are written in capitals.
	private static final Map<String, String> WORDS = new HashMap<>();
	static {
		WORDS.put("KiKA", "Kika");
		WORDS.put("ARTE", "Arte");
		WORDS.put("PHOENIX", "Phönix");
	}
	private static final Pattern ABBREVIATION = Pattern.compile("([A-Z]{2,5})(\\d*)");
	private static final Pattern LEADING_DIGITS = Pattern.compile("(\\d+)(\\p{L}.*)?");

	private SpokenName() {
	}

	static String of(String name) {
		StringBuilder out = new StringBuilder();
		for (String word : name.trim().split("\\s+")) {
			// A hyphen next to an abbreviation or a number ("ORF-1", "ORF-Sport") is a pause; between words
			// ("Sachsen-Anhalt") it belongs to the name.
			String[] parts = word.split("-");
			boolean split = parts.length > 1;
			if (split) {
				boolean technical = false;
				for (String p : parts) technical |= ABBREVIATION.matcher(p).matches() || p.matches("\\d+");
				split = technical;
			}
			String spoken = split ? String.join(" ", spell(parts)) : String.join("", spell(new String[] { word }));
			if (out.length() > 0) out.append(' ');
			out.append(spoken);
		}
		return out.toString();
	}

	private static String[] spell(String[] words) {
		String[] out = new String[words.length];
		for (int i = 0; i < words.length; i++) out[i] = spellWord(words[i]);
		return out;
	}

	private static String spellWord(String word) {
		if (word.isEmpty()) return word;
		String known = WORDS.get(word);
		if (known != null) return known;
		Matcher m = ABBREVIATION.matcher(word);
		if (m.matches()) {
			StringBuilder b = new StringBuilder();
			for (char c : m.group(1).toCharArray()) b.append(b.length() > 0 ? " " : "").append(LETTERS[c - 'A']);
			if (!m.group(2).isEmpty()) b.append(' ').append(digits(m.group(2)));
			return b.toString();
		}
		m = LEADING_DIGITS.matcher(word);
		if (m.matches()) return digits(m.group(1)) + (m.group(2) == null ? "" : " " + capitalize(m.group(2)));
		return capitalize(word);
	}

	private static String digits(String number) {
		StringBuilder b = new StringBuilder();
		for (char c : number.toCharArray()) b.append(b.length() > 0 ? " " : "").append(DIGITS[c - '0']);
		return b.toString();
	}

	/** Station names are nouns, written with a capital in German ("neo" is "Neo"). */
	private static String capitalize(String word) {
		return Character.toUpperCase(word.charAt(0)) + word.substring(1);
	}
}
