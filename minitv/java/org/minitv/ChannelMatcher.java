package org.minitv;

import java.text.Normalizer;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;

/** Finds the channel a spoken sentence is about. */
final class ChannelMatcher {
	// German names of the letters: acronyms such as ZDF or MDR are spoken letter by letter.
	private static final Map<String, String> LETTERS = new HashMap<>();
	// Filler words of a sentence ("zeig mir das ZDF bitte"); they never decide which channel is meant.
	private static final java.util.Set<String> FILLER = new java.util.HashSet<>(java.util.Arrays.asList(
			"das", "die", "der", "den", "dem", "ein", "eine", "in", "im", "auf", "zu", "mal", "bitte", "von", "und",
			"mir", "mich", "zeig", "zeige", "schalte", "schalt", "wechsle", "sender", "kanal", "programm"));
	private static final String[] DIGITS = { "null", "eins", "zwei", "drei", "vier", "funf", "sechs", "sieben", "acht", "neun" };

	static {
		String[] pairs = { "a", "a", "be", "b", "ce", "c", "ze", "c", "de", "d", "e", "e", "ef", "f", "eff", "f", "ge", "g",
				"ha", "h", "i", "i", "jot", "j", "ka", "k", "el", "l", "ell", "l", "em", "m", "emm", "m", "en", "n", "enn", "n",
				"o", "o", "pe", "p", "ku", "q", "er", "r", "es", "s", "ess", "s", "te", "t", "u", "u", "fau", "v", "vau", "v",
				"we", "w", "iks", "x", "ypsilon", "y", "zet", "z", "zett", "z" };
		for (int i = 0; i < pairs.length; i += 2) LETTERS.put(pairs[i], pairs[i + 1]);
		// A recognizer may also write the letters themselves ("r b b").
		for (char c = 'a'; c <= 'z'; c++) LETTERS.put(String.valueOf(c), String.valueOf(c));
	}

	private static final int MIN_WORD_ALONE = 4;

	private ChannelMatcher() {
	}

	/**
	 * What a recognizer should listen for: the channel names as whole phrases (spelled as the speech model spells
	 * them, so with umlauts), how they are spoken, their longer words on their own ("Tagesschau"), and "[unk]" for
	 * everything else. Single letters and short words stay out: they turn noise into a channel word.
	 */
	static List<String> grammarWords(List<String> phrases, List<String> spoken) {
		java.util.Set<String> out = new java.util.LinkedHashSet<>();
		String[] digits = { "null", "eins", "zwei", "drei", "vier", "fünf", "sechs", "sieben", "acht", "neun" };
		List<String> all = new ArrayList<>(phrases);
		all.addAll(spoken);
		for (String name : all) {
			StringBuilder spaced = new StringBuilder();
			for (char c : name.toLowerCase(Locale.ROOT).toCharArray()) {
				if (c >= '0' && c <= '9') spaced.append(' ').append(digits[c - '0']).append(' ');
				else spaced.append(Character.isLetter(c) ? c : ' ');
			}
			String phrase = spaced.toString().trim().replaceAll(" +", " ");
			if (phrase.isEmpty()) continue;
			out.add(phrase);
			for (String word : phrase.split(" ")) {
				if (word.length() >= MIN_WORD_ALONE && !FILLER.contains(word)) out.add(word);
			}
		}
		out.add("[unk]");
		return new ArrayList<>(out);
	}

	/** Words for leaving the TV. Only offered with a wake word: a remote has its own button for this. */
	static final List<String> QUIT_WORDS = java.util.Arrays.asList("beenden", "ausschalten");

	static boolean isQuit(String spoken) {
		for (String word : spoken.toLowerCase(Locale.ROOT).split(" +")) {
			if (QUIT_WORDS.contains(word)) return true;
		}
		return false;
	}

	static final List<String> VOLUME_WORDS = java.util.Arrays.asList("lauter", "leiser");
	static final int VOLUME_LEVELS = 7;

	/** +1 for louder, -1 for quieter, 0 when the command is not about the volume. */
	static int volumeStep(String spoken) {
		for (String word : spoken.toLowerCase(Locale.ROOT).split(" +")) {
			if (word.equals("lauter")) return 1;
			if (word.equals("leiser")) return -1;
		}
		return 0;
	}

	/** The next of evenly spaced volume levels, so that a full sweep ends exactly at silence or at the maximum. */
	static int nextVolume(int current, int max, int step) {
		int level = Math.round((float) current * VOLUME_LEVELS / max) + step;
		return Math.round((float) Math.max(0, Math.min(VOLUME_LEVELS, level)) * max / VOLUME_LEVELS);
	}

	/** The app a spoken command asks for, out of phrase to package, or null. */
	static String appFor(String spoken, Map<String, String> apps) {
		// Whole words only: "radio" must not fire on a word that merely contains it.
		String text = " " + spoken.toLowerCase(Locale.ROOT) + " ";
		for (Map.Entry<String, String> app : apps.entrySet()) {
			if (text.contains(" " + app.getKey() + " ")) return app.getValue();
		}
		return null;
	}

	/** The index of the channel meant, or -1. */
	static int match(String spoken, List<String> phrases, List<Integer> owners) {
		List<List<String>> titles = new ArrayList<>();
		for (String phrase : phrases) titles.add(words(phrase, false));
		List<String> said = new ArrayList<>();
		for (String word : words(spoken, true)) {
			if (!FILLER.contains(word) && known(word, titles)) said.add(word);
		}
		if (said.isEmpty()) return -1;
		int best = -1;
		double bestScore = 0;
		for (int i = 0; i < titles.size(); i++) {
			List<String> title = titles.get(i);
			boolean all = true;
			for (String word : said) all &= contains(title, word);
			double score = (double) said.size() / title.size();
			if (all && score > bestScore) {
				bestScore = score;
				best = owners.get(i);
			}
		}
		return best >= 0 ? best : closest(said, titles, owners);
	}

	/**
	 * A stray word must not cost the whole command ("berlin das erste"): takes the title with the most words said.
	 * Between equals the later word wins, because noise comes before a command more often than after it.
	 */
	private static int closest(List<String> said, List<List<String>> titles, List<Integer> owners) {
		int best = -1;
		int bestWords = 0;
		int bestLast = -1;
		double bestShare = 0;
		for (int i = 0; i < titles.size(); i++) {
			List<String> title = titles.get(i);
			int words = 0;
			int last = -1;
			for (int w = 0; w < said.size(); w++) {
				if (contains(title, said.get(w))) {
					words++;
					last = w;
				}
			}
			double share = (double) words / title.size();
			if (words > bestWords || (words == bestWords && (last > bestLast || (last == bestLast && share > bestShare)))) {
				best = owners.get(i);
				bestWords = words;
				bestLast = last;
				bestShare = share;
			}
		}
		return best;
	}

	private static boolean known(String word, List<List<String>> titles) {
		for (List<String> title : titles) {
			if (contains(title, word)) return true;
		}
		return false;
	}

	private static boolean contains(List<String> title, String word) {
		for (String t : title) {
			if (similar(word, t)) return true;
		}
		return false;
	}

	// Words of four letters or more may differ by one letter (a recognizer's slip), shorter ones may not.
	private static boolean similar(String a, String b) {
		if (a.equals(b)) return true;
		return Math.min(a.length(), b.length()) >= 4 && distance(a, b) <= 1;
	}

	private static List<String> words(String text, boolean spelled) {
		String plain = Normalizer.normalize(text.toLowerCase(Locale.ROOT), Normalizer.Form.NFD)
				.replaceAll("\\p{M}", "").replace("ß", "ss");
		StringBuilder spaced = new StringBuilder();
		for (char c : plain.toCharArray()) {
			if (c >= '0' && c <= '9') spaced.append(' ').append(DIGITS[c - '0']).append(' ');
			else if (c >= 'a' && c <= 'z') spaced.append(c);
			else spaced.append(' ');
		}
		List<String> out = new ArrayList<>();
		String[] tokens = spaced.toString().trim().split(" +");
		for (int i = 0; i < tokens.length && !tokens[i].isEmpty();) {
			int j = i;
			StringBuilder run = new StringBuilder();
			while (spelled && j < tokens.length && LETTERS.containsKey(tokens[j])) run.append(LETTERS.get(tokens[j++]));
			if (j - i >= 2) {
				out.add(run.toString());
				i = j;
			} else {
				out.add(tokens[i++]);
			}
		}
		return out;
	}

	private static int distance(String a, String b) {
		int[] row = new int[b.length() + 1];
		for (int j = 0; j <= b.length(); j++) row[j] = j;
		for (int i = 1; i <= a.length(); i++) {
			int diagonal = row[0];
			row[0] = i;
			for (int j = 1; j <= b.length(); j++) {
				int above = row[j];
				row[j] = Math.min(Math.min(row[j] + 1, row[j - 1] + 1), diagonal + (a.charAt(i - 1) == b.charAt(j - 1) ? 0 : 1));
				diagonal = above;
			}
		}
		return row[b.length()];
	}
}
