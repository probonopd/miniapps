/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */
package org.minicaptive;

import java.io.IOException;

/**
 * The probes operating systems use: generate_204 answers "204, empty" and captive.apple.com "Success" when the
 * Internet is reachable. A portal answers anything else, usually a redirect to its login page.
 */
final class Probe {
	enum State { ONLINE, PORTAL, OFFLINE }

	final State state;
	/** The portal's page (after redirects) when PORTAL. */
	final Http.Page page;
	final String detail;

	private Probe(State state, Http.Page page, String detail) {
		this.state = state;
		this.page = page;
		this.detail = detail;
	}

	/**
	 * ONLINE when a probe passed and none was intercepted: some portals let Google's probe through and nothing
	 * else. PORTAL when one was intercepted. A probe without any answer counts for neither (Apple's is blocked
	 * in some countries). With no answer at all, the gateway is asked: portals that hide even DNS before
	 * the terms are accepted still answer there.
	 */
	static Probe run(Http http, Config cfg, String gateway) {
		Probe portal = null, online = null;
		String error = "no probe";
		for (String url : cfg.probes) {
			Http.Page p;
			long t0 = System.currentTimeMillis();
			try {
				p = http.get(url);
			} catch (IOException e) {
				error = url + ": " + e;
				Journal.log("probe " + url + ": no answer after " + (System.currentTimeMillis() - t0) + " ms: " + e);
				continue;
			}
			boolean ok = passed(url, p);
			Journal.log("probe " + url + ": " + p.code + ", " + p.body.length() + " bytes, "
					+ (System.currentTimeMillis() - t0) + " ms" + (p.chain.size() > 1 ? ", redirected to " + p.url : "")
					+ (ok ? " -> Internet" : " -> intercepted"));
			if (ok && online == null) {
				online = new Probe(State.ONLINE, p, url);
			} else if (!ok && portal == null) {
				portal = new Probe(State.PORTAL, p, "intercepted: " + p.code + " " + p.url);
			}
		}
		if (portal != null) {
			return portal;
		}
		if (online != null) {
			return online;
		}
		if (gateway != null) {
			try {
				Http.Page g = http.get("http://" + gateway + "/");
				boolean page = g.code < 400 && g.body.toLowerCase(java.util.Locale.ROOT).matches("(?s).*<(form|a |input|button|meta http-equiv).*");
				Journal.log("gateway " + gateway + ": " + g.code + ", " + g.body.length() + " bytes"
						+ (page ? " -> looks like a portal" : ""));
				if (page) {
					return new Probe(State.PORTAL, g, "portal at the gateway " + g.url);
				}
			} catch (IOException e) {
				Journal.log("gateway " + gateway + ": no answer: " + e);
			}
		}
		return new Probe(State.OFFLINE, null, error);
	}

	private static boolean passed(String url, Http.Page p) {
		if (p.chain.size() > 1) {
			return false;
		}
		if (url.contains("generate_204")) {
			return p.code == 204 && p.body.isEmpty();
		}
		return p.code == 200 && p.body.contains("<TITLE>Success</TITLE>");
	}
}
