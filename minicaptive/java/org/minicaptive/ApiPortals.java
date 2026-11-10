/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
package org.minicaptive;

import java.io.IOException;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

/**
 * Portals that have no form to press, only a JSON API behind their JavaScript. Built in for the big ones whose
 * sequence is known: Deutsche Bahn (as OpenWrt travelmate's login scripts send it) and conn4 (REWE, Kaufland,
 * B&B Hotels, Mövenpick, ibis budget, Maritim; the flow as binarynoise/CaptivePortalAutoLogin describes it).
 */
final class ApiPortals {
	/** Set when the portal is closed for now (conn4 outside opening hours): until then, epoch ms; else 0. */
	long closedUntil;

	/** true: a known API portal accepted the login. false: not one of them, or it said no. */
	boolean pass(Http http, Http.Page page) {
		for (String url : page.chain) {
			String host = URI.create(url).getHost();
			if ("wifi.bahn.de".equals(host) || "login.wifionice.de".equals(host)) {
				return deutscheBahn(http, host);
			}
			if (host != null && host.endsWith(".conn4.com") && page.body.contains("conn4.hotspot.wbsToken")) {
				try {
					return conn4(http, page, url);
				} catch (IOException | JSONException | RuntimeException e) {
					Journal.log("conn4: " + e.getMessage());
					return false;
				}
			}
		}
		return false;
	}

	private static final Pattern TOKEN = Pattern.compile("conn4\\.hotspot\\.wbsToken\\s*=\\s*\\{\\s*\"token\"\\s*:\\s*\"([^\"]+)\"");
	private static final Pattern WBS_BASE = Pattern.compile("data-wbs-base-url=\"([^\"]+)\"");
	private static final Pattern SITE_ID = Pattern.compile("[?&]site_id=(\\d+)");

	/**
	 * conn4 scene portals: the page carries a token and a timetable of scenes; the scene shown now either logs in
	 * (its page names the API) or says the WLAN is off (outside opening hours: no login exists then).
	 */
	/** What a conn4 page says: token, site, the scene to log in with, or when it opens again. */
	static final class Conn4Plan {
		String token;
		String site;
		/** null when closed. */
		String sceneUrl;
		/** Epoch ms of the next login scene when closed (0: none scheduled). */
		long opensAt;
	}

	/**
	 * Reads token and timetable from a conn4 page. The timetable has local wall-clock times without a zone: the
	 * device's own zone is the hotspot's (the traveler is there); "now" is when the portal made the page.
	 */
	static Conn4Plan conn4Plan(String body, java.util.List<String> chain, java.util.TimeZone zone)
			throws IOException, JSONException {
		Conn4Plan plan = new Conn4Plan();
		Matcher t = TOKEN.matcher(body);
		if (!t.find()) {
			throw new IOException("no token on the page");
		}
		plan.token = t.group(1);
		JSONObject schedule = objectAround(body, "\"scene_template\"");
		for (String u : chain) {
			Matcher m = SITE_ID.matcher(u);
			if (m.find()) {
				plan.site = m.group(1);
			}
		}
		if (plan.site == null && schedule.has("token")) {
			plan.site = String.valueOf(schedule.getJSONObject("token").opt("site"));
		}
		java.text.SimpleDateFormat f = new java.text.SimpleDateFormat("yyyy-MM-dd HH:mm:ss", java.util.Locale.ROOT);
		f.setTimeZone(zone);
		long now = schedule.optLong("initialization_ts", System.currentTimeMillis() / 1000) * 1000;
		JSONObject current = null;
		long currentAt = Long.MIN_VALUE;
		JSONArray events = schedule.getJSONArray("events");
		for (int i = 0; i < events.length(); i++) {
			JSONObject e = events.getJSONObject(i);
			JSONObject p = e.optJSONObject("payload");
			if (p == null || !"scene".equals(p.optString("type"))) {
				continue;
			}
			long at;
			try {
				at = f.parse(e.getString("time")).getTime();
			} catch (java.text.ParseException x) {
				throw new IOException("timetable: " + x.getMessage());
			}
			boolean login = !p.getJSONObject("data").optString("module").contains("offline");
			if (at <= now && at >= currentAt) {
				currentAt = at;
				current = p.getJSONObject("data");
			} else if (at > now && login && (plan.opensAt == 0 || at < plan.opensAt)) {
				plan.opensAt = at;
			}
		}
		if (current == null) {
			throw new IOException("no scene scheduled now");
		}
		if (!current.optString("module").contains("offline")) {
			plan.sceneUrl = schedule.getString("scene_template").replace("{id}", current.getString("id"));
		}
		return plan;
	}

	/**
	 * conn4 scene portals: the page carries a token and a timetable of scenes; the scene shown now either logs in
	 * (its page names the API) or says the WLAN is off (outside its opening hours: no login exists then).
	 */
	private boolean conn4(Http http, Http.Page page, String url) throws IOException, JSONException {
		Conn4Plan plan = conn4Plan(page.body, page.chain, java.util.TimeZone.getDefault());
		if (plan.sceneUrl == null) {
			closedUntil = plan.opensAt != 0 ? plan.opensAt : System.currentTimeMillis() + 60 * 60000L;
			Journal.log("conn4: this WLAN is closed now (its timetable shows no login), open again "
					+ (plan.opensAt != 0 ? "at " + new java.text.SimpleDateFormat("HH:mm", java.util.Locale.ROOT).format(new java.util.Date(plan.opensAt)) : "later"));
			return false;
		}
		String token = plan.token, site = plan.site;
		Http.Page scene = http.get(plan.sceneUrl);
		Matcher b = WBS_BASE.matcher(scene.body);
		if (!b.find()) {
			throw new IOException("scene has no API address");
		}
		String api = URI.create(scene.url).resolve(b.group(1).endsWith("/") ? b.group(1) : b.group(1) + "/").toString();
		JSONObject session = postJson(http, api + "create-session/", "authorization=" + enc("token=" + token)
				+ "&locale=de_DE&locationId=" + enc(site == null ? "" : site) + "&session_id=&with-tariffs=1");
		if (!session.optBoolean("ok")) {
			throw new IOException("create-session refused: " + session);
		}
		String sid = session.getString("session");
		for (JSONObject tariff : freeTariffs(session.optJSONArray("tariffs"))) {
			JSONObject r = postJson(http, api + "register/free/", "authorization=" + enc("session=" + sid)
					+ "&registration_type=terms-only&" + enc("registration[terms]") + "=1&tariff=" + tariff.getInt("id"));
			Journal.log("conn4: free tariff " + tariff.getInt("id") + " (" + tariff.optString("name") + "): "
					+ (r.optBoolean("ok") ? "registered" : "refused"));
			if (r.optBoolean("ok")) {
				return true;
			}
		}
		throw new IOException("no free tariff accepted");
	}

	/** Free ones only (no price, no paid, voucher, seat number, third party or SIM login), longest first. */
	private static java.util.List<JSONObject> freeTariffs(JSONArray all) throws JSONException {
		java.util.List<JSONObject> out = new java.util.ArrayList<>();
		for (int i = 0; all != null && i < all.length(); i++) {
			JSONObject t = all.getJSONObject(i);
			boolean free = t.optInt("price", 0) == 0 && (t.optBoolean("is_free", true) || t.optBoolean("is_free_with_limitations", false));
			for (String no : new String[] { "is_paid", "is_free_seat_number_name", "is_third_party", "is_smp", "is_eap",
					"is_paid_subscription" }) {
				free &= !t.optBoolean(no, false);
			}
			if (free || all.length() == 1) {
				out.add(t);
			}
		}
		out.sort((a, c) -> Integer.compare(minutes(c), minutes(a)));
		return out;
	}

	private static int minutes(JSONObject t) {
		return t.has("validity") ? t.optInt("validity") : t.optInt("duration") / 60;
	}

	private static JSONObject postJson(Http http, String url, String form) throws IOException, JSONException {
		Map<String, String> h = new LinkedHashMap<>();
		h.put("X-Requested-With", "XMLHttpRequest");
		h.put("Accept", "application/json");
		Http.Page r = http.request("POST", url, "application/x-www-form-urlencoded; charset=UTF-8",
				form.getBytes(StandardCharsets.UTF_8), h);
		return new JSONObject(r.body);
	}

	/** The JSON object literal in a script that contains key (balanced braces, strings respected). */
	private static JSONObject objectAround(String text, String key) throws IOException, JSONException {
		int k = text.indexOf(key);
		if (k < 0) {
			throw new IOException("no " + key + " on the page");
		}
		for (int start = text.lastIndexOf('{', k); start >= 0; start = text.lastIndexOf('{', start - 1)) {
			int end = closing(text, start);
			if (end > k) {
				return new JSONObject(text.substring(start, end + 1));
			}
		}
		throw new IOException("no object around " + key);
	}

	private static int closing(String s, int open) {
		int depth = 0;
		boolean str = false;
		for (int i = open; i < s.length(); i++) {
			char c = s.charAt(i);
			if (str) {
				if (c == '\\') {
					i++;
				} else if (c == '"') {
					str = false;
				}
			} else if (c == '"') {
				str = true;
			} else if (c == '{') {
				depth++;
			} else if (c == '}' && --depth == 0) {
				return i;
			}
		}
		return -1;
	}

	private static String enc(String s) {
		try {
			return java.net.URLEncoder.encode(s, "UTF-8");
		} catch (java.io.UnsupportedEncodingException e) {
			throw new AssertionError(e);
		}
	}

	/** WIFIonICE and Deutsche Bahn station WLANs: the current "cna" API, else the older form with a CSRF cookie. */
	private boolean deutscheBahn(Http http, String host) {
		String base = "https://" + host;
		Map<String, String> h = new LinkedHashMap<>();
		h.put("X-Requested-With", "XMLHttpRequest");
		h.put("X-Csrf-Token", "csrf");
		h.put("Referer", base + "/cna/");
		try {
			Http.Page r = http.request("POST", base + "/cna/logon", "application/json",
					"{}".getBytes(StandardCharsets.UTF_8), h);
			Journal.log("Deutsche Bahn API: " + r.code + " " + r.body.replaceAll("\\s+", " ").substring(0, Math.min(120, r.body.length())));
			if (r.body.contains("success")) {
				return true;
			}
		} catch (IOException e) {
			Journal.log("Deutsche Bahn API: " + e);
		}
		try {
			http.get(base + "/en/");
			String token = http.cookie(base + "/en/", "csrf");
			if (token == null) {
				Journal.log("Deutsche Bahn legacy form: no csrf cookie");
				return false;
			}
			Http.Page r = http.request("POST", base + "/en/", "application/x-www-form-urlencoded",
					("login=true&CSRFToken=" + token).getBytes(StandardCharsets.UTF_8));
			Journal.log("Deutsche Bahn legacy form: " + r.code);
			return r.code < 400;
		} catch (IOException e) {
			Journal.log("Deutsche Bahn legacy form: " + e);
			return false;
		}
	}
}
