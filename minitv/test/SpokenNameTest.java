package org.minitv;

/** Run with test.sh. The expected forms are the spoken="..." values of the generated playlist. */
public class SpokenNameTest {
	static int failures;

	static void check(String name, String expected) {
		String got = SpokenName.of(name);
		boolean ok = got.equals(expected);
		System.out.println((ok ? "ok   " : "FAIL ") + name + " -> " + got + (ok ? "" : " (expected " + expected + ")"));
		if (!ok) failures++;
	}

	public static void main(String[] args) {
		check("ARD", "A Er De");
		check("ARD Alpha", "A Er De Alpha");
		check("ARD Tagesschau", "A Er De Tagesschau");
		check("ZDF", "Zet De Eff");
		check("ZDF neo", "Zet De Eff Neo");
		check("ZDF info", "Zet De Eff Info");
		check("MDR Sachsen", "Em De Er Sachsen");
		check("MDR Thüringen", "Em De Er Thüringen");
		check("MDR Sachsen-Anhalt", "Em De Er Sachsen-Anhalt");
		check("PHOENIX", "Phönix");
		check("RBB Berlin", "Er Be Be Berlin");
		check("RBB Brandenburg", "Er Be Be Brandenburg");
		check("BR Süd", "Be Er Süd");
		check("BR Nord", "Be Er Nord");
		check("HR", "Ha Er");
		check("SR", "Es Er");
		check("SWR BW", "Es We Er Be We");
		check("SWR RP", "Es We Er Er Pe");
		check("NDR Hamburg", "En De Er Hamburg");
		check("NDR Niedersachsen", "En De Er Niedersachsen");
		check("NDR Schleswig-Holstein", "En De Er Schleswig-Holstein");
		check("NDR Mecklenburg-Vorpommern", "En De Er Mecklenburg-Vorpommern");
		check("DW", "De We");
		check("WDR Lokalzeit Köln", "We De Er Lokalzeit Köln");
		check("3Sat", "Drei Sat");
		check("KiKA", "Kika");
		check("ORF-1", "O Er Eff Eins");
		check("ORF-2", "O Er Eff Zwei");
		check("ORF-3", "O Er Eff Drei");
		check("ORF-Sport", "O Er Eff Sport");
		check("HDMI", "Ha De Em I");
		check("ORF1", "O Er Eff Eins");
		// These need spoken="...": an English word, a parenthesis, country codes that stand for words.
		System.out.println("needs spoken=: ARD ONE -> " + SpokenName.of("ARD ONE") + ", WDR (Deutschland) -> "
				+ SpokenName.of("WDR (Deutschland)") + ", ARTE DE -> " + SpokenName.of("ARTE DE") + ", ARTE FR -> " + SpokenName.of("ARTE FR"));
		System.out.println(failures == 0 ? "all passed" : failures + " failed");
		System.exit(failures == 0 ? 0 : 1);
	}
}
