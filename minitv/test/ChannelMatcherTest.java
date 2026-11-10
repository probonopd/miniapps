package org.minitv;

import java.util.Arrays;
import java.util.List;

/** Run with test.sh. */
public class ChannelMatcherTest {
	static final List<String> NAMES = Arrays.asList("ARD", "ARD ONE", "ARD Alpha", "ARD Tagesschau", "ZDF", "ZDF neo", "ZDF info",
			"MDR Sachsen", "MDR Thüringen", "MDR Sachsen-Anhalt", "PHOENIX", "WDR Lokalzeit Köln", "WDR (Deutschland)", "ARTE DE", "ARTE FR",
			"3Sat", "kika", "DW", "ORF-1", "NDR Hamburg", "RBB Brandenburg", "RBB Berlin", "HR", "SWR BW");
	// Alternative names, as the playlist generator writes them into the #EXTINF lines.
	static final java.util.Map<String, String> ALIASES = new java.util.HashMap<>();
	static {
		ALIASES.put("ARD", "Das Erste");
		ALIASES.put("HR", "Hessen;Hessischer Rundfunk");
		ALIASES.put("SWR BW", "Baden-W\u00fcrttemberg;SWR Baden-W\u00fcrttemberg");
		ALIASES.put("DW", "Deutsche Welle");
		ALIASES.put("kika", "Kinderkanal");
	}
	static int failures;

	static void check(String spoken, String expected) {
		java.util.List<String> phrases = new java.util.ArrayList<>();
		java.util.List<Integer> owners = new java.util.ArrayList<>();
		for (int n = 0; n < NAMES.size(); n++) {
			phrases.add(NAMES.get(n));
			owners.add(n);
			String alias = ALIASES.get(NAMES.get(n));
			if (alias != null) for (String a : alias.split(";")) {
				phrases.add(a);
				owners.add(n);
			}
		}
		int i = ChannelMatcher.match(spoken, phrases, owners);
		String got = i < 0 ? null : NAMES.get(i);
		boolean ok = expected == null ? got == null : expected.equals(got);
		if (!ok) failures++;
		System.out.println((ok ? "ok   " : "FAIL ") + "\"" + spoken + "\" -> " + got + (ok ? "" : " (expected " + expected + ")"));
	}

	public static void main(String[] args) {
		java.util.Map<String, String> apps = new java.util.HashMap<>();
		apps.put("öffne transistor", "app.one");
		if (!"app.one".equals(ChannelMatcher.appFor("bitte öffne transistor", apps)) || ChannelMatcher.appFor("transistor", apps) != null) {
			throw new AssertionError("appFor");
		}
		apps.put("radio", "app.two");
		if (!"app.two".equals(ChannelMatcher.appFor("radio", apps)) || ChannelMatcher.appFor("radiosender", apps) != null) {
			throw new AssertionError("appFor word");
		}
		if (ChannelMatcher.volumeStep("lauter") != 1 || ChannelMatcher.volumeStep("leiser") != -1 || ChannelMatcher.volumeStep("zdf") != 0
				|| ChannelMatcher.nextVolume(13, 30, 1) != 17 || ChannelMatcher.nextVolume(13, 30, -1) != 9
				|| ChannelMatcher.nextVolume(30, 30, 1) != 30 || ChannelMatcher.nextVolume(0, 30, -1) != 0) {
			throw new AssertionError("volume");
		}
		check("zdf", "ZDF");
		if (!ChannelMatcher.isQuit("bitte beenden") || !ChannelMatcher.isQuit("ausschalten") || ChannelMatcher.isQuit("das erste")) {
			throw new AssertionError("isQuit");
		}
		check("o berlin das erste", "ARD");
		check("berlin zdf", "ZDF");
		check("das zdf", "ZDF");
		check("zett de eff", "ZDF");
		check("zdf neo", "ZDF neo");
		check("zdf info", "ZDF info");
		check("ard", "ARD");
		check("a er de", "ARD");
		check("ard alpha", "ARD Alpha");
		check("tagesschau", "ARD Tagesschau");
		check("mdr sachsen", "MDR Sachsen");
		check("em de er sachsen anhalt", "MDR Sachsen-Anhalt");
		check("mdr thüringen", "MDR Thüringen");
		check("wdr köln", "WDR Lokalzeit Köln");
		check("wdr", "WDR (Deutschland)");
		check("phoenix", "PHOENIX");
		check("arte", "ARTE DE");
		check("arte fr", "ARTE FR");
		check("drei sat", "3Sat");
		check("kika", "kika");
		check("de we", "DW");
		check("o er eff eins", "ORF-1");
		check("ndr hamburg bitte", "NDR Hamburg");
		check("er be be brandenburg", "RBB Brandenburg");
		check("r b b berlin", "RBB Berlin");
		check("em de er", "MDR Sachsen");
		check("wetter in hamburg", "NDR Hamburg");
		check("wetter in paris", null);
		check("hessen", "HR");
		check("hessischer rundfunk", "HR");
		check("baden w\u00fcrttemberg", "SWR BW");
		check("swr baden w\u00fcrttemberg", "SWR BW");
		check("das erste", "ARD");
		check("deutsche welle", "DW");
		check("kinderkanal", "kika");
		check("", null);
		System.out.println(failures == 0 ? "all passed" : failures + " failed");
		System.exit(failures == 0 ? 0 : 1);
	}
}
