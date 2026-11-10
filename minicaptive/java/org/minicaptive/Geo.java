/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
package org.minicaptive;

import android.content.Context;
import android.location.Location;
import android.location.LocationManager;
import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.util.List;
import java.util.Locale;
import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

/**
 * Where the device was, for the log (which hotspot was where). Android's own location first; where it has none
 * (no GPS, as on the Echo Show), BeaconDB (beacondb.net: no account, the successor of Mozilla's location service)
 * from the access points in range. Off unless switched on, since BeaconDB learns which access points are near.
 */
final class Geo {
	private static final String BEACONDB = "https://api.beacondb.net/v1/geolocate";
	/** Android's fix is used if not older than this; a new entry at most this often. */
	private static final long FRESH_MS = 5 * 60000L;

	private static long lastLogged;

	private Geo() {
	}

	static boolean on(Context ctx) {
		return new File(ctx.getFilesDir(), "log-location").exists();
	}

	static void setOn(Context ctx, boolean on) throws IOException {
		File f = new File(ctx.getFilesDir(), "log-location");
		if (on ? !f.exists() && !f.createNewFile() : f.exists() && !f.delete()) {
			throw new IOException("cannot switch the location " + (on ? "on" : "off"));
		}
		Journal.log(on ? "location logging on" : "location logging off");
	}

	/** Writes the location to the log, at most every five minutes. Failures are logged, never fatal. */
	static void log(Context ctx, Wlan wlan, android.net.Network net) {
		long now = System.currentTimeMillis();
		if (!on(ctx) || now - lastLogged < FRESH_MS) {
			return;
		}
		lastLogged = now;
		Location best = android(ctx);
		if (best != null && now - best.getTime() < FRESH_MS) {
			Journal.log(String.format(Locale.ROOT, "location: %.5f,%.5f +-%.0f m (Android %s, %d s old)",
					best.getLatitude(), best.getLongitude(), best.getAccuracy(), best.getProvider(),
					(now - best.getTime()) / 1000));
			return;
		}
		try {
			Journal.log(beaconDb(wlan, net));
		} catch (IOException | JSONException e) {
			Journal.log("location: none (Android has no fresh fix, BeaconDB: " + e.getMessage() + ")");
		}
	}

	private static Location android(Context ctx) {
		LocationManager lm = ctx.getSystemService(LocationManager.class);
		Location best = null;
		for (String p : lm.getProviders(true)) {
			Location l;
			try {
				l = lm.getLastKnownLocation(p);
			} catch (SecurityException e) {
				return null;
			}
			if (l != null && (best == null || l.getTime() > best.getTime())) {
				best = l;
			}
		}
		return best;
	}

	/** Over the WLAN if there is one (mobile data may be off), else Android's default; no IP-based guess. */
	@SuppressWarnings("deprecation")
	private static String beaconDb(Wlan wlan, android.net.Network net) throws IOException, JSONException {
		JSONArray aps = new JSONArray();
		for (android.net.wifi.ScanResult r : wlan.fresh()) {
			// "_nomap": the owner asked location services to leave the access point out.
			if (!r.SSID.endsWith("_nomap")) {
				aps.put(new JSONObject().put("macAddress", r.BSSID).put("signalStrength", r.level));
			}
		}
		if (aps.length() < 2) {
			throw new IOException("fewer than two access points in range");
		}
		byte[] body = new JSONObject().put("considerIp", false).put("wifiAccessPoints", aps).toString()
				.getBytes(StandardCharsets.UTF_8);
		URL u = new URL(BEACONDB);
		HttpURLConnection c = (HttpURLConnection) (net != null ? net.openConnection(u) : u.openConnection());
		try {
			c.setConnectTimeout(10000);
			c.setReadTimeout(10000);
			c.setRequestMethod("POST");
			c.setRequestProperty("Content-Type", "application/json");
			// The same user agent as everything else minicaptive sends: nothing that names the app or its maker.
			c.setRequestProperty("User-Agent", Http.userAgent);
			c.setDoOutput(true);
			try (OutputStream o = c.getOutputStream()) {
				o.write(body);
			}
			int code = c.getResponseCode();
			InputStream in = code >= 400 ? c.getErrorStream() : c.getInputStream();
			ByteArrayOutputStream out = new ByteArrayOutputStream();
			if (in != null) {
				try (InputStream i = in) {
					byte[] buf = new byte[4096];
					for (int n; (n = i.read(buf)) > 0;) {
						out.write(buf, 0, n);
					}
				}
			}
			if (code != 200) {
				throw new IOException("HTTP " + code);
			}
			JSONObject j = new JSONObject(out.toString("UTF-8"));
			JSONObject loc = j.getJSONObject("location");
			return String.format(Locale.ROOT, "location: %.5f,%.5f +-%.0f m (BeaconDB, %d access points)",
					loc.getDouble("lat"), loc.getDouble("lng"), j.getDouble("accuracy"), aps.length());
		} finally {
			c.disconnect();
		}
	}

	/** All open WLANs in range with their signal, for the log. */
	@SuppressWarnings("deprecation")
	static String openList(Wlan wlan) {
		StringBuilder b = new StringBuilder();
		List<android.net.wifi.ScanResult> l = wlan.openInRange();
		for (android.net.wifi.ScanResult r : l) {
			b.append(b.length() == 0 ? "" : ", ").append('"').append(r.SSID).append("\" ").append(r.level).append(" dBm");
		}
		return l.size() + " open WLANs in range" + (l.isEmpty() ? "" : ": " + b);
	}
}
