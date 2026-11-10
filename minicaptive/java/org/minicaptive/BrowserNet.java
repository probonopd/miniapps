/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
package org.minicaptive;

import android.webkit.CookieManager;
import android.webkit.JavascriptInterface;
import android.webkit.WebResourceRequest;
import android.webkit.WebResourceResponse;
import java.io.ByteArrayInputStream;
import java.io.IOException;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.util.Collections;
import java.util.HashMap;
import java.util.HashSet;
import java.util.Iterator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Set;
import org.json.JSONException;
import org.json.JSONObject;

/**
 * All network traffic of the browser stage, done by our own HTTP client instead of the WebView's: the WebView
 * names the app in an X-Requested-With header on every request it makes, and that cannot be switched off.
 * Page loads (GET) are intercepted; POSTs, which an app cannot read the body of, are handed over by a script
 * put into every page (forms, fetch, XMLHttpRequest). Whatever still reaches the WebView's own network is
 * refused: a broken page is better than one that gives the device away.
 */
final class BrowserNet {
	private static final Set<String> DROP_REQUEST = new HashSet<>(java.util.Arrays.asList(
			"x-requested-with", "user-agent", "cookie", "accept-language"));
	/** Hop-by-hop or rewritten by us; CSP would block the script we put into the page. */
	private static final Set<String> DROP_RESPONSE = new HashSet<>(java.util.Arrays.asList(
			"set-cookie", "content-length", "content-encoding", "transfer-encoding", "connection",
			"content-security-policy", "content-security-policy-report-only"));

	private final Http http;
	/** Hosts the portal loaded pages from: the only ones the page script may send POSTs to. */
	private final Set<String> pageHosts = Collections.synchronizedSet(new HashSet<>());
	/** Answers to POSTs made by the page script, served when the page then navigates to them. */
	private final Map<String, Http.Raw> pending = Collections.synchronizedMap(new HashMap<>());
	final Bridge bridge = new Bridge();
	/** Every request of the visit with its answer, kept with a page that defeated us. */
	final List<String> requests = Collections.synchronizedList(new java.util.ArrayList<>());

	BrowserNet(Http http) {
		this.http = http;
	}

	/** null: not ours to answer (data:, blob:, about:). */
	WebResourceResponse intercept(WebResourceRequest r) {
		String url = r.getUrl().toString(), scheme = r.getUrl().getScheme();
		if (!"http".equals(scheme) && !"https".equals(scheme)) {
			return null;
		}
		if (!"GET".equals(r.getMethod())) {
			Journal.log("web stage: refused a direct " + r.getMethod() + " to " + url + " (it would name the app)");
			requests.add(r.getMethod() + " " + url + " -> refused (WebView's own network)");
			return text(405, "refused");
		}
		boolean document = r.isForMainFrame() || accepts(r, "text/html");
		try {
			Http.Raw resp = pending.remove(url);
			if (resp == null) {
				resp = fetch("GET", url, headers(r.getRequestHeaders()), null, !document);
				url = resp.url;
			}
			if (resp.code >= 300 && resp.code < 400 && resp.header("Location") != null) {
				// WebView takes no redirect from an app: a page that moves on by itself does the same.
				String to = URI.create(url).resolve(resp.header("Location").trim().replace(" ", "%20")).toString();
				String q = JSONObject.quote(to);
				return html("<!doctype html><meta http-equiv=\"refresh\" content=\"0;url=" + to.replace("\"", "%22")
						+ "\"><script>location.replace(" + q + ")</script>");
			}
			return response(url, resp);
		} catch (IOException e) {
			Journal.log("web stage: " + url + ": " + e);
			return text(502, "no answer");
		}
	}

	private static boolean accepts(WebResourceRequest r, String type) {
		for (Map.Entry<String, String> h : r.getRequestHeaders().entrySet()) {
			if (h.getKey().equalsIgnoreCase("Accept") && h.getValue().startsWith(type)) {
				return true;
			}
		}
		return false;
	}

	private static Map<String, String> headers(Map<String, String> in) {
		Map<String, String> out = new LinkedHashMap<>();
		for (Map.Entry<String, String> h : in.entrySet()) {
			if (!DROP_REQUEST.contains(h.getKey().toLowerCase(Locale.ROOT))) {
				out.put(h.getKey(), h.getValue());
			}
		}
		return out;
	}

	/**
	 * One request with the WebView's cookies (scripts set them there too), its new cookies stored back; redirects
	 * followed when follow is set (subresources, fetch), else returned for the page to follow.
	 */
	private Http.Raw fetch(String method, String url, Map<String, String> headers, byte[] body, boolean follow)
			throws IOException {
		CookieManager cm = CookieManager.getInstance();
		for (int hop = 0; hop < 10; hop++) {
			Map<String, String> h = new LinkedHashMap<>(headers);
			String c = cm.getCookie(url);
			if (c != null && !c.isEmpty()) {
				h.put("Cookie", c);
			}
			Http.Raw r;
			try {
				r = http.raw(method, url, h, body);
			} catch (IOException e) {
				requests.add(method + " " + url + " -> " + e);
				throw e;
			}
			requests.add(method + " " + url + " -> " + r.code + " " + r.header("Content-Type"));
			for (Map.Entry<String, List<String>> e : r.headers.entrySet()) {
				if (e.getKey().equalsIgnoreCase("Set-Cookie")) {
					for (String v : e.getValue()) {
						cm.setCookie(url, v);
					}
				}
			}
			cm.flush();
			r.url = url;
			String loc = r.header("Location");
			if (!follow || r.code < 300 || r.code >= 400 || loc == null) {
				return r;
			}
			url = URI.create(url).resolve(loc.trim().replace(" ", "%20")).toString();
			if (r.code != 307 && r.code != 308) {
				method = "GET";
				body = null;
				headers.remove("Content-Type");
			}
		}
		throw new IOException("too many redirects");
	}

	private WebResourceResponse response(String url, Http.Raw r) {
		String type = r.header("Content-Type"), mime = "application/octet-stream", charset = null;
		if (type != null) {
			String[] p = type.split(";");
			mime = p[0].trim().toLowerCase(Locale.ROOT);
			for (int i = 1; i < p.length; i++) {
				String kv = p[i].trim();
				if (kv.toLowerCase(Locale.ROOT).startsWith("charset=")) {
					charset = kv.substring(8).replace("\"", "");
				}
			}
		}
		byte[] data = r.data;
		if (mime.equals("text/html")) {
			pageHosts.add(URI.create(url).getHost());
			data = withShim(data);
		}
		Map<String, String> h = new LinkedHashMap<>();
		for (Map.Entry<String, List<String>> e : r.headers.entrySet()) {
			if (!DROP_RESPONSE.contains(e.getKey().toLowerCase(Locale.ROOT))) {
				h.put(e.getKey(), String.join(", ", e.getValue()));
			}
		}
		// WebResourceResponse takes no 3xx (handled before) and no 1xx.
		int code = r.code < 200 ? 200 : r.code;
		return new WebResourceResponse(mime, charset, code, r.reason, h, new ByteArrayInputStream(data));
	}

	/** The script goes first into the head, before any of the page's own scripts can send something. */
	private static byte[] withShim(byte[] page) {
		// ISO-8859-1 maps bytes one to one: the page's own encoding stays untouched.
		String s = new String(page, StandardCharsets.ISO_8859_1);
		String tag = "<script>" + SHIM + "</script>";
		java.util.regex.Matcher m = java.util.regex.Pattern.compile("<head[^>]*>", java.util.regex.Pattern.CASE_INSENSITIVE)
				.matcher(s);
		s = m.find() ? s.substring(0, m.end()) + tag + s.substring(m.end()) : tag + s;
		return s.getBytes(StandardCharsets.ISO_8859_1);
	}

	private static WebResourceResponse text(int code, String msg) {
		return new WebResourceResponse("text/plain", "utf-8", code, msg, Collections.emptyMap(),
				new ByteArrayInputStream(msg.getBytes(StandardCharsets.UTF_8)));
	}

	private static WebResourceResponse html(String page) {
		return new WebResourceResponse("text/html", "utf-8", 200, "OK", Collections.emptyMap(),
				new ByteArrayInputStream(page.getBytes(StandardCharsets.UTF_8)));
	}

	/** What the page script calls. Only towards hosts the portal itself loaded pages from. */
	final class Bridge {
		/** fetch/XMLHttpRequest with a body: the answer as JSON (s status, r reason, u url, h headers, b body). */
		@JavascriptInterface
		public String req(String method, String url, String headersJson, String body) {
			try {
				allowed(url);
				Map<String, String> h = new LinkedHashMap<>();
				JSONObject j = new JSONObject(headersJson);
				for (Iterator<String> k = j.keys(); k.hasNext();) {
					String n = k.next();
					if (!DROP_REQUEST.contains(n.toLowerCase(Locale.ROOT))) {
						h.put(n, j.getString(n));
					}
				}
				Http.Raw r = fetch(method.toUpperCase(Locale.ROOT), url, h, body.getBytes(StandardCharsets.UTF_8), true);
				JSONObject rh = new JSONObject();
				for (Map.Entry<String, List<String>> e : r.headers.entrySet()) {
					if (!DROP_RESPONSE.contains(e.getKey().toLowerCase(Locale.ROOT))) {
						rh.put(e.getKey(), String.join(", ", e.getValue()));
					}
				}
				Journal.log("web stage: " + method + " " + url + " -> " + r.code);
				return new JSONObject().put("s", r.code).put("r", r.reason).put("u", r.url).put("h", rh)
						.put("b", new String(r.data, StandardCharsets.UTF_8)).toString();
			} catch (IOException | JSONException | RuntimeException e) {
				Journal.log("web stage: " + method + " " + url + ": " + e.getMessage());
				return "{\"e\":" + JSONObject.quote(String.valueOf(e.getMessage())) + "}";
			}
		}

		/** A form POST: done here, the page then navigates to the returned URL and gets this answer. */
		@JavascriptInterface
		public String nav(String url, String body, String contentType) {
			try {
				allowed(url);
				Map<String, String> h = new LinkedHashMap<>();
				h.put("Content-Type", contentType);
				h.put("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8");
				Http.Raw r = fetch("POST", url, h, body.getBytes(StandardCharsets.UTF_8), true);
				Journal.log("web stage: form POST " + url + " -> " + r.code + " " + r.url);
				pending.put(r.url, r);
				return r.url;
			} catch (IOException | RuntimeException e) {
				Journal.log("web stage: form POST " + url + ": " + e.getMessage());
				return "";
			}
		}

		private void allowed(String url) throws IOException {
			String host = URI.create(url).getHost();
			String scheme = URI.create(url).getScheme();
			if (!("http".equals(scheme) || "https".equals(scheme)) || !pageHosts.contains(host)) {
				throw new IOException("not a portal host: " + host);
			}
		}
	}

	/** ES5 (old WebViews): routes every request with a body through the bridge. */
	private static final String SHIM = "(function(){if(window.__mcShim||!window.__mc){return;}window.__mcShim=1;"
			+ "var B=window.__mc;"
			+ "function abs(u){var a=document.createElement('a');a.href=u;return a.href;}"
			+ "function str(b){if(b==null){return '';}if(typeof b=='string'){return b;}"
			+ "if(window.URLSearchParams&&b instanceof URLSearchParams){return b.toString();}"
			+ "if(window.FormData&&b instanceof FormData){var p=new URLSearchParams();"
			+ "b.forEach(function(v,k){if(typeof v=='string'){p.append(k,v);}});return p.toString();}return String(b);}"
			+ "function form(b){return (window.URLSearchParams&&b instanceof URLSearchParams)||(window.FormData&&b instanceof FormData);}"
			+ "if(window.fetch){var of=window.fetch;window.fetch=function(i,o){o=o||{};"
			+ "var m=String(o.method||(i&&i.method)||'GET').toUpperCase();if(m=='GET'||m=='HEAD'){return of.apply(this,arguments);}"
			+ "var h={};if(o.headers){if(o.headers.forEach&&!(o.headers instanceof Array)){o.headers.forEach(function(v,k){h[k]=v;});}"
			+ "else{for(var k in o.headers){h[k]=o.headers[k];}}}"
			+ "if(form(o.body)&&!h['Content-Type']&&!h['content-type']){h['Content-Type']='application/x-www-form-urlencoded;charset=UTF-8';}"
			+ "var r=JSON.parse(B.req(m,abs(typeof i=='string'?i:i.url),JSON.stringify(h),str(o.body)));"
			+ "if(r.e){return Promise.reject(new TypeError(r.e));}"
			+ "var nb=r.s==204||r.s==205||r.s==304;return Promise.resolve(new Response(nb?null:r.b,{status:r.s,statusText:r.r,headers:r.h}));};}"
			+ "var X=XMLHttpRequest.prototype,oo=X.open,os=X.send,oh=X.setRequestHeader;"
			+ "X.open=function(m,u){this.__m=String(m).toUpperCase();this.__u=abs(u);this.__h={};return oo.apply(this,arguments);};"
			+ "X.setRequestHeader=function(k,v){if(this.__h){this.__h[k]=v;}return oh.apply(this,arguments);};"
			+ "X.send=function(b){if(!this.__m||this.__m=='GET'||this.__m=='HEAD'){return os.apply(this,arguments);}"
			+ "var x=this;if(form(b)&&!x.__h['Content-Type']){x.__h['Content-Type']='application/x-www-form-urlencoded;charset=UTF-8';}"
			+ "var r=JSON.parse(B.req(x.__m,x.__u,JSON.stringify(x.__h),str(b)));"
			+ "function d(k,v){Object.defineProperty(x,k,{value:v,configurable:true});}"
			+ "if(r.e){d('readyState',4);d('status',0);setTimeout(function(){x.dispatchEvent(new Event('readystatechange'));"
			+ "x.dispatchEvent(new Event('error'));x.dispatchEvent(new Event('loadend'));},0);return;}"
			+ "d('readyState',4);d('status',r.s);d('statusText',r.r);d('responseText',r.b);d('responseURL',r.u);"
			+ "var j=null;if(x.responseType=='json'){try{j=JSON.parse(r.b);}catch(e){}}d('response',x.responseType=='json'?j:r.b);"
			+ "x.getResponseHeader=function(n){n=n.toLowerCase();for(var k in r.h){if(k.toLowerCase()==n){return r.h[k];}}return null;};"
			+ "x.getAllResponseHeaders=function(){var s='';for(var k in r.h){s+=k+': '+r.h[k]+'\\r\\n';}return s;};"
			+ "setTimeout(function(){x.dispatchEvent(new Event('readystatechange'));x.dispatchEvent(new Event('load'));"
			+ "x.dispatchEvent(new Event('loadend'));},0);};"
			+ "function post(f,s){if(String(f.getAttribute('method')||'GET').toUpperCase()!='POST'){return false;}"
			+ "var fd=new FormData(f);if(s&&s.name){fd.append(s.name,s.value||'');}"
			+ "var a=abs((s&&s.getAttribute('formaction'))||f.getAttribute('action')||location.href);"
			+ "var u=B.nav(a,str(fd),'application/x-www-form-urlencoded');"
			+ "if(u){var t=f.getAttribute('target');((t=='_top')?top:(t=='_parent')?parent:window).location.assign(u);}return true;}"
			+ "document.addEventListener('submit',function(e){if(!e.defaultPrevented&&post(e.target,e.submitter)){e.preventDefault();}},false);"
			+ "var fs=HTMLFormElement.prototype.submit;HTMLFormElement.prototype.submit=function(){if(!post(this,null)){fs.call(this);}};"
			+ "})();";
}
