/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */
package org.minicall;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;

/**
 * Starts the call service at boot and after an update, so that the device is reachable for calls without anyone
 * opening the app, and an in-call flag left by the replaced version is cleared (it would keep the wake word off).
 */
public final class BootReceiver extends BroadcastReceiver {
	@Override
	public void onReceive(Context ctx, Intent intent) {
		if (Intent.ACTION_BOOT_COMPLETED.equals(intent.getAction())
				|| Intent.ACTION_MY_PACKAGE_REPLACED.equals(intent.getAction())) {
			CallService.start(ctx, new Intent(ctx, CallService.class));
		}
	}
}
