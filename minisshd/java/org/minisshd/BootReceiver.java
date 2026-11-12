/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */
package org.minisshd;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;

/** Starts the SSH service at boot, so the device is reachable without anyone opening an app - there is none to open. */
public final class BootReceiver extends BroadcastReceiver {
	@Override
	public void onReceive(Context ctx, Intent intent) {
		if (Intent.ACTION_BOOT_COMPLETED.equals(intent.getAction())) {
			MinisshdService.start(ctx);
		}
	}
}
