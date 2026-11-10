/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
package org.minicaptive;

import android.annotation.SuppressLint;
import android.content.Context;
import android.net.ConnectivityManager;
import android.net.Network;
import android.net.http.SslError;
import android.os.Handler;
import android.os.Looper;
import android.util.DisplayMetrics;
import android.view.View;
import android.webkit.SslErrorHandler;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import java.util.Map;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicReference;
import org.json.JSONObject;

/**
 * Second stage: portals that only work in a browser (buttons made by JavaScript, logins sent with fetch()). An
 * invisible WebView loads the portal, and a script does what {@link FormPasser} does by hand: tick boxes, fill
 * the asked-for fields, click the most promising button, in every frame. One click per round, then a probe.
 */
final class WebPasser {
	private static final int ROUNDS = 8;

	private final Context ctx;
	private final Network network;
	private final Config cfg;
	private final Http http;
	private final Handler main = new Handler(Looper.getMainLooper());
	private WebView web;
	private BrowserNet net;
	/** The page's HTML at the end, to keep when it failed. */
	String lastHtml = "";
	String lastUrl = "";

	WebPasser(Context ctx, Network network, Config cfg, Http http) {
		this.ctx = ctx;
		this.network = network;
		this.cfg = cfg;
		this.http = http;
	}

	boolean pass(String url) throws InterruptedException {
		ConnectivityManager cm = ctx.getSystemService(ConnectivityManager.class);
		// WebView has no per-request network: the whole process goes over the WLAN while the stage runs.
		cm.bindProcessToNetwork(network);
		try {
			onMain(() -> create(url));
			int idle = 0;
			for (int round = 0; round < ROUNDS; round++) {
				Thread.sleep(4000);
				String did = eval(script(), 10);
				Journal.log("web stage: " + did);
				Thread.sleep(4000);
				if (Probe.run(http, cfg, null).state == Probe.State.ONLINE) {
					return true;
				}
				idle = did.startsWith("\"nothing") ? idle + 1 : 0;
				if (idle >= 2) {
					break;
				}
			}
			lastHtml = unquote(eval("document.documentElement.outerHTML", 10))
					+ "\n<!-- requests of this visit:\n" + String.join("\n", net.requests).replace("--", "- -") + "\n-->";
			lastUrl = unquote(eval("location.href", 5));
			return false;
		} finally {
			onMain(this::destroy);
			cm.bindProcessToNetwork(null);
		}
	}

	/** evaluateJavascript gives JSON: a string comes quoted. */
	private static String unquote(String json) {
		try {
			Object v = new org.json.JSONTokener(json).nextValue();
			return v instanceof String ? (String) v : json;
		} catch (org.json.JSONException e) {
			return json;
		}
	}

	@SuppressLint("SetJavaScriptEnabled")
	private void create(String url) {
		web = new WebView(ctx);
		web.getSettings().setJavaScriptEnabled(true);
		web.getSettings().setDomStorageEnabled(true);
		web.getSettings().setUserAgentString(Http.userAgent);
		// Every visit as a new visitor: nothing the portal stored last time (cookies, storage, cache) is kept.
		forgetAll();
		web.clearCache(true);
		net = new BrowserNet(http);
		web.addJavascriptInterface(net.bridge, "__mc");
		// Service workers would fetch with the WebView's own network, header included: none of that.
		android.webkit.ServiceWorkerController.getInstance().setServiceWorkerClient(new android.webkit.ServiceWorkerClient() {
			@Override
			public android.webkit.WebResourceResponse shouldInterceptRequest(android.webkit.WebResourceRequest r) {
				return new android.webkit.WebResourceResponse("text/plain", "utf-8", 403, "no service workers",
						java.util.Collections.emptyMap(), new java.io.ByteArrayInputStream(new byte[0]));
			}
		});
		web.setWebViewClient(new WebViewClient() {
			// All traffic through our own client: the WebView's would carry X-Requested-With with the app's name.
			@Override
			public android.webkit.WebResourceResponse shouldInterceptRequest(WebView v, android.webkit.WebResourceRequest r) {
				return net.intercept(r);
			}

			@Override
			public void onPageFinished(WebView v, String u) {
				Journal.log("web stage: loaded " + u);
			}

			// A crashed page renderer would otherwise take the whole app (and the service) down with it.
			@Override
			public boolean onRenderProcessGone(WebView v, android.webkit.RenderProcessGoneDetail d) {
				Journal.log("web stage: page renderer gone");
				if (web == v) {
					web = null;
				}
				v.destroy();
				return true;
			}

			// A portal with a broken certificate gets no credentials: fail rather than guess it is the right one.
			@Override
			public void onReceivedSslError(WebView v, SslErrorHandler h, SslError e) {
				Journal.log("web stage: certificate error on " + e.getUrl());
				h.cancel();
			}
		});
		// Never shown, but laid out like a phone screen: hidden elements have no size, and the script skips those.
		DisplayMetrics dm = ctx.getResources().getDisplayMetrics();
		web.measure(View.MeasureSpec.makeMeasureSpec(dm.widthPixels, View.MeasureSpec.EXACTLY),
				View.MeasureSpec.makeMeasureSpec(dm.heightPixels, View.MeasureSpec.EXACTLY));
		web.layout(0, 0, dm.widthPixels, dm.heightPixels);
		web.loadUrl(url);
	}

	/** Cookies and stored data of all portals, gone: called before each browser visit and on every new join. */
	static void forgetAll() {
		android.webkit.CookieManager.getInstance().removeAllCookies(null);
		android.webkit.CookieManager.getInstance().flush();
		android.webkit.WebStorage.getInstance().deleteAllData();
	}

	/** forgetAll from any thread. */
	static void forgetAllSoon() {
		new Handler(Looper.getMainLooper()).post(WebPasser::forgetAll);
	}

	private void destroy() {
		if (web != null) {
			web.stopLoading();
			web.destroy();
			web = null;
		}
	}

	private void onMain(Runnable r) throws InterruptedException {
		CountDownLatch done = new CountDownLatch(1);
		main.post(() -> {
			try {
				r.run();
			} finally {
				done.countDown();
			}
		});
		done.await();
	}

	/** The script's result as JSON text, "timeout" when the page did not answer. */
	private String eval(String js, int seconds) throws InterruptedException {
		AtomicReference<String> out = new AtomicReference<>("timeout");
		CountDownLatch done = new CountDownLatch(1);
		main.post(() -> {
			if (web == null) {
				done.countDown();
				return;
			}
			web.evaluateJavascript(js, v -> {
				out.set(v);
				done.countDown();
			});
		});
		done.await(seconds, TimeUnit.SECONDS);
		return out.get();
	}

	private String script() {
		JSONObject fill = new JSONObject(), person = new JSONObject();
		StringBuilder click = new StringBuilder();
		for (Config.Rule r : cfg.rules) {
			// Host rules are checked in the page, which knows where it is after its redirects.
			if (r.action.equals("fill")) {
				put(fill, r.hostGlob + "\n" + r.arg.substring(0, r.arg.indexOf('=')), r.arg.substring(r.arg.indexOf('=') + 1));
			} else {
				put(fill, r.hostGlob + "\n#click" + click.length(), r.arg);
				click.append('x');
			}
		}
		for (Map.Entry<String, String> p : cfg.person.entrySet()) {
			put(person, p.getKey(), p.getValue());
		}
		return "(" + JS + ")(" + JSONObject.quote(Words.POSITIVE) + "," + JSONObject.quote(Words.NEGATIVE) + "," + JSONObject.quote(Words.MARKETING) + ","
				+ fill + "," + person + "," + JSONObject.quote(cfg.user) + ")";
	}

	private static void put(JSONObject o, String k, String v) {
		try {
			o.put(k, v);
		} catch (org.json.JSONException e) {
			throw new AssertionError(e);
		}
	}

	/** ES5: the Echo Show's WebView is old. */
	private static final String JS = "function(P,N,MK,RULES,PERSON,USER){"
			+ "var pos=new RegExp(P,'i'),neg=new RegExp(N,'i'),mk=new RegExp(MK,'i'),out=[],best=null,bs=0;"
			+ "function glob(g){return new RegExp('^'+g.replace(/[.+?^${}()|[\\]\\\\]/g,'\\\\$&').replace(/\\*/g,'.*')+'$','i');}"
			+ "function docs(d,l){l.push(d);var f=d.querySelectorAll('iframe,frame');for(var i=0;i<f.length;i++){"
			+ "try{if(f[i].contentDocument){docs(f[i].contentDocument,l);}}catch(e){}}return l;}"
			+ "function shown(e){var r=e.getBoundingClientRect(),s=e.ownerDocument.defaultView.getComputedStyle(e);"
			+ "return r.width>0&&r.height>0&&s.visibility!='hidden'&&s.display!='none';}"
			+ "function text(e){return((e.innerText||'')+' '+(e.value||'')+' '+(e.getAttribute('aria-label')||'')+' '"
			+ "+(e.title||'')+' '+(e.alt||'')).replace(/\\s+/g,' ').trim();}"
			+ "function fire(e,t){var v=e.ownerDocument.createEvent('HTMLEvents');v.initEvent(t,true,true);e.dispatchEvent(v);}"
			+ "function set(e,v){if(e.value==v){return;}e.value=v;fire(e,'input');fire(e,'change');out.push('fill '+(e.name||e.id));}"
			// Ad scenes before the login wait for their video to end; a hidden WebView starts none without a tap,
			// muted ones it may (and nothing plays out loud).
			+ "var all=docs(document,[]);all.forEach(function(d){[].forEach.call(d.querySelectorAll('video'),function(v){"
			+ "if(v.paused&&!v.ended){v.muted=true;var p=v.play();if(p&&p.catch){p.catch(function(){});}out.push('play video');}});});"
			+ "all.forEach(function(d){"
			+ "var host=d.location?d.location.hostname:'',clicks=[];"
			+ "Object.keys(RULES).forEach(function(k){var p=k.split('\\n');if(!glob(p[0]).test(host)){return;}"
			+ "if(p[1].indexOf('#click')==0){clicks.push(RULES[k].toLowerCase());return;}"
			+ "var e=d.getElementsByName(p[1])[0]||d.getElementById(p[1]);if(e){set(e,RULES[k]);}});"
			// Boxes, hidden ones included (custom-styled checkboxes hide the real input): native click, then its
			// label, then the property setter that framework-controlled inputs (React) listen to, with events.
			+ "[].forEach.call(d.querySelectorAll('input[type=checkbox],[role=checkbox]'),function(c){"
			+ "var inp=c.tagName=='INPUT';function on(){return inp?c.checked:c.getAttribute('aria-checked')=='true';}"
			+ "if(on()||c.disabled){return;}var lt=(c.name||'')+' '+(c.id||'')+' '+(c.getAttribute('aria-label')||'');"
			+ "var lab=c.id?d.querySelector('label[for=\"'+c.id+'\"]'):null;lab=lab||(c.closest?c.closest('label'):null);"
			// What the box is about: its label, its own text (ARIA boxes), the text right after it, or a small
			// parent; never a big block, which would mix in the neighboring boxes' words.
			+ "var own=inp?'':(c.innerText||''),nx=c.nextSibling?(c.nextSibling.textContent||''):'',"
			+ "pt=c.parentElement?(c.parentElement.innerText||''):'';"
			+ "lt+=' '+(lab?lab.innerText:own?own:nx.trim()?nx:(pt.length<120?pt:''));"
			+ "if(mk.test(lt)&&!c.required&&c.getAttribute('aria-required')!='true'){out.push('leave '+(c.name||c.id));return;}"
			+ "try{c.click();}catch(e){}if(!on()&&lab){lab.click();}"
			+ "if(!on()&&inp){Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'checked').set.call(c,true);"
			+ "fire(c,'click');fire(c,'input');fire(c,'change');}"
			+ "out.push((on()?'tick ':'cannot tick ')+(c.name||c.id||'box'));});"
			+ "[].forEach.call(d.querySelectorAll('input:not([type]),input[type=text],input[type=email]'),function(e){"
			+ "if(e.value||!shown(e)){return;}var h=(e.name+' '+e.id+' '+(e.placeholder||'')+' '+e.type).toLowerCase();"
			+ "if(/user|login|benutzer/.test(h)){if(USER){set(e,USER);}return;}"
			+ "var k=/e-?mail/.test(h)?'email':/room|zimmer|chambre|habitaci/.test(h)?'room':/name/.test(h)?'name':null;"
			+ "if(k&&PERSON[k]){set(e,PERSON[k]);}});"
			+ "[].forEach.call(d.querySelectorAll('button,input[type=submit],input[type=button],input[type=image],a,"
			+ "[role=button],[onclick],div,span'),function(e){if(!shown(e)){return;}var t=text(e),lt=t.toLowerCase(),s=0;"
			+ "if(!t||t.length>80){return;}"
			// Script-made buttons are often a div with a click handler: count it if it looks clickable.
			+ "if((e.tagName=='DIV'||e.tagName=='SPAN')&&!e.onclick&&!e.hasAttribute('onclick')"
			+ "&&e.ownerDocument.defaultView.getComputedStyle(e).cursor!='pointer'&&!/btn|button/i.test(e.className)){return;}"
			+ "for(var i=0;i<clicks.length;i++){if(lt.indexOf(clicks[i])>=0){s=100;}}"
			+ "if(!s){s=(neg.test(t)?-20:0)+(pos.test(t)?10:0);if(s>0&&e.tagName!='A'){s+=3;}}"
			+ "if(e.tagName=='A'&&/^\\s*(#|javascript:void)/.test(e.getAttribute('href')||'')&&!e.onclick){s-=2;}"
			+ "if(s>0&&s>=bs){bs=s;best=e;}});});"
			+ "if(best){out.push('click \"'+text(best)+'\"');best.click();}"
			// Nothing to do: say what is there, so that the log shows what a rule would need.
			+ "if(!out.length){var desc=all.map(function(d){var b=d.body?(d.body.innerText||'').replace(/\\s+/g,' '):'';"
			+ "return (d.location?d.location.pathname:'?')+' ['+d.querySelectorAll('button,a,input,[role=button]').length"
			+ "+' controls] '+b.substring(0,120);}).join(' | ');return 'nothing; '+desc;}"
			+ "return out.join('; ');}";
}
