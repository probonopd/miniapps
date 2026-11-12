/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */
package org.minicaptive;

import android.content.Context;
import android.net.ConnectivityManager;
import android.net.Network;
import android.net.LinkAddress;
import android.net.LinkProperties;
import android.net.NetworkCapabilities;
import android.net.RouteInfo;
import android.net.wifi.SupplicantState;
import android.net.wifi.WifiConfiguration;
import android.net.wifi.WifiInfo;
import android.net.wifi.WifiManager;
import android.os.Build;
import android.os.SystemClock;
import java.io.File;
import java.io.IOException;
import java.net.InetAddress;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;

/**
 * The WLAN side: which network the probes go over, getting back onto the configured WLAN, and asking the root
 * helper for a new MAC address. The old WifiManager calls still work for apps with targetSdk 28.
 */
final class Wlan {
	/** Watched by the root helper on rooted devices (Echo Show); apps cannot change the MAC themselves. */
	static final File MAC_REQUEST = new File("/data/media/0/minicaptive/rotate-mac");

	private final ConnectivityManager cm;
	private final WifiManager wm;
	/** WLANs this app added (open ones in "any" mode); only these are removed again. */
	private final java.util.Set<String> added = new java.util.HashSet<>();

	Wlan(Context ctx) {
		cm = ctx.getSystemService(ConnectivityManager.class);
		wm = (WifiManager) ctx.getApplicationContext().getSystemService(Context.WIFI_SERVICE);
	}

	/** The connected WLAN network, validated or not; null when there is none. */
	@SuppressWarnings("deprecation")
	Network network() {
		for (Network n : cm.getAllNetworks()) {
			NetworkCapabilities c = cm.getNetworkCapabilities(n);
			if (c != null && c.hasTransport(NetworkCapabilities.TRANSPORT_WIFI)) {
				return n;
			}
		}
		return null;
	}

	/**
	 * Everything worth knowing later about the WLAN: name, access point, signal, band, the addresses DHCP gave,
	 * and what Android itself thinks (validated = it found the Internet, captive = it saw a portal).
	 */
	@SuppressWarnings("deprecation")
	String describe(Network n) {
		WifiInfo i = wm.getConnectionInfo();
		StringBuilder b = new StringBuilder();
		if (i == null) {
			b.append("no WLAN info");
		} else {
			b.append("WLAN \"").append(ssid(i.getSSID())).append("\" ap ").append(i.getBSSID()).append(' ')
					.append(i.getRssi()).append(" dBm ").append(i.getFrequency()).append(" MHz ")
					.append(i.getLinkSpeed()).append(" Mbit/s, ").append(i.getSupplicantState());
		}
		if (n != null) {
			LinkProperties lp = cm.getLinkProperties(n);
			if (lp != null) {
				List<String> addr = new ArrayList<>(), gw = new ArrayList<>(), dns = new ArrayList<>();
				for (LinkAddress a : lp.getLinkAddresses()) {
					addr.add(a.toString());
				}
				for (RouteInfo r : lp.getRoutes()) {
					if (r.isDefaultRoute() && r.getGateway() != null) {
						gw.add(r.getGateway().getHostAddress());
					}
				}
				for (InetAddress d : lp.getDnsServers()) {
					dns.add(d.getHostAddress());
				}
				b.append(", addresses ").append(addr).append(" gateway ").append(gw).append(" dns ").append(dns);
			}
			b.append(", android: ").append(flags(cm.getNetworkCapabilities(n)));
		}
		return b.toString();
	}

	/** For the status screen: WLAN, access point, addresses, and what Android thinks, one item per line. */
	@SuppressWarnings("deprecation")
	String panel() {
		Network n = network();
		WifiInfo i = wm.getConnectionInfo();
		if (n == null || i == null) {
			return "Not connected to any WLAN" + (wm.isWifiEnabled() ? "" : " (WLAN is switched off)");
		}
		StringBuilder b = new StringBuilder();
		String name = ssid(i.getSSID());
		b.append("WLAN: ").append(name).append(isOpen(name) ? " (open)" : "").append('\n');
		b.append("Access point: ").append(i.getBSSID()).append(", ").append(i.getRssi()).append(" dBm, ")
				.append(i.getFrequency() > 4000 ? "5" : "2.4").append(" GHz, ").append(i.getLinkSpeed())
				.append(" Mbit/s\n");
		LinkProperties lp = cm.getLinkProperties(n);
		if (lp != null) {
			List<String> v4 = new ArrayList<>(), v6 = new ArrayList<>(), gw = new ArrayList<>();
			for (LinkAddress a : lp.getLinkAddresses()) {
				(a.getAddress() instanceof java.net.Inet4Address ? v4 : v6).add(a.toString());
			}
			for (RouteInfo r : lp.getRoutes()) {
				if (r.isDefaultRoute() && r.getGateway() != null && r.getGateway() instanceof java.net.Inet4Address) {
					gw.add(r.getGateway().getHostAddress());
				}
			}
			b.append("IP: ").append(v4.isEmpty() ? "none" : String.join(", ", v4))
					.append(gw.isEmpty() ? "" : ", gateway " + String.join(", ", gw)).append('\n');
			if (!v6.isEmpty()) {
				b.append("IPv6: ").append(v6.size()).append(" address").append(v6.size() > 1 ? "es" : "").append('\n');
			}
		}
		b.append("Android: ").append(flags(cm.getNetworkCapabilities(n)));
		return b.toString();
	}

	static String flags(NetworkCapabilities c) {
		if (c == null) {
			return "gone";
		}
		return (c.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED) ? "validated" : "not validated")
				+ (c.hasCapability(NetworkCapabilities.NET_CAPABILITY_CAPTIVE_PORTAL) ? ", captive portal" : "");
	}

	private static String ssid(String s) {
		return s != null && s.length() > 1 && s.startsWith("\"") ? s.substring(1, s.length() - 1) : String.valueOf(s);
	}

	/** Whether the device is on the WLAN (and access point) the settings ask for. */
	@SuppressWarnings("deprecation")
	boolean onConfigured(Config cfg) {
		if (network() == null) {
			return false;
		}
		WifiInfo i = wm.getConnectionInfo();
		return (cfg.ssid.isEmpty() || cfg.ssid.equals(ssid(i.getSSID())))
				&& (cfg.bssid.isEmpty() || cfg.bssid.equalsIgnoreCase(String.valueOf(i.getBSSID())));
	}

	@SuppressWarnings("deprecation")
	String currentSsid() {
		WifiInfo i = wm.getConnectionInfo();
		return network() == null || i == null ? "" : ssid(i.getSSID());
	}

	/** Open WLANs in range (no WEP, WPA, SAE, OWE or 802.1X), strongest first, one entry per name. */
	// ScanResult.SSID: its replacement (getWifiSsid) needs Android 13.
	@SuppressWarnings("deprecation")
	List<android.net.wifi.ScanResult> openInRange() {
		List<android.net.wifi.ScanResult> out = new ArrayList<>();
		List<String> names = new ArrayList<>();
		for (android.net.wifi.ScanResult r : fresh()) {
			if (!r.SSID.isEmpty() && !OPEN_NOT.matcher(r.capabilities).find() && !names.contains(r.SSID)) {
				names.add(r.SSID);
				out.add(r);
			}
		}
		return out;
	}

	/**
	 * Scan results of the last two minutes, strongest first. Android keeps older ones too, and a WLAN left behind
	 * on a platform is a poor candidate: joining it would only cost the join timeout. When Android has nothing that
	 * recent (scans throttled), those of the last five minutes rather than none.
	 */
	@SuppressWarnings("deprecation")
	List<android.net.wifi.ScanResult> fresh() {
		List<android.net.wifi.ScanResult> out = new ArrayList<>(), older = new ArrayList<>();
		List<android.net.wifi.ScanResult> scan = wm.getScanResults();
		long nowUs = SystemClock.elapsedRealtime() * 1000;
		if (scan != null) {
			for (android.net.wifi.ScanResult r : scan) {
				long age = nowUs - r.timestamp;
				(age < 120_000_000L ? out : age < 300_000_000L ? older : new ArrayList<android.net.wifi.ScanResult>()).add(r);
			}
		}
		if (out.isEmpty()) {
			out = older;
		}
		out.sort((a, b) -> b.level - a.level);
		return out;
	}

	/**
	 * Before choosing a WLAN to try: a scan of our own if Android's last one is older than a minute, and then the
	 * wait for its results (up to 10 s), not a guess at how long a scan takes.
	 */
	@SuppressWarnings("deprecation")
	void scanAndWait() throws InterruptedException {
		long since = SystemClock.elapsedRealtime() * 1000;
		if (!scanIfStale()) {
			return;
		}
		for (int i = 0; i < 20; i++) {
			Thread.sleep(500);
			List<android.net.wifi.ScanResult> scan = wm.getScanResults();
			if (scan != null) {
				for (android.net.wifi.ScanResult r : scan) {
					if (r.timestamp > since) {
						return;
					}
				}
			}
		}
		Journal.log("scan gave no new results within 10 s");
	}

	/** The strongest signal of ssid in range (any security), or null when it is not in range. */
	@SuppressWarnings("deprecation")
	Integer level(String ssid) {
		for (android.net.wifi.ScanResult r : fresh()) {
			if (r.SSID.equals(ssid)) {
				return r.level;
			}
		}
		return null;
	}

	@SuppressWarnings("deprecation")
	int rssi() {
		WifiInfo i = wm.getConnectionInfo();
		return i == null ? -127 : i.getRssi();
	}

	boolean enabled() {
		return wm.isWifiEnabled();
	}

	/** The IPv4 gateway of n, where a portal answers even when its DNS gives nothing away; null if none. */
	String gateway(Network n) {
		LinkProperties lp = n == null ? null : cm.getLinkProperties(n);
		if (lp != null) {
			for (RouteInfo r : lp.getRoutes()) {
				if (r.isDefaultRoute() && r.getGateway() instanceof java.net.Inet4Address) {
					return r.getGateway().getHostAddress();
				}
			}
		}
		return null;
	}

	/** Tells Android the WLAN works now, so it checks again and stops sending everything over mobile data. */
	void reportWorking(Network n) {
		cm.reportNetworkConnectivity(n, true);
	}

	private static final java.util.regex.Pattern OPEN_NOT = java.util.regex.Pattern.compile("WEP|WPA|PSK|SAE|OWE|EAP");

	/**
	 * A scan of our own only when Android's last one is older than a minute (battery): Android scans by itself
	 * while the screen is on and in the background, and its results wake the service when something new shows up.
	 */
	@SuppressWarnings("deprecation")
	boolean scanIfStale() {
		List<android.net.wifi.ScanResult> scan = wm.getScanResults();
		long newest = 0;
		if (scan != null) {
			for (android.net.wifi.ScanResult r : scan) {
				newest = Math.max(newest, r.timestamp);
			}
		}
		if (SystemClock.elapsedRealtime() * 1000 - newest < 60_000_000L) {
			return false;
		}
		return wm.startScan();
	}

	/**
	 * Joins ssid (pinned to bssid unless empty) and waits until Android reports the WLAN connected with an
	 * address, at most timeoutMs. A WLAN that is not saved is added if it is open; a protected one must have been
	 * saved by hand. true: connected.
	 */
	@SuppressWarnings("deprecation")
	boolean join(String ssid, String bssid, long timeoutMs) throws IOException, InterruptedException {
		WifiConfiguration c = saved(ssid);
		if (c == null) {
			if (!isOpen(ssid)) {
				throw new IOException("WLAN " + ssid + " is protected and not saved on this device, or not in range: "
						+ "connect to it once by hand");
			}
			c = new WifiConfiguration();
			c.SSID = "\"" + ssid + "\"";
			c.allowedKeyManagement.set(WifiConfiguration.KeyMgmt.NONE);
			c.networkId = wm.addNetwork(c);
			if (c.networkId < 0) {
				throw new IOException("cannot add open WLAN " + ssid);
			}
			added.add(ssid);
			Journal.log("added open WLAN \"" + ssid + "\"");
		}
		if (!bssid.isEmpty() && !bssid.equalsIgnoreCase(String.valueOf(c.BSSID))) {
			c.BSSID = bssid.toUpperCase(Locale.ROOT);
			if (wm.updateNetwork(c) < 0) {
				throw new IOException("cannot pin " + ssid + " to " + bssid);
			}
		}
		Journal.log("joining \"" + ssid + "\"" + (bssid.isEmpty() ? "" : " via " + bssid));
		if (!wm.enableNetwork(c.networkId, true) || !wm.reconnect()) {
			throw new IOException("WLAN refused to join " + ssid);
		}
		long end = System.currentTimeMillis() + timeoutMs;
		while (System.currentTimeMillis() < end) {
			Thread.sleep(1000);
			Network n = network();
			if (ssid.equals(currentSsid()) && usable(n)) {
				// A new connection, a new visitor: no cookies from before (our HTTP client starts empty anyway).
				WebPasser.forgetAllSoon();
				Journal.log("joined: " + describe(n));
				return true;
			}
		}
		Journal.log("could not join \"" + ssid + "\" within " + timeoutMs / 1000 + " s: " + describe(network()));
		return false;
	}

	/**
	 * Off the WLAN, and if this app added it, also forgets it, so the next join adds it anew (with a new MAC
	 * where Android allows that). A WLAN saved by hand stays as it is.
	 */
	@SuppressWarnings("deprecation")
	void renew(String ssid) {
		// No disconnect(): Android files that as the user turning the WLAN down and then avoids it on its own.
		// Removing our entry ends the connection anyway.
		WifiConfiguration c = saved(ssid);
		if (c != null && added.contains(ssid) && wm.removeNetwork(c.networkId)) {
			added.remove(ssid);
			Journal.log("forgot \"" + ssid + "\" for a new MAC");
		}
	}

	/**
	 * Associated is not usable yet: until DHCP gave an IPv4 (or a global IPv6) address and a default route,
	 * Android refuses sockets on the network (EPERM) and a probe would only report "no answer".
	 */
	boolean usable(Network n) {
		LinkProperties lp = n == null ? null : cm.getLinkProperties(n);
		if (lp == null) {
			return false;
		}
		boolean addr = false, route = false;
		for (LinkAddress a : lp.getLinkAddresses()) {
			addr |= a.getAddress() instanceof java.net.Inet4Address || !a.getAddress().isLinkLocalAddress();
		}
		for (RouteInfo r : lp.getRoutes()) {
			route |= r.isDefaultRoute();
		}
		return addr && route;
	}

	/** Whether Android's own check found the Internet on n. */
	boolean validated(Network n) {
		NetworkCapabilities c = n == null ? null : cm.getNetworkCapabilities(n);
		return c != null && c.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED);
	}

	/** Takes a WLAN this app added off the device again, so Android does not join a useless one by itself later. */
	@SuppressWarnings("deprecation")
	void forgetAdded(String ssid) {
		WifiConfiguration c = saved(ssid);
		if (added.remove(ssid) && c != null && wm.removeNetwork(c.networkId)) {
			Journal.log("removed WLAN \"" + ssid + "\" again");
		}
	}

	@SuppressWarnings("deprecation")
	private WifiConfiguration saved(String ssid) {
		List<WifiConfiguration> list = wm.getConfiguredNetworks();
		if (list != null) {
			for (WifiConfiguration c : list) {
				if (ssid.equals(ssid(c.SSID))) {
					return c;
				}
			}
		}
		return null;
	}

	/**
	 * WLANs to choose from, as [name, label]: those in range first, strongest first, with signal and whether
	 * they need a password; then saved ones out of range. Names differ by case ("WiFi@DB", "WIFI@DB"): the
	 * label shows which one is really here.
	 */
	@SuppressWarnings("deprecation")
	List<String[]> choices() {
		List<String[]> out = new ArrayList<>();
		List<String> seen = new ArrayList<>();
		for (android.net.wifi.ScanResult r : fresh()) {
			if (!r.SSID.isEmpty() && !seen.contains(r.SSID)) {
				seen.add(r.SSID);
				out.add(new String[] { r.SSID, r.SSID + "   " + r.level + " dBm, "
						+ (OPEN_NOT.matcher(r.capabilities).find() ? "password" : "open") });
			}
		}
		List<WifiConfiguration> saved = wm.getConfiguredNetworks();
		if (saved != null) {
			for (WifiConfiguration c : saved) {
				String n = ssid(c.SSID);
				if (!seen.contains(n)) {
					seen.add(n);
					out.add(new String[] { n, n + "   (saved, not in range)" });
				}
			}
		}
		return out;
	}

	/** Whether the connected WLAN needs a password (its saved entry has a key management other than none). */
	@SuppressWarnings("deprecation")
	boolean connectedProtected() {
		WifiInfo i = wm.getConnectionInfo();
		List<WifiConfiguration> list = wm.getConfiguredNetworks();
		if (network() == null || i == null || list == null) {
			return false;
		}
		for (WifiConfiguration c : list) {
			if (c.networkId == i.getNetworkId()) {
				return !c.allowedKeyManagement.get(WifiConfiguration.KeyMgmt.NONE) || c.wepKeys[0] != null;
			}
		}
		return false;
	}

	/** Whether ssid is an open WLAN in range: only public hotspots get a new MAC, never a WLAN with a password. */
	@SuppressWarnings("deprecation")
	boolean isOpen(String ssid) {
		for (android.net.wifi.ScanResult r : openInRange()) {
			if (r.SSID.equals(ssid)) {
				return true;
			}
		}
		return false;
	}

	/** Asks the root helper for a new random MAC; returns false where there is none (phones). */
	boolean requestNewMac() throws IOException {
		File dir = MAC_REQUEST.getParentFile();
		if (dir == null || !dir.isDirectory()) {
			return false;
		}
		if (!MAC_REQUEST.createNewFile() && !MAC_REQUEST.exists()) {
			throw new IOException("cannot write " + MAC_REQUEST);
		}
		Journal.log("new MAC requested");
		return true;
	}
}
