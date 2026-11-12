/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */
package org.minicaptive;

import java.io.IOException;
import java.net.URI;
import java.net.URLEncoder;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Locale;
import java.util.Set;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import org.jsoup.Jsoup;
import org.jsoup.nodes.Document;
import org.jsoup.nodes.Element;
import org.jsoup.nodes.FormElement;

/**
 * First stage, plain HTTP: what a person does on most portals - follow the page's own redirects, tick every box,
 * keep the hidden fields, press the button that says accept or connect. Fast, and no browser engine involved.
 * Portals that build their page in JavaScript are left to {@link WebPasser}.
 */
final class FormPasser {
	private static final Pattern JS_LOCATION = Pattern.compile(
			"(?:window\\.|document\\.|top\\.|self\\.)?location(?:\\.href)?\\s*=\\s*['\"]([^'\"]+)['\"]"
					+ "|location\\.(?:replace|assign)\\(\\s*['\"]([^'\"]+)['\"]");
	private static final Pattern REFRESH = Pattern.compile("url\\s*=\\s*['\"]?([^'\";]+)", Pattern.CASE_INSENSITIVE);

	private final Http http;
	private final Config cfg;
	/** The page the stage ended on, to keep when it failed. */
	Http.Page last;

	FormPasser(Http http, Config cfg) {
		this.http = http;
		this.cfg = cfg;
	}

	/** Set by passApi when the portal is closed for now (epoch ms until it opens), else 0. */
	long closedUntil;

	/** A known API portal (no form at all). true when a probe passes afterwards. */
	boolean passApi(Http.Page page) {
		last = page;
		ApiPortals api = new ApiPortals();
		boolean ok = api.pass(http, page) && online();
		closedUntil = api.closedUntil;
		return ok;
	}

	/** WISPr on the page or the pages its redirects lead to. true when a probe passes afterwards. */
	boolean passWispr(Http.Page page) throws IOException {
		last = page;
		try {
			if (Wispr.login(http, page, cfg)) {
				Journal.log("WISPr login accepted");
				return online();
			}
		} catch (IOException e) {
			Journal.log(e.getMessage());
		}
		return false;
	}

	/** Forms and links, hop by hop (with WISPr on later pages too). true when a probe passes afterwards. */
	boolean pass(Http.Page page) throws IOException {
		Set<String> seen = new HashSet<>();
		for (int hop = 0; hop < 8; hop++) {
			last = page;
			if (hop > 0 && Wispr.block(page.body) != null && passWispr(page)) {
				return true;
			}
			Document doc = Jsoup.parse(page.body, page.url);
			String next = clientRedirect(doc, page.body);
			Step step = next != null ? new Step("follow " + next, "GET", next, null) : bestStep(doc);
			if (step == null) {
				Journal.log("form stage: nothing to press on " + page.url);
				return false;
			}
			if (!seen.add(step.method + " " + step.url + " " + step.form)) {
				Journal.log("form stage: same step again, giving up: " + step.what);
				return false;
			}
			Journal.log("form stage: " + step.what);
			page = step.form == null ? http.get(step.url)
					: http.request(step.method, step.url, "application/x-www-form-urlencoded",
							step.form.getBytes(StandardCharsets.UTF_8));
			if (online()) {
				return true;
			}
		}
		last = page;
		return false;
	}

	private boolean online() {
		return Probe.run(http, cfg, null).state == Probe.State.ONLINE;
	}

	private static final class Step {
		final String what;
		final String method;
		final String url;
		/** urlencoded body for POST, null for GET (then it is in the URL). */
		final String form;

		Step(String what, String method, String url, String form) {
			this.what = what;
			this.method = method;
			this.url = url;
			this.form = form;
		}
	}

	/** Meta refresh, a script that only sets location, or a frame that is the whole page. */
	private static String clientRedirect(Document doc, String body) {
		for (Element m : doc.select("meta[http-equiv~=(?i)refresh][content]")) {
			Matcher r = REFRESH.matcher(m.attr("content"));
			if (r.find()) {
				return abs(doc, r.group(1).trim());
			}
		}
		if (doc.select("form, a[href], button, input[type=submit]").isEmpty()) {
			Element frame = doc.selectFirst("frame[src], iframe[src]");
			if (frame != null) {
				return frame.absUrl("src");
			}
			for (Element s : doc.select("script")) {
				Matcher j = JS_LOCATION.matcher(s.data());
				if (j.find()) {
					return abs(doc, j.group(1) != null ? j.group(1) : j.group(2));
				}
			}
		}
		return null;
	}

	private static String abs(Document doc, String url) {
		return URI.create(doc.location()).resolve(url.replace(" ", "%20")).toString();
	}

	/** The best form to submit, or else the best link to follow; null when nothing looks like getting online. */
	private Step bestStep(Document doc) {
		Step best = null;
		int bestScore = 0;
		String host = URI.create(doc.location()).getHost();
		List<Config.Rule> rules = cfg.rulesFor(host);
		for (FormElement f : doc.select("form").forms()) {
			Element button = null;
			int bscore = Integer.MIN_VALUE;
			for (Element b : f.select("button, input[type=submit], input[type=image]")) {
				int s = clickScore(b, rules);
				if (s > bscore) {
					bscore = s;
					button = b;
				}
			}
			int score = (button == null ? Words.score(f.text()) : bscore) + (f.select("input[type=checkbox]").isEmpty() ? 0 : 2);
			if (!f.select("input[type=password]").isEmpty() && cfg.password.isEmpty()) {
				score -= 15;
			}
			if (!f.select("input[name=q], input[type=search]").isEmpty()) {
				score -= 15;
			}
			if (f.select("input, button, select").isEmpty()) {
				continue;
			}
			// A lone form on a portal page is the login, whatever its button says.
			if (score > bestScore || (best == null && doc.select("form").size() == 1 && score > -10)) {
				bestScore = Math.max(score, 1);
				best = submit(f, button, rules);
			}
		}
		if (best != null) {
			return best;
		}
		for (Element a : doc.select("a[href]")) {
			String href = a.absUrl("href");
			if (!href.startsWith("http")) {
				continue;
			}
			int s = clickScore(a, rules);
			if (s > bestScore) {
				bestScore = s;
				best = new Step("link \"" + a.text() + "\"", "GET", href, null);
			}
		}
		return best;
	}

	private static int clickScore(Element e, List<Config.Rule> rules) {
		String text = (e.text() + " " + e.attr("value") + " " + e.attr("alt") + " " + e.attr("title")).trim();
		for (Config.Rule r : rules) {
			if (r.action.equals("click") && text.toLowerCase(Locale.ROOT).contains(r.arg.toLowerCase(Locale.ROOT))) {
				return 100;
			}
		}
		return Words.score(text);
	}

	private Step submit(FormElement f, Element button, List<Config.Rule> rules) {
		List<String[]> fields = new ArrayList<>();
		Set<String> radios = new HashSet<>();
		for (Element e : f.elements()) {
			String name = e.attr("name");
			if (name.isEmpty() || e.hasAttr("disabled")) {
				continue;
			}
			String type = e.attr("type").toLowerCase(Locale.ROOT);
			if (e.tagName().equals("select")) {
				Element o = e.selectFirst("option[selected]");
				o = o != null ? o : e.selectFirst("option");
				fields.add(new String[] { name, o == null ? "" : (o.hasAttr("value") ? o.attr("value") : o.text()) });
			} else if (e.tagName().equals("textarea")) {
				fields.add(new String[] { name, e.text() });
			} else if (e.tagName().equals("input")) {
				switch (type) {
				case "submit": case "image": case "button": case "reset": case "file":
					break;
				case "checkbox":
					if (Words.tickable(e.hasAttr("required"), checkboxText(e))) {
						fields.add(new String[] { name, e.hasAttr("value") ? e.attr("value") : "on" });
					}
					break;
				case "radio":
					if (radios.add(name)) {
						fields.add(new String[] { name, e.attr("value") });
					}
					break;
				case "password":
					fields.add(new String[] { name, cfg.password });
					break;
				default:
					fields.add(new String[] { name, fieldValue(e) });
				}
			}
		}
		for (Config.Rule r : rules) {
			if (r.action.equals("fill")) {
				String n = r.arg.substring(0, r.arg.indexOf('='));
				fields.removeIf(x -> x[0].equals(n));
				fields.add(new String[] { n, r.arg.substring(r.arg.indexOf('=') + 1) });
			}
		}
		String what = "form";
		if (button != null) {
			what = "form, button \"" + (button.text() + " " + button.attr("value")).trim() + "\"";
			String n = button.attr("name");
			if (!n.isEmpty() && button.attr("type").equalsIgnoreCase("image")) {
				fields.add(new String[] { n + ".x", "1" });
				fields.add(new String[] { n + ".y", "1" });
			} else if (!n.isEmpty()) {
				fields.add(new String[] { n, button.attr("value") });
			}
		}
		StringBuilder q = new StringBuilder();
		for (String[] kv : fields) {
			q.append(q.length() == 0 ? "" : "&").append(enc(kv[0])).append('=').append(enc(kv[1]));
		}
		String action = f.hasAttr("action") && !f.attr("action").isEmpty() ? f.absUrl("action") : f.ownerDocument().location();
		if (button != null && button.hasAttr("formaction")) {
			action = button.absUrl("formaction");
		}
		if (f.attr("method").equalsIgnoreCase("post")) {
			return new Step(what + " to " + action, "POST", action, q.toString());
		}
		String url = action.replaceFirst("#.*", "").replaceFirst("\\?.*", "") + "?" + q;
		return new Step(what + " to " + url, "GET", url, null);
	}

	/** What a checkbox is about: its label (by for= or around it), its name and id. */
	private static String checkboxText(Element e) {
		StringBuilder t = new StringBuilder(e.attr("name") + " " + e.id());
		if (!e.id().isEmpty()) {
			for (Element l : e.ownerDocument().select("label[for=" + e.id() + "]")) {
				t.append(' ').append(l.text());
			}
		}
		Element p = e.closest("label");
		t.append(' ').append(p != null ? p.text() : e.parent() != null ? e.parent().text() : "");
		return t.toString();
	}

	/** Keeps what the portal filled in, else what the settings say this field wants, else leaves it empty. */
	private String fieldValue(Element e) {
		if (!e.attr("value").isEmpty()) {
			return e.attr("value");
		}
		String hint = e.attr("name") + " " + e.id() + " " + e.attr("placeholder") + " " + e.attr("type");
		if (hint.toLowerCase(Locale.ROOT).matches(".*(user|login|benutzer).*")) {
			return cfg.user;
		}
		String kind = Words.personKind(hint);
		return kind != null && cfg.person.containsKey(kind) ? cfg.person.get(kind) : "";
	}

	private static String enc(String s) {
		try {
			return URLEncoder.encode(s, "UTF-8");
		} catch (java.io.UnsupportedEncodingException e) {
			throw new AssertionError(e);
		}
	}
}
