/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */
package org.minisshd;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.net.nsd.NsdManager;
import android.net.nsd.NsdServiceInfo;
import android.net.wifi.WifiManager;
import android.os.Build;
import android.os.IBinder;
import android.util.Log;
import java.io.File;
import java.io.InputStream;
import java.util.Collections;
import java.util.Map;

/**
 * Runs a patched, statically linked dropbear (see ../README.md) as a plain background process and relaunches it
 * if it ever exits. dropbear/dropbearkey travel as lib*.so files, so Android already extracted them, executable,
 * into this app's own nativeLibraryDir at install time - there is nothing to unpack here.
 */
public final class MinisshdService extends Service {
	private static final String TAG = "minisshd";
	private static final int PORT = 2222;

	// Both service types point at the same port: SFTP is a subsystem of SSH, not a separate
	// listener, but Bonjour/Avahi-style discovery conventionally advertises it as its own type.
	private static final String[] SERVICE_TYPES = {"_ssh._tcp.", "_sftp-ssh._tcp."};

	private volatile boolean stopping;
	private NsdManager nsdManager;
	private WifiManager.MulticastLock multicastLock;
	private final NsdManager.RegistrationListener[] nsdListeners = new NsdManager.RegistrationListener[SERVICE_TYPES.length];

	static void start(Context ctx) {
		Intent intent = new Intent(ctx, MinisshdService.class);
		if (Build.VERSION.SDK_INT >= 26) {
			ctx.startForegroundService(intent);
		} else {
			ctx.startService(intent);
		}
	}

	@Override
	public void onCreate() {
		super.onCreate();
		startForeground(1, notification());
		new Thread(this::runLoop, "minisshd-runner").start();
		announceZeroconf();
	}

	@Override
	public int onStartCommand(Intent intent, int flags, int startId) {
		return START_STICKY;
	}

	@Override
	public void onDestroy() {
		stopping = true;
		withdrawZeroconf();
		super.onDestroy();
	}

	@Override
	public IBinder onBind(Intent intent) {
		return null;
	}

	// Android 8 and newer show no notification without a channel, and then refuse the foreground service.
	@SuppressWarnings("deprecation")
	private Notification notification() {
		Notification.Builder b;
		if (Build.VERSION.SDK_INT >= 26) {
			NotificationManager nm = (NotificationManager) getSystemService(NOTIFICATION_SERVICE);
			nm.createNotificationChannel(new NotificationChannel("ssh", "SSH", NotificationManager.IMPORTANCE_LOW));
			b = new Notification.Builder(this, "ssh");
		} else {
			b = new Notification.Builder(this);
		}
		return b.setContentTitle("minisshd").setSmallIcon(android.R.drawable.stat_sys_download_done)
				.setOngoing(true).build();
	}

	private void runLoop() {
		String libDir = getApplicationInfo().nativeLibraryDir;
		File dropbear = new File(libDir, "libdropbear.so");
		File dropbearkey = new File(libDir, "libdropbearkey.so");
		// dropbear's own SFTPSERVER_PATH is a compile-time constant pointing at a path that
		// doesn't exist on Android; our patched dropbear takes this env var as an override,
		// since the real path (this app's own nativeLibraryDir) is only known at install time.
		File sftpServer = new File(libDir, "libsftpserver.so");
		File hostkey = new File(getFilesDir(), "dropbear_ed25519_host_key");

		try {
			// Kept in app-private storage so the fingerprint stays the same across restarts and reboots.
			if (!hostkey.exists()) {
				exec(null, dropbearkey.getAbsolutePath(), "-t", "ed25519", "-f", hostkey.getAbsolutePath());
			}

			Map<String, String> env = Collections.singletonMap(
					"DROPBEAR_SFTPSERVER_PATH", sftpServer.getAbsolutePath());
			int backoffMs = 1000;
			while (!stopping) {
				long start = System.currentTimeMillis();
				int code = exec(env, dropbear.getAbsolutePath(), "-F", "-E", "-p", String.valueOf(PORT),
						"-r", hostkey.getAbsolutePath());
				Log.w(TAG, "dropbear exited with " + code);
				if (stopping) {
					break;
				}
				long ranMs = System.currentTimeMillis() - start;
				// Only back off on a crash loop; a long run before exit means something else killed it.
				backoffMs = ranMs > 10000 ? 1000 : Math.min(backoffMs * 2, 60000);
				Thread.sleep(backoffMs);
			}
		} catch (Exception e) {
			Log.e(TAG, "runLoop failed", e);
		}
	}

	// mDNS multicast is commonly suppressed on WiFi to save power; NsdManager advertises
	// nothing without a multicast lock held while the service wants to be discoverable.
	private void announceZeroconf() {
		WifiManager wifi = (WifiManager) getApplicationContext().getSystemService(Context.WIFI_SERVICE);
		multicastLock = wifi.createMulticastLock(TAG);
		multicastLock.setReferenceCounted(false);
		multicastLock.acquire();

		nsdManager = (NsdManager) getSystemService(Context.NSD_SERVICE);
		for (int i = 0; i < SERVICE_TYPES.length; i++) {
			NsdServiceInfo info = new NsdServiceInfo();
			info.setServiceName(Build.MODEL);
			info.setServiceType(SERVICE_TYPES[i]);
			info.setPort(PORT);
			NsdManager.RegistrationListener listener = new NsdManager.RegistrationListener() {
				@Override
				public void onServiceRegistered(NsdServiceInfo i) {
					Log.i(TAG, "zeroconf registered: " + i.getServiceName() + " " + i.getServiceType());
				}
				@Override
				public void onRegistrationFailed(NsdServiceInfo i, int code) {
					Log.w(TAG, "zeroconf registration failed for " + i.getServiceType() + ": " + code);
				}
				@Override
				public void onServiceUnregistered(NsdServiceInfo i) {}
				@Override
				public void onUnregistrationFailed(NsdServiceInfo i, int code) {}
			};
			nsdListeners[i] = listener;
			nsdManager.registerService(info, NsdManager.PROTOCOL_DNS_SD, listener);
		}
	}

	private void withdrawZeroconf() {
		if (nsdManager != null) {
			for (NsdManager.RegistrationListener listener : nsdListeners) {
				if (listener != null) {
					try {
						nsdManager.unregisterService(listener);
					} catch (IllegalArgumentException alreadyGone) {
						// registration never completed or was already torn down
					}
				}
			}
		}
		if (multicastLock != null && multicastLock.isHeld()) {
			multicastLock.release();
		}
	}

	private int exec(Map<String, String> env, String... argv) throws Exception {
		ProcessBuilder pb = new ProcessBuilder(argv);
		if (env != null) {
			pb.environment().putAll(env);
		}
		pb.redirectErrorStream(true);
		Process p = pb.start();
		try (InputStream in = p.getInputStream()) {
			byte[] buf = new byte[4096];
			while (in.read(buf) >= 0) {
				// dropbear logs connections here via -E; just drain it so the pipe never fills and blocks it.
			}
		}
		return p.waitFor();
	}
}
