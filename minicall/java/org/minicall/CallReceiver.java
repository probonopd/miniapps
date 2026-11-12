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
 * Lets the TV app (minitv, over MQTT) start and end calls: org.minicall.action.CALL with the extra "to" (a contact's
 * name or address) and org.minicall.action.HANGUP. Only apps signed with the same key hold the permission the
 * manifest demands of senders, and even they can only call contacts.
 */
public final class CallReceiver extends BroadcastReceiver {
	@Override
	public void onReceive(Context ctx, Intent intent) {
		String action = intent.getAction();
		Intent svc = new Intent(ctx, CallService.class);
		if (CallService.ACTION_CALL.equals(action)) {
			svc.setAction(action).putExtra(CallService.EXTRA_TO, intent.getStringExtra(CallService.EXTRA_TO));
		} else if (CallService.ACTION_HANGUP.equals(action)) {
			svc.setAction(action);
		} else {
			return;
		}
		CallService.start(ctx, svc);
	}
}
