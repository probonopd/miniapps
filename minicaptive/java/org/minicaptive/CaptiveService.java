/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */
package org.minicaptive;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.Service;
import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;
import android.content.IntentFilter;
import android.net.ConnectivityManager;
import android.net.Network;
import android.net.NetworkCapabilities;
import android.net.NetworkRequest;
import android.net.wifi.WifiManager;
import android.os.Build;
import android.os.IBinder;
import android.os.PowerManager;
import java.io.File;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.text.SimpleDateFormat;
import java.util.Arrays;
import java.util.Date;
import java.util.HashMap;
import java.util.HashSet;
import java.util.Locale;
import java.util.Map;
import java.util.Set;

/**
 * The loop (after OpenWrt's travelmate), for a traveler walking through stations and airports:
 * <ul>
 * <li>check over the WLAN; online -> wait; portal -> WISPr, forms, WebView;</li>
 * <li>chosen WLAN: join it whenever it is in range, start over on it (new MAC) when its portal refuses us;</li>
 * <li>any open WLAN: stay while it works, move to a much stronger one when the signal gets weak, try a refusing
 * one once more as a new device, then the other open WLANs in range, strongest first;</li>
 * <li>"no answer" (a tunnel, the hotspot's uplink down) is not a refusal: no new MAC, only a short pause;</li>
 * <li>check at once when a WLAN connects, the screen goes on, or a new candidate comes into range.</li>
 * </ul>
 */
public final class CaptiveService extends Service {
	static final String ACTION_CHECK = "org.minicaptive.CHECK";
	private static final int KEEP_PAGES = 20;
	private static final long JOIN_TIMEOUT = 30000L;
	/** Open WLANs tried per round; each costs up to half a minute plus its portal. */
	private static final int MAX_HOPS = 6;
	/** Starting over makes the WLAN reconnect, and a reconnect wakes the loop: without a pause that feeds itself. */
	private static final long START_OVER_PAUSE = 5 * 60000L;
	/** A WLAN without any answer is left out briefly (the uplink may be back soon), a refusing one for longer. */
	private static final long NO_ANSWER_FOR = 5 * 60000L;
	private static final long REFUSED_FOR = 30 * 60000L;
	/** Below this the current WLAN is about to go (walking away); a candidate must be this much stronger. */
	private static final int WEAK_DBM = -78;
	private static final int STRONGER_BY_DB = 15;
	private static final long UPGRADE_PAUSE = 5 * 60000L;
	/** Walking around: retry soon, the next WLAN may be in range a minute later. */
	private static final long MAX_RETRY = 3 * 60000L;
	private static final long MAX_IDLE = 10 * 60000L;
	/** Set by a round that found no WLAN worth trying. */
	private boolean nothingInRange;

	enum Outcome { ONLINE, REFUSED, NO_ANSWER, NO_WLAN, CLOSED }

	/** When the last portal said it is closed (opening hours), when it opens again. */
	private long closedUntil;

	private final Object wake = new Object();
	private boolean poked;
	private volatile boolean running;
	private volatile Config current;
	private Thread loop;
	private ConnectivityManager.NetworkCallback callback;
	private BroadcastReceiver receiver;
	private PowerManager.WakeLock wakeLock;
	private String lastSeen = "";
	private long lastStartOver;
	private long lastUpgrade;
	private final Map<String, Long> bad = new HashMap<>();
	private final Map<String, Integer> refusals = new HashMap<>();
	/** WLANs that got us online since the service started, and those already retried with a new MAC since. */
	private final Set<String> worked = new HashSet<>();
	private final Set<String> renewed = new HashSet<>();
	private Set<String> inRange = new HashSet<>();

	/** The switch on the screen: a file, so it holds across reboots and the boot receiver can read it. */
	static boolean enabled(Context ctx) {
		return new File(ctx.getFilesDir(), "enabled").exists();
	}

	static void setEnabled(Context ctx, boolean on) throws IOException {
		File f = new File(ctx.getFilesDir(), "enabled");
		if (on ? !f.exists() && !f.createNewFile() : f.exists() && !f.delete()) {
			throw new IOException("cannot switch " + (on ? "on" : "off"));
		}
		Journal.log(on ? "switched on" : "switched off");
		if (on) {
			start(ctx);
		} else {
			ctx.stopService(new Intent(ctx, CaptiveService.class));
			Journal.state("off");
		}
	}

	/** Does nothing while switched off. */
	static void start(Context ctx) {
		if (!enabled(ctx)) {
			return;
		}
		Intent i = new Intent(ctx, CaptiveService.class);
		if (Build.VERSION.SDK_INT >= 26) {
			ctx.startForegroundService(i);
		} else {
			ctx.startService(i);
		}
	}

	/** Runs a round now instead of at the end of the wait. */
	static void check(Context ctx) {
		if (!enabled(ctx)) {
			return;
		}
		start(ctx.getApplicationContext());
		ctx.startService(new Intent(ctx, CaptiveService.class).setAction(ACTION_CHECK));
	}

	@Override
	public void onCreate() {
		super.onCreate();
		Journal.init(this);
		Http.init(this);
		startForeground(1, notification());
		// START_STICKY brings the service back after the process died, also when it was switched off meanwhile.
		if (!enabled(this)) {
			stopSelf();
			return;
		}
		// A portal round must not be cut off by the CPU going to sleep in a pocket.
		wakeLock = getSystemService(PowerManager.class).newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "minicaptive:round");
		wakeLock.setReferenceCounted(false);
		if (Journal.fileOn()) {
			try {
				exportPages(this);
			} catch (IOException e) {
				Journal.log("kept pages not moved: " + e.getMessage());
			}
		}
		running = true;
		loop = new Thread(this::run, "minicaptive");
		loop.start();
		callback = new ConnectivityManager.NetworkCallback() {
			@Override
			public void onAvailable(Network n) {
				Journal.log("WLAN connected");
				poke();
			}

			@Override
			public void onLost(Network n) {
				Journal.log("WLAN lost");
				poke();
			}

			@Override
			public void onCapabilitiesChanged(Network n, NetworkCapabilities c) {
				String f = Wlan.flags(c);
				if (!f.equals(lastSeen)) {
					lastSeen = f;
					Journal.log("android says: " + f);
				}
			}
		};
		getSystemService(ConnectivityManager.class).registerNetworkCallback(
				new NetworkRequest.Builder().addTransportType(NetworkCapabilities.TRANSPORT_WIFI).build(), callback);
		receiver = new BroadcastReceiver() {
			@Override
			public void onReceive(Context c, Intent i) {
				if (WifiManager.SCAN_RESULTS_AVAILABLE_ACTION.equals(i.getAction())) {
					// Runs on the main thread: an exception here would end the service, not just this check.
					try {
						scanned();
					} catch (RuntimeException e) {
						Journal.log("scan results: " + e);
					}
				} else {
					// The traveler takes the phone out: that is when the Internet is wanted, after a sleep that
					// may have paused the loop.
					Journal.log("screen on");
					poke();
				}
			}
		};
		IntentFilter f = new IntentFilter(Intent.ACTION_SCREEN_ON);
		f.addAction(Intent.ACTION_USER_PRESENT);
		f.addAction(WifiManager.SCAN_RESULTS_AVAILABLE_ACTION);
		registerReceiver(receiver, f);
	}

	/** While not online: a candidate that just came into range (the chosen WLAN, an untried open one) is worth a round now. */
	@SuppressWarnings("deprecation")
	private void scanned() {
		Config cfg = current;
		if (cfg == null || Journal.state().equals("online")) {
			return;
		}
		Wlan wlan = new Wlan(this);
		Set<String> now = new HashSet<>();
		if (!cfg.ssid.isEmpty()) {
			if (wlan.level(cfg.ssid) != null) {
				now.add(cfg.ssid);
			}
		} else {
			for (android.net.wifi.ScanResult r : wlan.openInRange()) {
				if (!isBad(r.SSID) && !cfg.skipped(r.SSID)) {
					now.add(r.SSID);
				}
			}
		}
		Set<String> added = new HashSet<>(now);
		added.removeAll(inRange);
		inRange = now;
		if (!added.isEmpty()) {
			Journal.log("came into range: " + added);
			poke();
		}
	}

	@Override
	public int onStartCommand(Intent intent, int flags, int startId) {
		if (intent != null && ACTION_CHECK.equals(intent.getAction())) {
			poke();
		}
		return START_STICKY;
	}

	@Override
	public void onDestroy() {
		running = false;
		if (loop != null) {
			getSystemService(ConnectivityManager.class).unregisterNetworkCallback(callback);
			unregisterReceiver(receiver);
			loop.interrupt();
		}
		super.onDestroy();
	}

	@Override
	public IBinder onBind(Intent intent) {
		return null;
	}

	private void poke() {
		synchronized (wake) {
			poked = true;
			wake.notifyAll();
		}
	}

	private void sleep(long ms) throws InterruptedException {
		synchronized (wake) {
			long end = System.currentTimeMillis() + ms;
			while (!poked && System.currentTimeMillis() < end) {
				wake.wait(Math.max(1, end - System.currentTimeMillis()));
			}
			poked = false;
		}
	}

	private void run() {
		Wlan wlan = new Wlan(this);
		int failures = 0;
		try {
			while (running) {
				Config cfg;
				try {
					cfg = Config.load(this);
				} catch (IOException | RuntimeException e) {
					Journal.state("settings broken");
					Journal.result("settings: " + e.getMessage());
					sleep(60000);
					continue;
				}
				current = cfg;
				boolean ok;
				wakeLock.acquire(10 * 60000L);
				try {
					ok = round(wlan, cfg, failures);
				} catch (IOException | RuntimeException e) {
					Journal.log("round failed: " + e);
					ok = false;
				} finally {
					wakeLock.release();
				}
				failures = ok ? 0 : failures + 1;
				// Nothing to try in range: Android's own scans wake the service when that changes, so no need to
				// come back soon (battery).
				long cap = nothingInRange ? MAX_IDLE : MAX_RETRY;
				long wait = ok ? cfg.interval * 1000L : Math.min(cap, 15000L << Math.min(failures - 1, 5));
				if (!ok) {
					Journal.log("retry in " + wait / 1000 + " s (failure " + failures + ")");
				}
				sleep(wait);
			}
		} catch (InterruptedException e) {
			Journal.log("stopped");
		}
	}

	/** One check, and what it takes to get online again. true: online at the end. */
	private boolean round(Wlan wlan, Config cfg, int failures) throws IOException, InterruptedException {
		nothingInRange = false;
		// Switched off by the user (or airplane mode): their decision, not ours to undo.
		if (!wlan.enabled()) {
			Journal.state("WLAN switched off");
			return false;
		}
		Network net = wlan.network();
		// A WLAN with a password (home, office) has no portal to pass: stay out of its way entirely, unless the
		// user chose exactly that WLAN.
		String cur = wlan.currentSsid();
		if (!cur.equals(cfg.ssid) && wlan.connectedProtected()) {
			Journal.log("minicaptive switched off: connected to \"" + cur + "\", a WLAN with a password");
			// Android 12+ shows two lines of a toast at most: the reason first, short.
			String why = "Off: " + cur + " has a password";
			new android.os.Handler(android.os.Looper.getMainLooper()).post(
					() -> android.widget.Toast.makeText(getApplicationContext(), why, android.widget.Toast.LENGTH_LONG).show());
			setEnabled(this, false);
			return true;
		}
		Journal.log("--- check (" + (cfg.ssid.isEmpty() ? "any open WLAN" : "only \"" + cfg.ssid + "\"") + "): "
				+ wlan.describe(net));
		if (Journal.fileOn()) {
			Journal.log(Geo.openList(wlan));
		}
		Geo.log(this, wlan, net);
		return cfg.ssid.isEmpty() ? anyWlan(wlan, cfg, net, failures) : chosenWlan(wlan, cfg, net, failures);
	}

	/**
	 * Only the chosen WLAN: joined whenever it is in range (out of range, Android's own choice is left alone rather
	 * than dropped for nothing), and started over on when its portal refuses us.
	 */
	// ScanResult.SSID: its replacement (getWifiSsid) needs Android 13.
	@SuppressWarnings("deprecation")
	private boolean chosenWlan(Wlan wlan, Config cfg, Network net, int failures) throws IOException, InterruptedException {
		if (!cfg.ssid.equals(wlan.currentSsid())) {
			if (wlan.level(cfg.ssid) == null) {
				wlan.scanAndWait();
			}
			Integer level = wlan.level(cfg.ssid);
			if (level == null) {
				Journal.state("\"" + cfg.ssid + "\" not in range");
				nothingInRange = true;
				for (android.net.wifi.ScanResult r : wlan.fresh()) {
					if (r.SSID.equalsIgnoreCase(cfg.ssid)) {
						Journal.log("\"" + r.SSID + "\" is in range: a different WLAN, its name differs from the chosen \""
								+ cfg.ssid + "\" in case only");
						break;
					}
				}
				return false;
			}
			Journal.state("joining " + cfg.ssid);
			Journal.log("\"" + cfg.ssid + "\" in range, " + level + " dBm");
			if (!wlan.join(cfg.ssid, cfg.bssid, JOIN_TIMEOUT)) {
				return false;
			}
			net = wlan.network();
		}
		Outcome o = online(wlan, cfg, net);
		if (o == Outcome.ONLINE) {
			return true;
		}
		long now = System.currentTimeMillis();
		if (failures >= 1 && o == Outcome.REFUSED && now - lastStartOver >= START_OVER_PAUSE) {
			lastStartOver = now;
			startOver(wlan, cfg, cfg.ssid, cfg.bssid);
		}
		return false;
	}

	/**
	 * Any open WLAN: stay on the current one while it works (moving to a much stronger one when its signal fades);
	 * else try the open WLANs in range, strongest first. Protected WLANs are never tried (Android may still join
	 * saved ones by itself).
	 */
	// ScanResult.SSID: its replacement (getWifiSsid) needs Android 13.
	@SuppressWarnings("deprecation")
	private boolean anyWlan(Wlan wlan, Config cfg, Network net, int failures) throws IOException, InterruptedException {
		String cur = wlan.currentSsid();
		// Still on a WLAN we gave up on (nothing better was in range): no new portal attempt on it, only look around.
		if (net != null && !isBad(cur)) {
			Outcome o = online(wlan, cfg, net);
			if (o == Outcome.ONLINE) {
				succeeded(cur);
				upgrade(wlan, cfg, cur);
				return true;
			}
			// A WLAN that got us online before most likely ended our session (time or volume limit): it gets one
			// more go, as a new device where that is possible.
			if (o == Outcome.REFUSED && worked.contains(cur) && renewed.add(cur)) {
				startOver(wlan, cfg, cur, "");
				return false;
			}
			// All ways through its portal failed: no use asking it again, the next open WLAN may do. "No answer"
			// (a tunnel, the uplink down for a moment) gets a second round first.
			if (o == Outcome.NO_ANSWER && failures < 1) {
				return false;
			}
			markBad(wlan, cur, o);
		}
		wlan.scanAndWait();
		int tried = 0;
		for (android.net.wifi.ScanResult r : wlan.openInRange()) {
			if (isBad(r.SSID) || r.SSID.equals(cur) || cfg.skipped(r.SSID)) {
				continue;
			}
			if (++tried > MAX_HOPS) {
				break;
			}
			Journal.state("trying " + r.SSID);
			Journal.log("open WLAN \"" + r.SSID + "\" " + r.level + " dBm " + r.frequency + " MHz");
			Outcome o = tryJoin(wlan, cfg, r.SSID);
			if (o == Outcome.ONLINE) {
				succeeded(r.SSID);
				Journal.result("online via open WLAN " + r.SSID);
				return true;
			}
			markBad(wlan, r.SSID, o);
		}
		Journal.state(net == null ? "no WLAN" : "no Internet");
		Journal.log(tried == 0 ? "no untried open WLAN in range" : "none of " + tried + " open WLANs got online");
		nothingInRange = tried == 0 && net == null;
		return false;
	}

	/**
	 * Walking away from the current WLAN: when its signal gets weak and an open WLAN in range is much stronger,
	 * try that one; if it does not get online, back to the old one, whose session is still valid.
	 */
	@SuppressWarnings("deprecation")
	private void upgrade(Wlan wlan, Config cfg, String cur) throws IOException, InterruptedException {
		int rssi = wlan.rssi();
		long now = System.currentTimeMillis();
		if (rssi > WEAK_DBM || now - lastUpgrade < UPGRADE_PAUSE) {
			return;
		}
		lastUpgrade = now;
		wlan.scanAndWait();
		for (android.net.wifi.ScanResult r : wlan.openInRange()) {
			if (r.SSID.equals(cur) || isBad(r.SSID) || cfg.skipped(r.SSID) || r.level < rssi + STRONGER_BY_DB) {
				continue;
			}
			Journal.log("\"" + cur + "\" is weak (" + rssi + " dBm), trying stronger \"" + r.SSID + "\" (" + r.level + " dBm)");
			Journal.state("trying " + r.SSID);
			Outcome o = tryJoin(wlan, cfg, r.SSID);
			if (o == Outcome.ONLINE) {
				succeeded(r.SSID);
				Journal.result("moved to the stronger open WLAN " + r.SSID);
				return;
			}
			markBad(wlan, r.SSID, o);
			Journal.log("back to \"" + cur + "\"");
			if (wlan.join(cur, "", JOIN_TIMEOUT)) {
				online(wlan, cfg, wlan.network());
			}
			return;
		}
	}

	/** Join and check; a WLAN Android refuses to add or join costs only itself, not the whole round. */
	private Outcome tryJoin(Wlan wlan, Config cfg, String ssid) throws IOException, InterruptedException {
		try {
			if (!wlan.join(ssid, "", JOIN_TIMEOUT)) {
				return Outcome.NO_WLAN;
			}
		} catch (IOException e) {
			Journal.log("\"" + ssid + "\": " + e.getMessage());
			return Outcome.NO_WLAN;
		}
		return online(wlan, cfg, wlan.network());
	}

	private void succeeded(String ssid) {
		bad.remove(ssid);
		refusals.remove(ssid);
		worked.add(ssid);
		renewed.remove(ssid);
	}

	private boolean isBad(String ssid) {
		Long until = bad.get(ssid);
		return until != null && until > System.currentTimeMillis();
	}

	/**
	 * Leaves ssid out for a while: briefly when it gave no answer at all, for longer (doubling up to four hours)
	 * when its portal refused us. A refusing one this app added is also removed, so it comes back with a new MAC.
	 */
	private void markBad(Wlan wlan, String ssid, Outcome o) {
		if (ssid.isEmpty()) {
			return;
		}
		long d = NO_ANSWER_FOR;
		if (o == Outcome.CLOSED) {
			d = Math.max(NO_ANSWER_FOR, closedUntil - System.currentTimeMillis());
		} else if (o == Outcome.REFUSED) {
			int n = refusals.merge(ssid, 1, Integer::sum);
			d = REFUSED_FOR << Math.min(n - 1, 3);
			wlan.forgetAdded(ssid);
		}
		bad.put(ssid, System.currentTimeMillis() + d);
		Journal.log("\"" + ssid + "\" " + (o == Outcome.CLOSED ? "is closed" : o == Outcome.REFUSED ? "refused us" : o == Outcome.NO_ANSWER
				? "gave no answer" : "could not be joined") + ", skipped for " + d / 60000 + " min");
	}

	/** Probe, and pass the portal if there is one. */
	private Outcome online(Wlan wlan, Config cfg, Network net) throws IOException, InterruptedException {
		if (net == null) {
			Journal.state("no WLAN");
			return Outcome.NO_WLAN;
		}
		String ssid = wlan.currentSsid();
		// Right after an association DHCP may still be running: a probe then fails for nothing.
		for (int i = 0; i < 15 && !wlan.usable(net); i++) {
			Thread.sleep(1000);
		}
		// Android keeps a dropped network (and its last verdict) around for a moment: that is no WLAN.
		if (!wlan.usable(net) || ssid.isEmpty()) {
			Journal.state("no WLAN");
			Journal.log("WLAN not usable (no address, or no longer associated)");
			return Outcome.NO_WLAN;
		}
		Http http = new Http(net);
		// Asking the gateway is for hotspots only: on a WLAN with a password it would be a home router's admin page.
		Probe p = Probe.run(http, cfg, wlan.isOpen(ssid) ? wlan.gateway(net) : null);
		switch (p.state) {
		case ONLINE:
			Journal.state("online");
			Journal.log("Internet: yes via \"" + ssid + "\" (" + p.detail + ")");
			return Outcome.ONLINE;
		case OFFLINE:
			// Android took the network away while we probed (EPERM on binding): that is no WLAN, not "no Internet".
			if (p.detail.contains("EPERM") || !wlan.usable(net)) {
				Journal.state("no WLAN");
				Journal.log("WLAN went away during the check");
				return Outcome.NO_WLAN;
			}
			// Our probes may be blocked where Android's own check got through (a probe= of the wrong network,
			// a filtered host): Android's verdict over the same WLAN is good enough.
			if (wlan.validated(net)) {
				Journal.state("online");
				Journal.log("Internet: yes via \"" + ssid + "\" by Android's check (our probes got no answer: "
						+ p.detail + ")");
				return Outcome.ONLINE;
			}
			Journal.state("no answer");
			Journal.log("Internet: no, and no portal either: " + p.detail);
			return Outcome.NO_ANSWER;
		default:
			break;
		}
		Journal.state("portal");
		Journal.log("captive portal on \"" + ssid + "\": " + p.detail + " (" + p.page.body.length() + " bytes"
				+ (Wispr.block(p.page.body) != null ? ", WISPr" : "") + ")");
		Strategies.Way how = null;
		Strategies mem = new Strategies(this);
		Http.Page page = p.page;
		String keptUrl = page.url, keptHtml = page.body;
		for (Strategies.Way w : mem.order(ssid)) {
			if (w == Strategies.Way.BROWSER && !cfg.webview) {
				continue;
			}
			Journal.log("trying " + w + (w.name().equals(mem.remembered(ssid)) ? " (worked here before)" : ""));
			boolean ok;
			FormPasser form = new FormPasser(http, cfg);
			switch (w) {
			case API:
				ok = form.passApi(page);
				if (form.closedUntil > 0) {
					// Closed (outside opening hours): no other way in exists, so do not try any.
					closedUntil = form.closedUntil;
					Journal.result("\"" + ssid + "\" is closed until "
							+ new SimpleDateFormat("HH:mm", Locale.ROOT).format(new Date(closedUntil)));
					return Outcome.CLOSED;
				}
				break;
			case WISPR: ok = form.passWispr(page); break;
			case FORM:
				ok = form.pass(page);
				keptUrl = form.last.url;
				keptHtml = form.last.body;
				break;
			default:
				WebPasser web = new WebPasser(this, net, cfg, http);
				ok = web.pass(page.chain.get(0));
				keptUrl = web.lastUrl;
				keptHtml = web.lastHtml;
				break;
			}
			if (ok) {
				how = w;
				break;
			}
			// A failed attempt may have moved the portal on (new session, new page): start the next one fresh.
			Probe again = Probe.run(http, cfg, null);
			if (again.state == Probe.State.ONLINE) {
				how = w;
				break;
			}
			if (again.state == Probe.State.PORTAL) {
				page = again.page;
			}
		}
		if (how == null) {
			keep(keptUrl, keptHtml);
			Journal.result("portal not passed at " + p.page.host() + " on \"" + ssid + "\"");
			return Outcome.REFUSED;
		}
		mem.worked(ssid, how);
		Journal.state("online");
		Journal.result("portal passed (" + how + ") at " + p.page.host() + " on \"" + ssid + "\"");
		wlan.reportWorking(net);
		return Outcome.ONLINE;
	}

	/**
	 * A fresh association, with a new MAC where a root helper can give one (apps cannot: Android keeps a fixed
	 * random MAC per WLAN). Never a new MAC for a WLAN with a password.
	 */
	private void startOver(Wlan wlan, Config cfg, String ssid, String bssid) throws IOException, InterruptedException {
		if (cfg.macRotate && wlan.isOpen(ssid) && wlan.requestNewMac()) {
			return;
		}
		Journal.log("starting over on \"" + ssid + "\"");
		wlan.renew(ssid);
		wlan.join(ssid, bssid, JOIN_TIMEOUT);
	}

	/**
	 * Pages that defeated both stages, for writing a rule: with "Log to file" on in /sdcard/minicaptive/pages/
	 * (to read with adb, like the log), else in the app's files/pages/; newest 20. Pages kept privately before
	 * the log was switched on move to the card then.
	 */
	private void keep(String url, String html) throws IOException {
		File priv = new File(getFilesDir(), "pages");
		File dir = Journal.fileOn() ? new File(Journal.FILE.getParentFile(), "pages") : priv;
		if (!dir.isDirectory() && !dir.mkdirs()) {
			throw new IOException("cannot create " + dir);
		}
		String name = new SimpleDateFormat("yyyyMMdd-HHmmss", Locale.ROOT).format(new Date()) + ".html";
		Files.write(new File(dir, name).toPath(), ("<!-- " + url + " -->\n" + html).getBytes(StandardCharsets.UTF_8));
		Journal.log("kept page as " + new File(dir, name));
		if (dir != priv) {
			exportPages(this);
		}
		File[] all = dir.listFiles();
		if (all != null && all.length > KEEP_PAGES) {
			Arrays.sort(all);
			for (int i = 0; i < all.length - KEEP_PAGES; i++) {
				if (!all[i].delete()) {
					throw new IOException("cannot delete " + all[i]);
				}
			}
		}
	}

	/** Pages kept privately (while not logging to file) onto the card, next to the log. */
	static void exportPages(Context ctx) throws IOException {
		File[] old = new File(ctx.getFilesDir(), "pages").listFiles();
		File dir = new File(Journal.FILE.getParentFile(), "pages");
		if (old == null || old.length == 0) {
			return;
		}
		if (!dir.isDirectory() && !dir.mkdirs()) {
			throw new IOException("cannot create " + dir);
		}
		for (File f : old) {
			Files.copy(f.toPath(), new File(dir, f.getName()).toPath(), java.nio.file.StandardCopyOption.REPLACE_EXISTING);
			if (!f.delete()) {
				throw new IOException("cannot delete " + f);
			}
		}
		Journal.log(old.length + " kept pages moved to " + dir);
	}

	// Android 8 and newer show no notification without a channel, and then refuse the foreground service.
	@SuppressWarnings("deprecation")
	private Notification notification() {
		Notification.Builder b;
		if (Build.VERSION.SDK_INT >= 26) {
			NotificationManager nm = getSystemService(NotificationManager.class);
			nm.createNotificationChannel(new NotificationChannel("wlan", "WLAN", NotificationManager.IMPORTANCE_LOW));
			b = new Notification.Builder(this, "wlan");
		} else {
			b = new Notification.Builder(this);
		}
		return b.setContentTitle("minicaptive").setContentText("keeps the WLAN online")
				.setSmallIcon(android.R.drawable.stat_sys_download_done).setOngoing(true).build();
	}
}
