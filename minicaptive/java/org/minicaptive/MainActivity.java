/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
package org.minicaptive;

import android.Manifest;
import android.app.Activity;
import android.app.AlertDialog;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.graphics.Color;
import android.graphics.Typeface;
import android.graphics.drawable.GradientDrawable;
import android.os.Build;
import android.location.LocationManager;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.provider.Settings;
import android.text.InputType;
import android.view.Gravity;
import android.view.View;
import android.widget.AdapterView;
import android.widget.ArrayAdapter;
import android.widget.Button;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.Spinner;
import android.widget.Switch;
import android.widget.TextView;
import java.io.IOException;
import java.util.ArrayList;
import java.util.List;

/**
 * Switch, WLAN choice, status and settings, for setting up and testing; the service needs nobody here. Shows
 * whether the WLAN is online, how the last portal was passed, and what the service did.
 */
public final class MainActivity extends Activity {
	private static final String ANY = "any open WLAN";

	private TextView state;
	private TextView result;
	private TextView log;
	private EditText settings;
	private Switch onOff;
	private Spinner wlan;
	private boolean filling;
	private boolean picked;
	/** The WLAN name behind each dropdown entry ("" for any open WLAN); the entries show more than the name. */
	private final List<String> names = new ArrayList<>();
	private Switch logSwitch;
	private Switch geoSwitch;
	private TextView wlanInfo;
	private LinearLayout box;
	private GradientDrawable boxBg;
	private final Handler ticker = new Handler(Looper.getMainLooper());
	/** The WLAN can change without the service writing anything (Android joins, signal moves). */
	private final Runnable tick = new Runnable() {
		@Override
		public void run() {
			show();
			ticker.postDelayed(this, 2000);
		}
	};
	private TextView perms;
	private Button grant;

	@Override
	protected void onCreate(Bundle b) {
		super.onCreate(b);
		Journal.init(this);
		int pad = (int) (12 * getResources().getDisplayMetrics().density);
		LinearLayout col = new LinearLayout(this);
		col.setOrientation(LinearLayout.VERTICAL);
		col.setPadding(pad, pad, pad, pad);
		onOff = new Switch(this);
		onOff.setText("Keep the WLAN online");
		onOff.setTextSize(18);
		onOff.setOnCheckedChangeListener((v, on) -> {
			// Showing the stored state is not a switch by the user.
			if (filling) {
				return;
			}
			if (on) {
				askPermissions();
			}
			try {
				CaptiveService.setEnabled(this, on);
			} catch (IOException e) {
				result.setText(e.getMessage());
			}
		});
		perms = new TextView(this);
		perms.setTextColor(Color.rgb(200, 0, 0));
		grant = new Button(this);
		grant.setText("Grant");
		grant.setOnClickListener(v -> askPermissions());
		state = new TextView(this);
		state.setTextSize(28);
		state.setTypeface(Typeface.DEFAULT_BOLD);
		state.setGravity(Gravity.CENTER);
		state.setTextColor(Color.WHITE);
		wlanInfo = new TextView(this);
		wlanInfo.setTextColor(Color.WHITE);
		wlanInfo.setTextSize(15);
		box = new LinearLayout(this);
		box.setOrientation(LinearLayout.VERTICAL);
		box.setPadding(pad, pad, pad, pad);
		box.addView(state);
		box.addView(wlanInfo);
		boxBg = new GradientDrawable();
		boxBg.setCornerRadius(pad);
		box.setBackground(boxBg);
		result = new TextView(this);
		result.setGravity(Gravity.CENTER);
		TextView wlanLabel = new TextView(this);
		wlanLabel.setText("WLAN to stay on (\"any\": open WLANs, strongest first):");
		wlan = new Spinner(this);
		wlan.setOnItemSelectedListener(new AdapterView.OnItemSelectedListener() {
			@Override
			public void onItemSelected(AdapterView<?> parent, View v, int pos, long id) {
				// Spinners also report selections made by code (adapter set, layout): only a pick by hand counts.
				if (picked) {
					picked = false;
					String old = settings.getText().toString();
					String now = Config.withSetting(old, "ssid", names.get(pos));
					if (!now.equals(old)) {
						settings.setText(now);
						save();
					}
				}
			}

			@Override
			public void onNothingSelected(AdapterView<?> parent) {
			}
		});
		wlan.setOnTouchListener((v, e) -> {
			picked = true;
			return false;
		});
		Button check = new Button(this);
		check.setText("Check now");
		check.setOnClickListener(v -> CaptiveService.check(this));
		settings = new EditText(this);
		settings.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_FLAG_MULTI_LINE
				| InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS);
		settings.setTypeface(Typeface.MONOSPACE);
		settings.setTextSize(12);
		Button save = new Button(this);
		save.setText("Save settings");
		save.setOnClickListener(v -> save());
		logSwitch = new Switch(this);
		logSwitch.setText("Log to file " + Journal.FILE + " (to read with adb later), with the open WLANs in range");
		geoSwitch = new Switch(this);
		geoSwitch.setText("Log the location (Android's; else BeaconDB from the access points in range, no account)");
		geoSwitch.setOnCheckedChangeListener((v, on) -> {
			if (filling) {
				return;
			}
			try {
				Geo.setOn(this, on);
			} catch (IOException e) {
				result.setText(e.getMessage());
			}
		});
		logSwitch.setOnCheckedChangeListener((v, on) -> {
			if (filling) {
				return;
			}
			try {
				Journal.setFileOn(on);
			} catch (IOException e) {
				result.setText(e.getMessage());
				return;
			}
			if (!on && Journal.fileExists()) {
				new AlertDialog.Builder(this).setMessage("Delete the existing log file?")
						.setPositiveButton("Delete", (d, w) -> {
							try {
								Journal.deleteFile();
								result.setText("log file deleted");
							} catch (IOException e) {
								result.setText(e.getMessage());
							}
						}).setNegativeButton("Keep", null).show();
			}
		});
		log = new TextView(this);
		log.setTypeface(Typeface.MONOSPACE);
		log.setTextSize(11);
		log.setTextIsSelectable(true);
		col.addView(onOff);
		col.addView(perms);
		col.addView(grant);
		col.addView(box);
		col.addView(result);
		col.addView(wlanLabel);
		col.addView(wlan);
		col.addView(check);
		col.addView(settings);
		col.addView(save);
		col.addView(logSwitch);
		col.addView(geoSwitch);
		col.addView(log);
		ScrollView scroll = new ScrollView(this);
		scroll.addView(col);
		setContentView(scroll);
		askPermissions();
	}

	/**
	 * What Android must allow: WLAN names and scans are location data (8.1+), also in the background (10+);
	 * the card holds the log and dropped-in settings.
	 */
	private List<String> missingPermissions() {
		List<String> out = new ArrayList<>();
		if (Build.VERSION.SDK_INT < 23) {
			return out;
		}
		List<String> need = new ArrayList<>();
		need.add(Manifest.permission.ACCESS_FINE_LOCATION);
		if (Build.VERSION.SDK_INT >= 29) {
			need.add(Manifest.permission.ACCESS_BACKGROUND_LOCATION);
		}
		need.add(Manifest.permission.WRITE_EXTERNAL_STORAGE);
		for (String p : need) {
			if (checkSelfPermission(p) != PackageManager.PERMISSION_GRANTED) {
				out.add(p);
			}
		}
		return out;
	}

	private void askPermissions() {
		List<String> m = missingPermissions();
		if (!m.isEmpty()) {
			requestPermissions(m.toArray(new String[0]), 1);
		} else if (!locationOn()) {
			startActivity(new Intent(Settings.ACTION_LOCATION_SOURCE_SETTINGS));
		} else if (!batteryFree()) {
			startActivity(new Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
					android.net.Uri.parse("package:" + getPackageName())));
		}
	}

	/** Doze cuts apps off the network while the phone lies still; an exempt app keeps checking. */
	private boolean batteryFree() {
		return Build.VERSION.SDK_INT < 23
				|| getSystemService(android.os.PowerManager.class).isIgnoringBatteryOptimizations(getPackageName());
	}

	private boolean locationOn() {
		return Build.VERSION.SDK_INT < 28 || getSystemService(LocationManager.class).isLocationEnabled();
	}

	@Override
	public void onRequestPermissionsResult(int code, String[] perms, int[] results) {
		showPermissions();
	}

	private void showPermissions() {
		List<String> m = missingPermissions();
		StringBuilder b = new StringBuilder();
		for (String p : m) {
			b.append(b.length() == 0 ? "Not allowed yet: " : ", ").append(p.replace("android.permission.", ""));
		}
		if (!locationOn()) {
			b.append(b.length() == 0 ? "" : ". ").append("Location is off: WLAN names and scans need it");
		}
		if (!batteryFree()) {
			b.append(b.length() == 0 ? "" : ". ").append("Battery optimization is on: checks pause while the phone sleeps");
		}
		perms.setText(b);
		perms.setVisibility(b.length() == 0 ? View.GONE : View.VISIBLE);
		grant.setVisibility(b.length() == 0 ? View.GONE : View.VISIBLE);
	}

	private void save() {
		try {
			Config.writeText(this, settings.getText().toString());
			result.setText("saved");
			CaptiveService.check(this);
		} catch (IOException | RuntimeException e) {
			result.setText("not saved: " + e.getMessage());
		}
	}

	@Override
	protected void onResume() {
		super.onResume();
		filling = true;
		onOff.setChecked(CaptiveService.enabled(this));
		logSwitch.setChecked(Journal.fileOn());
		geoSwitch.setChecked(Geo.on(this));
		// The screen shows what the service will use: a file dropped on the card replaces the settings first.
		String text;
		try {
			Config.importCard(this);
			text = Config.readText(this);
		} catch (IOException | RuntimeException e) {
			text = "";
			result.setText("settings: " + e.getMessage());
		}
		settings.setText(text);
		names.clear();
		List<String> labels = new ArrayList<>();
		names.add("");
		labels.add(ANY);
		for (String[] c : new Wlan(this).choices()) {
			names.add(c[0]);
			labels.add(c[1]);
		}
		String current;
		try {
			current = Config.ssidOf(text);
		} catch (RuntimeException e) {
			current = "";
		}
		if (!current.isEmpty() && !names.contains(current)) {
			names.add(current);
			labels.add(current + "   (not saved, not in range)");
		}
		ArrayAdapter<String> a = new ArrayAdapter<>(this, android.R.layout.simple_spinner_item, labels);
		a.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item);
		wlan.setAdapter(a);
		wlan.setSelection(names.indexOf(current), false);
		// Selection events of the adapter change arrive later on the UI thread; only real picks count.
		wlan.post(() -> filling = false);
		// Kept portal pages next to the log, where adb reads them (they were private while not logging to file).
		if (Journal.fileOn()) {
			try {
				CaptiveService.exportPages(this);
			} catch (IOException e) {
				result.setText(e.getMessage());
			}
		}
		Journal.setListener(() -> runOnUiThread(this::show));
		showPermissions();
		ticker.post(tick);
	}

	@Override
	protected void onPause() {
		Journal.setListener(null);
		ticker.removeCallbacks(tick);
		super.onPause();
	}

	private void show() {
		// The service may have switched itself off (password WLAN): the switch shows it.
		if (onOff.isChecked() != CaptiveService.enabled(this)) {
			filling = true;
			onOff.setChecked(CaptiveService.enabled(this));
			filling = false;
		}
		String s = CaptiveService.enabled(this) ? Journal.state() : "off";
		state.setText(s.equals("online") ? "online - Internet works" : s);
		// Green: Internet. Yellow: on the way (portal, joining, trying a WLAN). Red: stuck. Gray: switched off.
		boxBg.setColor(s.equals("online") ? Color.rgb(0, 140, 60)
				: s.equals("portal") || s.startsWith("joining") || s.startsWith("trying") || s.equals("starting")
						? Color.rgb(215, 150, 0)
						: s.equals("off") ? Color.GRAY : Color.rgb(200, 30, 30));
		wlanInfo.setText(new Wlan(this).panel());
		if (!result.getText().toString().startsWith("not saved")) {
			result.setText(Journal.lastResult());
		}
		log.setText(Journal.text());
	}
}
