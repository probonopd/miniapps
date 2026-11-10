/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
package org.minicaptive;

import java.util.Locale;
import java.util.regex.Pattern;

/**
 * What portal buttons say. The same expressions run in Java (form stage) and JavaScript (WebView stage), so they
 * keep to the syntax both share.
 */
final class Words {
	/** Accepting the terms and getting online, in the languages hotspots in Europe use. */
	static final String POSITIVE = "accept|agree|akzept|zustimm|einverstanden|connect|verbind|continue|weiter"
			+ "|fortfahren|log ?in|anmelden|einloggen|sign ?in|start|surf|go online|get online|online gehen|internet"
			+ "|free|kostenlos|gratis|\\bok\\b|accepter|accès|aceptar|conectar|acepto|accetto|accedi|proceed|submit"
			+ "|i understand|\\benter\\b|navigate|browse|confirm|best[aä]tig|j'accepte|connexion|continuer"
			+ "|accetta|connetti|continua|akkoord|doorgaan|continuar|ich stimme zu|skip|[uü]berspringen"
			+ "|zum internet|los geht";
	/** Leaving, paying, signing up, switching language or reading the small print. */
	static final String NEGATIVE = "cancel|abbrechen|decline|ablehnen|reject|log ?out|abmelden|\\bback\\b|zur[uü]ck"
			+ "|language|sprache|english|deutsch|fran[cç]ais|espa[nñ]ol|imprint|impressum|help|hilfe|register"
			+ "|registrier|buy|kaufen|premium|\\bpay|bezahl|purchase|upgrade|facebook|google|apple|twitter|instagram"
			+ "|linkedin|sms|voucher|weiter ohne|no thanks|nein danke|sign ?up|gutschein|ticket|reset|reboot|restart|neustart|delete|l[oö]sch|factory|werks|firmware";
	/** Boxes for advertising: left alone unless the portal marks them required. */
	static final String MARKETING = "newsletter|marketing|werbung|angebot|promo|offer|advert|publicit|commercial"
			+ "|informiert|news|partner";
	static final Pattern MARK = Pattern.compile(MARKETING, Pattern.CASE_INSENSITIVE | Pattern.UNICODE_CASE);

	/** Ticks every box (terms, AGB, privacy, unknown ones) except advertising consent nobody requires. */
	static boolean tickable(boolean required, String text) {
		return required || !MARK.matcher(text).find();
	}

	/** Form fields a portal may ask for, by name, id or placeholder. */
	static final String EMAIL = "e-?mail";
	static final String ROOM = "room|zimmer|chambre|habitaci";
	static final String NAME = "name";

	static final Pattern POS = Pattern.compile(POSITIVE, Pattern.CASE_INSENSITIVE | Pattern.UNICODE_CASE);
	static final Pattern NEG = Pattern.compile(NEGATIVE, Pattern.CASE_INSENSITIVE | Pattern.UNICODE_CASE);

	private Words() {
	}

	/** Higher is more likely the button that gets one online; negative means keep away. */
	static int score(String text) {
		if (text == null || text.length() > 80) {
			return 0;
		}
		String t = text.toLowerCase(Locale.ROOT);
		return (NEG.matcher(t).find() ? -20 : 0) + (POS.matcher(t).find() ? 10 : 0);
	}

	/** Which person value (email, room, name) a field asks for, or null. */
	static String personKind(String hint) {
		String h = hint.toLowerCase(Locale.ROOT);
		if (Pattern.compile(EMAIL).matcher(h).find()) {
			return "email";
		}
		if (Pattern.compile(ROOM).matcher(h).find()) {
			return "room";
		}
		return Pattern.compile(NAME).matcher(h).find() && !h.contains("user") ? "name" : null;
	}
}
