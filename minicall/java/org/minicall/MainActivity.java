/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
package org.minicall;

import android.app.Activity;
import android.content.Intent;
import android.graphics.Color;
import android.graphics.drawable.GradientDrawable;
import android.os.Bundle;
import android.util.TypedValue;
import android.view.Gravity;
import android.widget.Button;
import android.widget.GridLayout;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;

/** One big button per contact; a touch calls. */
public final class MainActivity extends Activity implements CallService.Listener {
	private TextView status;
	private GridLayout grid;

	@Override
	protected void onCreate(Bundle state) {
		super.onCreate(state);
		CallService.start(this, new Intent(this, CallService.class));

		LinearLayout col = new LinearLayout(this);
		col.setOrientation(LinearLayout.VERTICAL);
		col.setBackgroundColor(0xff101418);
		col.setPadding(dp(24), dp(16), dp(24), dp(16));

		status = new TextView(this);
		status.setTextColor(0xffb0bec5);
		status.setTextSize(TypedValue.COMPLEX_UNIT_SP, 18);
		col.addView(status);

		grid = new GridLayout(this);
		grid.setColumnCount(3);
		ScrollView scroll = new ScrollView(this);
		scroll.addView(grid);
		LinearLayout.LayoutParams gp = new LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, 0, 1);
		gp.topMargin = dp(12);
		col.addView(scroll, gp);
		setContentView(col);
	}

	@Override
	protected void onResume() {
		super.onResume();
		CallService s = CallService.get();
		if (s != null) s.setListener(this);
		rebuild();
	}

	@Override
	protected void onPause() {
		CallService s = CallService.get();
		if (s != null) s.setListener(null);
		super.onPause();
	}

	@Override
	public void onStateChanged() {
		rebuild();
	}

	private void rebuild() {
		CallService s = CallService.get();
		if (s == null) {
			status.setText("starting");
			status.postDelayed(this::onResume, 500);
			return;
		}
		Settings st = s.settings();
		if (st == null) {
			status.setText("Not set up: " + s.error());
			grid.removeAllViews();
			return;
		}
		String reach = st.hasAccount() ? (s.registered() ? "reachable as " + st.sipUser + "@" + st.sipDomain
				: "connecting to " + st.sipDomain) : "reachable in the local network";
		status.setText(s.error().isEmpty() ? reach : reach + " - " + s.error());
		int devices = 0;
		for (Settings.Contact c : st.contacts) {
			if (c.uri.startsWith("mqtt:")) devices++;
		}
		if (grid.getChildCount() == st.contacts.size() + devices) return;
		grid.removeAllViews();
		int w = (getResources().getDisplayMetrics().widthPixels - dp(48)) / 3 - dp(16);
		for (Settings.Contact c : st.contacts) {
			Button b = new Button(this);
			b.setText(c.name);
			b.setAllCaps(false);
			b.setTextColor(Color.WHITE);
			b.setTextSize(TypedValue.COMPLEX_UNIT_SP, 30);
			b.setGravity(Gravity.CENTER);
			GradientDrawable bg = new GradientDrawable();
			bg.setCornerRadius(dp(16));
			bg.setColor(0xff2e7d32);
			b.setBackground(bg);
			b.setOnClickListener(v -> {
				CallService svc = CallService.get();
				if (svc != null) svc.call(c.name);
			});
			GridLayout.LayoutParams lp = new GridLayout.LayoutParams();
			lp.width = w;
			lp.height = dp(140);
			lp.setMargins(dp(8), dp(8), dp(8), dp(8));
			grid.addView(b, lp);
		}
		// Babyphone: a device's camera and microphone, one way, nothing changes on its screen. The device decides
		// who may (babyphone= in its minicall.conf).
		for (Settings.Contact c : st.contacts) {
			if (!c.uri.startsWith("mqtt:")) continue;
			Button b = new Button(this);
			b.setText("Babyphone\n" + c.name);
			b.setAllCaps(false);
			b.setTextColor(Color.WHITE);
			b.setTextSize(TypedValue.COMPLEX_UNIT_SP, 20);
			b.setGravity(Gravity.CENTER);
			GradientDrawable bg = new GradientDrawable();
			bg.setCornerRadius(dp(16));
			bg.setColor(0xff1565c0);
			b.setBackground(bg);
			b.setOnClickListener(v -> {
				CallService svc = CallService.get();
				if (svc != null) svc.babyphone(c.name);
			});
			GridLayout.LayoutParams lp = new GridLayout.LayoutParams();
			lp.width = w;
			lp.height = dp(140);
			lp.setMargins(dp(8), dp(8), dp(8), dp(8));
			grid.addView(b, lp);
		}
	}

	private int dp(int v) {
		return Math.round(v * getResources().getDisplayMetrics().density);
	}
}
