/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */
package org.minicaptive;

import android.net.Network;
import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.CookieManager;
import java.net.CookiePolicy;
import java.net.HttpURLConnection;
import java.net.URI;
import java.net.URL;
import java.nio.charset.Charset;
import java.nio.charset.StandardCharsets;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * HTTP over the WLAN network only (a phone would otherwise ask the portal's questions over mobile data), with one
 * cookie jar per attempt, since portals keep their session in cookies. Redirects are followed here, not by
 * HttpURLConnection, which neither keeps cookies across them nor goes from http to https.
 */
final class Http {
	/**
	 * What every current Chrome on Android sends (since Chrome's user-agent reduction the same "Android 10; K" for
	 * all devices), with the Chrome version of this device's WebView: nothing to tell this device or this app
	 * apart from millions of phones. A fixed old version would stand out by itself.
	 */
	static volatile String userAgent = "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) "
			+ "Chrome/130.0.0.0 Mobile Safari/537.36";
	static volatile String acceptLanguage = "en-US,en;q=0.9";

	static void init(android.content.Context ctx) {
		java.util.regex.Matcher m = Pattern.compile("Chrome/(\\d+)")
				.matcher(android.webkit.WebSettings.getDefaultUserAgent(ctx));
		if (m.find()) {
			userAgent = "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/"
					+ m.group(1) + ".0.0.0 Mobile Safari/537.36";
		}
		java.util.Locale l = java.util.Locale.getDefault();
		acceptLanguage = l.getCountry().isEmpty() || l.getLanguage().equals("en") ? "en-US,en;q=0.9"
				: l.getLanguage() + "-" + l.getCountry() + "," + l.getLanguage() + ";q=0.9,en-US;q=0.8,en;q=0.7";
	}
	private static final Pattern CHARSET = Pattern.compile("charset=\"?([\\w-]+)", Pattern.CASE_INSENSITIVE);
	private static final int MAX_BODY = 2 << 20;

	static final class Page {
		final String url;
		final int code;
		final String body;
		/** Every URL of the redirect chain, first one included. */
		final List<String> chain;

		Page(String url, int code, String body, List<String> chain) {
			this.url = url;
			this.code = code;
			this.body = body;
			this.chain = chain;
		}

		String host() {
			return URI.create(url).getHost();
		}
	}

	private final Network network;
	private final CookieManager cookies = new CookieManager(null, CookiePolicy.ACCEPT_ALL);

	Http(Network network) {
		this.network = network;
	}

	Page get(String url) throws IOException {
		return request("GET", url, null, null);
	}

	/** body null: GET. Follows up to 10 redirects; 303 and 302 after a POST turn into a GET, as browsers do. */
	Page request(String method, String url, String contentType, byte[] body) throws IOException {
		return request(method, url, contentType, body, Collections.emptyMap());
	}

	/** The value of cookie name the server set for url's host, or null. */
	String cookie(String url, String name) {
		for (java.net.HttpCookie c : cookies.getCookieStore().get(URI.create(url))) {
			if (c.getName().equals(name)) {
				return c.getValue();
			}
		}
		return null;
	}

	/** With extra request headers (API portals want XHR-like requests). */
	Page request(String method, String url, String contentType, byte[] body, Map<String, String> headers)
			throws IOException {
		List<String> chain = new java.util.ArrayList<>();
		for (int hop = 0; hop < 10; hop++) {
			chain.add(url);
			HttpURLConnection c = (HttpURLConnection) network.openConnection(new URL(url));
			try {
				c.setInstanceFollowRedirects(false);
				c.setUseCaches(false);
				c.setConnectTimeout(10000);
				c.setReadTimeout(15000);
				c.setRequestProperty("User-Agent", userAgent);
				c.setRequestProperty("Accept-Language", acceptLanguage);
				c.setRequestProperty("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8");
				URI uri = URI.create(url);
				for (Map.Entry<String, List<String>> h : cookies.get(uri, Collections.emptyMap()).entrySet()) {
					if (!h.getValue().isEmpty()) {
						c.setRequestProperty(h.getKey(), String.join("; ", h.getValue()));
					}
				}
				for (Map.Entry<String, String> h : headers.entrySet()) {
					c.setRequestProperty(h.getKey(), h.getValue());
				}
				c.setRequestMethod(method);
				if (body != null) {
					c.setDoOutput(true);
					c.setRequestProperty("Content-Type", contentType);
					try (OutputStream o = c.getOutputStream()) {
						o.write(body);
					}
				}
				int code = c.getResponseCode();
				cookies.put(uri, c.getHeaderFields());
				String loc = c.getHeaderField("Location");
				if (code >= 300 && code < 400 && loc != null) {
					url = uri.resolve(loc.trim().replace(" ", "%20")).toString();
					if (code != 307 && code != 308) {
						method = "GET";
						body = null;
					}
					continue;
				}
				return new Page(url, code, read(c, code), chain);
			} finally {
				c.disconnect();
			}
		}
		throw new IOException("too many redirects from " + chain.get(0));
	}

	/** One response as it came, for the browser stage (it handles redirects and cookies itself). */
	static final class Raw {
		/** Where it came from (the last URL when redirects were followed). */
		String url;
		final int code;
		final String reason;
		final Map<String, List<String>> headers;
		final byte[] data;

		Raw(int code, String reason, Map<String, List<String>> headers, byte[] data) {
			this.code = code;
			this.reason = reason;
			this.headers = headers;
			this.data = data;
		}

		String header(String name) {
			for (Map.Entry<String, List<String>> h : headers.entrySet()) {
				if (name.equalsIgnoreCase(h.getKey()) && !h.getValue().isEmpty()) {
					return h.getValue().get(0);
				}
			}
			return null;
		}
	}

	/**
	 * One request without following redirects and without this object's cookie jar: headers (Cookie included) as
	 * given, plus our user agent and language, never anything that names the app.
	 */
	Raw raw(String method, String url, Map<String, String> headers, byte[] body) throws IOException {
		HttpURLConnection c = (HttpURLConnection) network.openConnection(new URL(url));
		try {
			c.setInstanceFollowRedirects(false);
			c.setUseCaches(false);
			c.setConnectTimeout(10000);
			c.setReadTimeout(15000);
			for (Map.Entry<String, String> h : headers.entrySet()) {
				c.setRequestProperty(h.getKey(), h.getValue());
			}
			c.setRequestProperty("User-Agent", userAgent);
			c.setRequestProperty("Accept-Language", acceptLanguage);
			c.setRequestMethod(method);
			if (body != null) {
				c.setDoOutput(true);
				try (OutputStream o = c.getOutputStream()) {
					o.write(body);
				}
			}
			int code = c.getResponseCode();
			InputStream in = code >= 400 ? c.getErrorStream() : c.getInputStream();
			ByteArrayOutputStream out = new ByteArrayOutputStream();
			if (in != null) {
				try (InputStream i = in) {
					byte[] buf = new byte[16384];
					for (int n; (n = i.read(buf)) > 0 && out.size() < MAX_BODY;) {
						out.write(buf, 0, n);
					}
				}
			}
			Map<String, List<String>> h = new java.util.LinkedHashMap<>();
			for (Map.Entry<String, List<String>> e : c.getHeaderFields().entrySet()) {
				if (e.getKey() != null) {
					h.put(e.getKey(), e.getValue());
				}
			}
			String reason = c.getResponseMessage();
			return new Raw(code, reason == null || reason.isEmpty() ? "OK" : reason, h, out.toByteArray());
		} finally {
			c.disconnect();
		}
	}

	private static String read(HttpURLConnection c, int code) throws IOException {
		InputStream in = code >= 400 ? c.getErrorStream() : c.getInputStream();
		if (in == null) {
			return "";
		}
		ByteArrayOutputStream out = new ByteArrayOutputStream();
		try (InputStream i = in) {
			byte[] buf = new byte[16384];
			for (int n; (n = i.read(buf)) > 0 && out.size() < MAX_BODY;) {
				out.write(buf, 0, n);
			}
		}
		Charset cs = StandardCharsets.UTF_8;
		String type = c.getContentType();
		Matcher m = type == null ? null : CHARSET.matcher(type);
		if (m != null && m.find() && Charset.isSupported(m.group(1))) {
			cs = Charset.forName(m.group(1));
		}
		return out.toString(cs.name());
	}
}
