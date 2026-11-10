/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
package org.minicaptive;

import java.io.IOException;
import java.net.URLEncoder;
import java.nio.charset.StandardCharsets;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import org.jsoup.parser.Parser;

/**
 * WISPr 1.0 smart-client login: many hotel and carrier hotspots hide an XML block (often in an HTML comment) that
 * names a LoginURL, so a client can log in without rendering the page. Free hotspots of this kind accept empty or
 * arbitrary credentials; paid ones need user= and password=.
 */
final class Wispr {
	private static final Pattern BLOCK = Pattern.compile("<WISPAccessGatewayParam.*?</WISPAccessGatewayParam>",
			Pattern.DOTALL | Pattern.CASE_INSENSITIVE);

	private Wispr() {
	}

	static String tag(String xml, String name) {
		Matcher m = Pattern.compile("<" + name + ">\\s*(?:<!\\[CDATA\\[)?(.*?)(?:\\]\\]>)?\\s*</" + name + ">",
				Pattern.DOTALL | Pattern.CASE_INSENSITIVE).matcher(xml);
		return m.find() ? Parser.unescapeEntities(m.group(1).trim(), false) : null;
	}

	static String block(String body) {
		Matcher m = BLOCK.matcher(body);
		return m.find() ? m.group() : null;
	}

	/** true: the gateway accepted the login. false: no WISPr on this page. Throws when WISPr said no. */
	static boolean login(Http http, Http.Page page, Config cfg) throws IOException {
		String xml = block(page.body);
		// MessageType 110 (proxy): the real gateway answer is one more request away.
		for (int i = 0; xml != null && "110".equals(tag(xml, "MessageType")) && tag(xml, "NextURL") != null && i < 3; i++) {
			page = http.get(tag(xml, "NextURL"));
			xml = block(page.body);
		}
		if (xml == null || tag(xml, "LoginURL") == null) {
			return false;
		}
		String login = tag(xml, "LoginURL");
		Journal.log("WISPr gateway " + tag(xml, "LocationName") + ", login " + login);
		String form = "button=Login&FNAME=0&OriginatingServer=" + enc(page.chain.get(0)) + "&UserName=" + enc(cfg.user)
				+ "&Password=" + enc(cfg.password);
		Http.Page r = http.request("POST", login, "application/x-www-form-urlencoded",
				form.getBytes(StandardCharsets.UTF_8));
		String reply = block(r.body);
		// 201: authentication pending, the answer comes from LoginResultsURL.
		for (int i = 0; reply != null && "201".equals(tag(reply, "ResponseCode")) && i < 5; i++) {
			String next = tag(reply, "LoginResultsURL");
			if (next == null) {
				break;
			}
			sleep(Math.max(1, parse(tag(reply, "Delay"), 2)) * 1000L);
			reply = block(http.get(next).body);
		}
		String code = reply == null ? null : tag(reply, "ResponseCode");
		if ("50".equals(code)) {
			return true;
		}
		throw new IOException("WISPr login refused, code " + code + (reply == null ? "" : ", " + tag(reply, "ReplyMessage")));
	}

	private static int parse(String s, int fallbackForMissingDelay) {
		return s == null ? fallbackForMissingDelay : Integer.parseInt(s);
	}

	private static void sleep(long ms) throws IOException {
		try {
			Thread.sleep(ms);
		} catch (InterruptedException e) {
			Thread.currentThread().interrupt();
			throw new IOException("interrupted");
		}
	}

	private static String enc(String s) {
		try {
			return URLEncoder.encode(s, "UTF-8");
		} catch (java.io.UnsupportedEncodingException e) {
			throw new AssertionError(e);
		}
	}
}
