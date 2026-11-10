/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
package org.minicaptive;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;

/** Keeps the WLAN online after a reboot or an update of the app without anyone opening it. */
public final class BootReceiver extends BroadcastReceiver {
	@Override
	public void onReceive(Context ctx, Intent intent) {
		if (Intent.ACTION_BOOT_COMPLETED.equals(intent.getAction())
				|| Intent.ACTION_MY_PACKAGE_REPLACED.equals(intent.getAction())) {
			CaptiveService.start(ctx);
		}
	}
}
