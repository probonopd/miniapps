/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */
package org.minitv;

import android.Manifest;
import android.app.Activity;
import android.content.pm.PackageManager;
import android.graphics.Bitmap;
import android.hardware.camera2.CameraAccessException;
import android.hardware.camera2.CameraManager;
import android.media.AudioDeviceInfo;
import android.media.AudioManager;
import android.net.Uri;
import android.net.http.SslError;
import android.os.Handler;
import android.os.Looper;
import android.os.SystemClock;
import android.text.TextUtils;
import android.util.Log;
import android.webkit.ConsoleMessage;
import android.webkit.PermissionRequest;
import android.webkit.RenderProcessGoneDetail;
import android.webkit.ServiceWorkerClient;
import android.webkit.ServiceWorkerController;
import android.webkit.SslErrorHandler;
import android.webkit.WebChromeClient;
import android.webkit.WebResourceError;
import android.webkit.WebResourceRequest;
import android.webkit.WebResourceResponse;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.FrameLayout;
import java.io.ByteArrayInputStream;
import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.URI;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.util.Map;
import java.util.regex.Matcher;
import java.net.URISyntaxException;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;

/**
 * A Jitsi Meet video call in a fullscreen WebView layer of the TV, started remotely (MQTT cmd/jitsi or the green key).
 * Only rooms on one configured server are ever opened, and camera and microphone are granted to that server only.
 * The WebView exists only during a call: it costs well over 100 MB, which this projector does not have to spare.
 */
final class VideoCall {
	interface Listener {
		void onCallEnded();
	}

	static final String DEFAULT_SERVER = "meet.jit.si";
	// Letters, digits, - _ . and at least one letter or digit, so that "." and ".." can never point elsewhere.
	static final String ROOM = "(?=.*[A-Za-z0-9])[A-Za-z0-9._-]{1,64}";
	// MainActivity is exported (it is the home screen): only intents from this process may start a call.
	static final String TOKEN = java.util.UUID.randomUUID().toString();
	private static final int JOIN_MS = 60000;
	private static final int POLL_MS = 3000;
	// The others have left (or the connection is gone) this long: the call is over.
	private static final int ALONE_MS = 20000;
	// Nobody else came this long after joining: whoever called is not there.
	private static final int NOBODY_MS = 5 * 60000;
	/**
	 * The projector's WebView is Chromium 83 and cannot be updated; the current Jitsi web client calls functions that
	 * came later (String.replaceAll first of all, without which it never connects). These stand-ins go into the room
	 * page before its own scripts.
	 */
	private static final String POLYFILLS =
			// Only the two things that were asked for, and nothing else of the page is touched: Jitsi's logo (top left) and the
			// background image that a server can set on the large video's container (the video itself is a child of it and
			// stays). The watchdog below switches the rules off for good as soon as a playing video is not visible, so that
			// a change of Jitsi's page can never cost the picture.
			"<style id=\"mtv-look\">.watermark,.leftwatermark,.rightwatermark,.poweredby{display:none!important;}"
			+ "#largeVideoContainer{background:linear-gradient(#3c3c3c,#1a1a1a)!important;}</style><script>(function(){"
			+ "var bad=0;setInterval(function(){try{var st=document.getElementById('mtv-look');if(!st||st.disabled)return;"
			// Videos that play, and whether any of them can be seen (the large video is hidden by Jitsi itself in tile view,
			// where the tiles show the pictures). Playing videos but none visible for 8 checks in a row: the rules go.
			+ "var vs=document.querySelectorAll('video'),playing=0,seen=0;for(var i=0;i<vs.length;i++){var v=vs[i];"
			+ "if(v.videoWidth>0&&!v.paused){playing++;var r=v.getBoundingClientRect(),cs=getComputedStyle(v);"
			+ "if(r.width>=2&&r.height>=2&&cs.visibility==='visible'&&cs.display!=='none'&&cs.opacity!=='0')seen++;}}"
			+ "if(playing>0&&seen===0){if(++bad>=8){st.disabled=true;console.error('minitv: look rules off, playing videos are not visible');}}else bad=0;"
			+ "}catch(e){}},1000);"
			+ "function d(o,n,f){if(!(n in o))Object.defineProperty(o,n,{value:f,writable:true,configurable:true});}"
			+ "d(String.prototype,'replaceAll',function(s,r){return this.replace(s instanceof RegExp?s:"
			+ "new RegExp(String(s).replace(/[.*+?^${}()|[\\]\\\\]/g,'\\\\$&'),'g'),r);});"
			+ "function at(i){i=Math.trunc(i)||0;if(i<0)i+=this.length;return this[i];}"
			+ "d(Array.prototype,'at',at);d(String.prototype,'at',at);"
			+ "d(Array.prototype,'findLast',function(f,t){for(var i=this.length-1;i>=0;i--)if(f.call(t,this[i],i,this))return this[i];});"
			+ "d(Array.prototype,'findLastIndex',function(f,t){for(var i=this.length-1;i>=0;i--)if(f.call(t,this[i],i,this))return i;return -1;});"
			+ "d(Object,'hasOwn',function(o,k){return Object.prototype.hasOwnProperty.call(o,k);});"
			+ "d(window,'structuredClone',function(v){return v===undefined?v:JSON.parse(JSON.stringify(v));});"
			+ "d(Promise,'withResolvers',function(){var a,b,p=new this(function(x,y){a=x;b=y;});return{promise:p,resolve:a,reject:b};});"
			+ "d(Promise,'any',function(l){return new Promise(function(ok,no){var n=0,e=[];l=Array.from(l);if(!l.length)no(new Error('none'));"
			+ "l.forEach(function(p,i){Promise.resolve(p).then(ok,function(x){e[i]=x;if(++n===l.length)no(new Error('all rejected'));});});});});"
			+ "d(window,'WeakRef',function(t){this.t=t;});WeakRef.prototype.deref=WeakRef.prototype.deref||function(){return this.t;};"
			+ "d(window,'FinalizationRegistry',function(){this.register=this.unregister=function(){};});"
			+ "})();</script>";

	private final Activity activity;
	private final FrameLayout root;
	private final Listener listener;
	private final Handler handler = new Handler(Looper.getMainLooper());
	private WebView web;
	// Read on the WebView's network thread as well.
	private volatile String url;
	private volatile String host;
	private volatile String agent;
	private boolean camera;
	private boolean microphone;
	private boolean joined;
	private boolean othersSeen;
	private long joinedAt;
	private long aloneSince;
	private float volume = 1f;

	VideoCall(Activity activity, FrameLayout root, Listener listener) {
		this.activity = activity;
		this.root = root;
		this.listener = listener;
	}

	/**
	 * https://<server>/<room> for a room name (on the default server), or for the https address of a room on any
	 * server that "allowed" lets through ("*" for every host name, else a comma separated list of hosts); null for
	 * anything else. Query and fragment of an address are dropped: they could switch the camera on or change the config.
	 */
	static String roomUrl(String payload, String defaultServer, String allowed) {
		String room = payload.trim();
		String server = defaultServer;
		if (room.contains(":")) {
			URI u;
			try {
				u = new URI(room);
			} catch (URISyntaxException e) {
				return null;
			}
			String host = u.getHost() == null ? "" : u.getHost().toLowerCase(java.util.Locale.ROOT);
			if (!"https".equalsIgnoreCase(u.getScheme()) || u.getRawUserInfo() != null || !host.matches(HOST)
					|| !hostAllowed(host, allowed) || (u.getPort() != -1 && u.getPort() != 443) || u.getRawPath() == null) {
				return null;
			}
			server = host;
			room = u.getRawPath().replaceFirst("^/", "").replaceFirst("/$", "");
		}
		return room.matches(ROOM) ? "https://" + server + "/" + room : null;
	}

	// A plain name with dots and letters at the end: no IP address, no localhost, nothing in the local network.
	static final String HOST = "[a-z0-9-]+(\\.[a-z0-9-]+)*\\.[a-z][a-z0-9-]*";

	private static boolean hostAllowed(String host, String allowed) {
		for (String entry : allowed.split(",")) {
			entry = entry.trim().toLowerCase(java.util.Locale.ROOT);
			if (entry.equals("*") || entry.equals(host)) return true;
		}
		return false;
	}

	/** Why roomUrl refused the payload, for the result message. */
	static String rejection(String payload, String allowed) {
		return payload.contains(":") ? "only https addresses of rooms on " + (allowed.trim().equals("*") ? "a host name" : allowed)
				: "a room name has letters, digits, - _ . only (64 at most)";
	}

	private static String roomOf(String url) {
		return url.substring(url.lastIndexOf('/') + 1);
	}

	boolean active() {
		return web != null;
	}

	/**
	 * Opens the room fullscreen as child number index of the root; a call that is already open is replaced. False
	 * when there is no usable WebView (the error is published, the screen stays as it is).
	 */
	boolean start(String roomUrl, String displayName, int index, int maxMinutes) {
		if (web != null) release();
		try {
			web = new WebView(activity);
		} catch (Throwable t) {
			// A missing or broken WebView package throws from the constructor: keep the TV running.
			web = null;
			MqttService.error("jitsi", "WebView not available: " + t, "webview-missing");
			return false;
		}
		url = roomUrl;
		host = Uri.parse(roomUrl).getHost();
		joined = false;
		othersSeen = false;
		aloneSince = 0;
		WebSettings s = web.getSettings();
		s.setJavaScriptEnabled(true);
		s.setDomStorageEnabled(true);
		s.setMediaPlaybackRequiresUserGesture(false);
		// Jitsi sends mobile browsers to a page that offers its app: look like desktop Chrome of the same engine version.
		agent = s.getUserAgentString().replaceFirst("\\([^)]*\\)", "(X11; Linux armv7l)").replace("Version/4.0 ", "")
				.replace(" Mobile", "");
		s.setUserAgentString(agent);
		web.setBackgroundColor(0xFF000000);
		web.setLayerType(android.view.View.LAYER_TYPE_HARDWARE, null);
		web.setWebViewClient(new Client());
		web.setWebChromeClient(new Chrome());
		// Jitsi's service worker answers reloads of the page itself, past shouldInterceptRequest of the WebView.
		ServiceWorkerController.getInstance().setServiceWorkerClient(new ServiceWorkerClient() {
			@Override
			public WebResourceResponse shouldInterceptRequest(WebResourceRequest request) {
				return withPolyfills(request);
			}
		});
		root.addView(web, index, new FrameLayout.LayoutParams(-1, -1));
		camera = hasCamera();
		microphone = hasMicrophone();
		web.loadUrl(roomUrl + fragment(displayName));
		Log.i("minitv", "call: open " + roomUrl);
		handler.postDelayed(joinTimeout, JOIN_MS);
		handler.postDelayed(poll, POLL_MS);
		if (maxMinutes > 0) {
			handler.postDelayed(() -> {
				MqttService.error("jitsi", "call ended after " + maxMinutes + " minutes (jitsi_max_minutes)", "max-minutes");
				hangup("time limit");
			}, maxMinutes * 60000L);
		}
		MqttService.callChanged(roomOf(roomUrl));
		Speech.setInCall(true);
		return true;
	}

	/** Ends the call (if there is one) and frees the WebView; the TV continues. */
	void hangup(String reason) {
		if (web == null) return;
		Log.i("minitv", "call: end (" + reason + ")");
		release();
		MqttService.callChanged(null);
		Speech.setInCall(false);
		listener.onCallEnded();
	}

	/** The gain of the remote voices, 0 to 1: the volume emulation for outputs the system does not scale (USB). */
	void setVolume(float gain) {
		volume = gain;
		if (web == null) return;
		handler.removeCallbacks(poll);
		poll.run();
	}

	private void release() {
		handler.removeCallbacksAndMessages(null);
		WebView w = web;
		web = null;
		root.removeView(w);
		w.stopLoading();
		w.setWebChromeClient(null);
		w.destroy();
	}

	/**
	 * Jitsi takes config overrides from the address fragment: no pre-join page, no app offer, the device's resolution. The page
	 * itself is shown exactly as Jitsi draws it (screen sharing, several parties and the rest work as in a browser).
	 * Without the pre-join page Jitsi joins muted whatever the fragment says (so that a link cannot switch a camera
	 * on unasked); camera and microphone are switched on after joining instead, see onPolled.
	 */
	private String fragment(String displayName) {
		String[] params = { "config.prejoinConfig.enabled=false", "config.prejoinPageEnabled=false",
				"config.startWithVideoMuted=" + !camera, "config.startWithAudioMuted=" + !microphone,
				"config.disableDeepLinking=true", "config.enableWelcomePage=false", "config.enableClosePage=false",
				"config.resolution=480", "config.constraints.video.height.ideal=480", "config.constraints.video.height.max=480",
				// The camera's own 640x480 picture, uncropped and not scaled (a bigger one would be too much to encode here).
				"config.constraints.video.width.ideal=640", "config.constraints.video.width.max=640",
				// The WebView is Chromium 83: it cannot decode what newer browsers send as VP9 (scalable) or AV1, and shows
				// the other party as an avatar then. Everybody in the room is asked for VP8, which it plays.
				"config.videoQuality.codecPreferenceOrder=" + Uri.encode("[\"VP8\"]"),
				"config.videoQuality.mobileCodecPreferenceOrder=" + Uri.encode("[\"VP8\"]"),
				"config.videoQuality.preferredCodec=" + Uri.encode("\"VP8\""),
				"config.videoQuality.screenshareCodec=" + Uri.encode("\"VP8\""),
				"config.videoQuality.disabledCodec=" + Uri.encode("\"VP9\""),
				"userInfo.displayName=" + Uri.encode(org.json.JSONObject.quote(displayName)) };
		return "#" + TextUtils.join("&", params);
	}

	private boolean hasCamera() {
		if (activity.checkSelfPermission(Manifest.permission.CAMERA) != PackageManager.PERMISSION_GRANTED) return false;
		try {
			return ((CameraManager) activity.getSystemService(Activity.CAMERA_SERVICE)).getCameraIdList().length > 0;
		} catch (CameraAccessException e) {
			return false;
		}
	}

	private boolean hasMicrophone() {
		if (activity.checkSelfPermission(Manifest.permission.RECORD_AUDIO) != PackageManager.PERMISSION_GRANTED) return false;
		for (AudioDeviceInfo d : ((AudioManager) activity.getSystemService(Activity.AUDIO_SERVICE))
				.getDevices(AudioManager.GET_DEVICES_INPUTS)) {
			switch (d.getType()) {
			case AudioDeviceInfo.TYPE_USB_HEADSET:
			case AudioDeviceInfo.TYPE_USB_DEVICE:
			case AudioDeviceInfo.TYPE_WIRED_HEADSET:
			// A device with its own microphone (a smart display) has one that apps can use; on the projector the
			// remote's microphone is not listed here, so nothing changes there.
			case AudioDeviceInfo.TYPE_BUILTIN_MIC:
				return true;
			default:
				break;
			}
		}
		return false;
	}

	private final Runnable joinTimeout = () -> {
		MqttService.error("jitsi", "conference not joined within " + JOIN_MS / 1000 + " s", "join-timeout");
		hangup("not joined");
	};

	/**
	 * Every few seconds: applies the volume to the remote voices (new audio elements appear as people join) and reads
	 * whether the conference is joined and how many are in it, to notice when the call is over.
	 */
	private final Runnable poll = new Runnable() {
		@Override
		public void run() {
			if (web == null) return;
			web.evaluateJavascript("(function(g){var a=document.querySelectorAll('audio');for(var i=0;i<a.length;i++)a[i].volume=g;"
					+ "var c=window.APP&&APP.conference;try{return c?(c.isJoined()?c.membersCount:0):-1;}catch(e){return -1;}})("
					+ volume + ")", VideoCall.this::onPolled);
			handler.removeCallbacks(this);
			handler.postDelayed(this, POLL_MS);
		}
	};

	private void onPolled(String value) {
		if (web == null) return;
		int members;
		try {
			members = Integer.parseInt(value);
		} catch (NumberFormatException e) {
			return;
		}
		long now = SystemClock.elapsedRealtime();
		if (members > 0 && !joined) {
			joined = true;
			joinedAt = now;
			handler.removeCallbacks(joinTimeout);
			Log.i("minitv", "call: joined " + url + (camera ? ", camera on" : "") + (microphone ? ", microphone on" : ""));
			// The camera only if there is one, the microphone only if the device lists one for apps (a headset or a built-in microphone;
			// the projector remote's microphone is not available to apps).
			web.evaluateJavascript("APP.conference.muteVideo(" + !camera + ");APP.conference.muteAudio(" + !microphone + ");", null);
		}
		if (!joined) return;
		if (members > 1) {
			othersSeen = true;
			aloneSince = 0;
		} else if (othersSeen) {
			if (aloneSince == 0) aloneSince = now;
			else if (now - aloneSince >= ALONE_MS) hangup("the others left");
		} else if (now - joinedAt >= NOBODY_MS) {
			hangup("nobody else joined");
		}
	}

	private boolean isRoom(Uri u) {
		return "https".equals(u.getScheme()) && host.equalsIgnoreCase(u.getHost()) && Uri.parse(url).getPath().equals(u.getPath());
	}

	/**
	 * The room page itself is fetched here, to put the polyfills in front of Jitsi's scripts; null for every other
	 * request. Called on a network thread.
	 */
	private WebResourceResponse withPolyfills(WebResourceRequest request) {
		if (url == null || !"GET".equals(request.getMethod()) || !isRoom(request.getUrl())) return null;
		try {
			HttpURLConnection c = (HttpURLConnection) new URL(request.getUrl().toString()).openConnection();
			for (Map.Entry<String, String> h : request.getRequestHeaders().entrySet()) {
				// HttpURLConnection only unpacks compressed bodies when it asked for them itself.
				if (!h.getKey().equalsIgnoreCase("Accept-Encoding")) c.setRequestProperty(h.getKey(), h.getValue());
			}
			c.setRequestProperty("User-Agent", agent);
			c.setConnectTimeout(15000);
			c.setReadTimeout(15000);
			// Anything unusual is left to the WebView, which reports it like any failed page.
			if (c.getResponseCode() != HttpURLConnection.HTTP_OK) return null;
			ByteArrayOutputStream out = new ByteArrayOutputStream();
			try (InputStream in = c.getInputStream()) {
				byte[] buffer = new byte[16384];
				for (int n; (n = in.read(buffer)) > 0;) out.write(buffer, 0, n);
			}
			String html = out.toString("UTF-8").replaceFirst("(?i)<head>", "<head>" + Matcher.quoteReplacement(POLYFILLS));
			return new WebResourceResponse("text/html", "utf-8", new ByteArrayInputStream(html.getBytes(StandardCharsets.UTF_8)));
		} catch (IOException e) {
			return null;
		}
	}

	private final class Client extends WebViewClient {
		/** The page stays on the room: other sites are never opened, and leaving the room ends the call. */
		@Override
		public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
			if (!request.isForMainFrame() || isRoom(request.getUrl())) return false;
			if (host.equalsIgnoreCase(request.getUrl().getHost())) handler.post(() -> hangup("left the room"));
			return true;
		}

		@Override
		public WebResourceResponse shouldInterceptRequest(WebView view, WebResourceRequest request) {
			// Not limited to isForMainFrame(): Chromium 83 reports false for the page loaded with loadUrl.
			return withPolyfills(request);
		}

		@Override
		public void onReceivedError(WebView view, WebResourceRequest request, WebResourceError error) {
			if (!request.isForMainFrame()) return;
			MqttService.error("jitsi", "page load failed: " + error.getErrorCode() + " " + error.getDescription(), "load");
			handler.post(() -> hangup("page load failed"));
		}

		@Override
		public void onReceivedHttpError(WebView view, WebResourceRequest request, WebResourceResponse response) {
			if (!request.isForMainFrame()) return;
			MqttService.error("jitsi", "page load failed: HTTP " + response.getStatusCode(), "http");
			handler.post(() -> hangup("page load failed"));
		}

		@Override
		public void onReceivedSslError(WebView view, SslErrorHandler error, SslError e) {
			error.cancel();
			MqttService.error("jitsi", "TLS error " + e.getPrimaryError() + " for " + e.getUrl(), "tls");
		}

		@Override
		public boolean onRenderProcessGone(WebView view, RenderProcessGoneDetail detail) {
			// Returning true keeps the app alive; the dead WebView must not be used again.
			MqttService.error("jitsi", detail.didCrash() ? "WebView renderer crashed" : "WebView renderer killed (low memory)",
					"renderer");
			handler.post(() -> hangup("renderer gone"));
			return true;
		}
	}

	private final class Chrome extends WebChromeClient {
		/** Camera and microphone for the configured server only, nothing else for anyone. */
		@Override
		public void onPermissionRequest(PermissionRequest request) {
			Uri origin = request.getOrigin();
			List<String> granted = new ArrayList<>();
			if ("https".equals(origin.getScheme()) && host.equalsIgnoreCase(origin.getHost())) {
				for (String r : request.getResources()) {
					if (PermissionRequest.RESOURCE_VIDEO_CAPTURE.equals(r) || PermissionRequest.RESOURCE_AUDIO_CAPTURE.equals(r)) {
						granted.add(r);
					}
				}
			}
			if (granted.isEmpty()) {
				request.deny();
				MqttService.error("jitsi", "permission denied: " + Arrays.toString(request.getResources()) + " for " + origin,
						"permission");
			} else {
				// The page asks only for what it uses; without the app's own permission the system hands it nothing.
				if (granted.contains(PermissionRequest.RESOURCE_VIDEO_CAPTURE)
						&& activity.checkSelfPermission(Manifest.permission.CAMERA) != PackageManager.PERMISSION_GRANTED
						|| granted.contains(PermissionRequest.RESOURCE_AUDIO_CAPTURE)
						&& activity.checkSelfPermission(Manifest.permission.RECORD_AUDIO) != PackageManager.PERMISSION_GRANTED) {
					MqttService.error("jitsi", "the app has no camera or microphone permission (pm grant)", "app-permission");
				}
				request.grant(granted.toArray(new String[0]));
			}
		}

		// Without this, every video element shows a gray play symbol until its stream starts.
		@Override
		public Bitmap getDefaultVideoPoster() {
			return Bitmap.createBitmap(1, 1, Bitmap.Config.ARGB_8888);
		}

		@Override
		public boolean onConsoleMessage(ConsoleMessage m) {
			if (m.messageLevel() == ConsoleMessage.MessageLevel.ERROR) Log.w("minitv", "call page: " + m.message());

			return true;
		}
	}
}
