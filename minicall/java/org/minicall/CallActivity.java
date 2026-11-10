/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
package org.minicall;

import android.app.Activity;
import android.graphics.Color;
import android.graphics.SurfaceTexture;
import android.graphics.drawable.GradientDrawable;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.util.TypedValue;
import android.view.Gravity;
import android.view.SurfaceHolder;
import android.view.SurfaceView;
import android.view.TextureView;
import android.view.View;
import android.view.WindowManager;
import android.widget.Button;
import android.widget.FrameLayout;
import android.widget.TextView;

/** The call: the far end as large as fits and one button to hang up; the own picture is not shown. */
public final class CallActivity extends Activity implements CallService.Listener, VideoBridge.Display {
	private final Handler main = new Handler(Looper.getMainLooper());
	private FrameLayout root;
	private SurfaceView remote;
	private TextureView self;
	private TextView status;
	private int remoteW = 4, remoteH = 3;

	@Override
	protected void onCreate(Bundle state) {
		super.onCreate(state);
		showOverEverything();

		root = new FrameLayout(this);
		root.setBackgroundColor(Color.BLACK);

		remote = new SurfaceView(this);
		root.addView(remote, new FrameLayout.LayoutParams(1, 1, Gravity.CENTER));
		remote.getHolder().addCallback(new SurfaceHolder.Callback() {
			@Override
			public void surfaceCreated(SurfaceHolder h) {
				VideoBridge v = video();
				if (v != null) v.setRemoteSurface(h.getSurface(), CallActivity.this);
			}

			@Override
			public void surfaceChanged(SurfaceHolder h, int format, int w, int hgt) {
				// The decoder scales to whatever size the surface has.
			}

			@Override
			public void surfaceDestroyed(SurfaceHolder h) {
				VideoBridge v = video();
				if (v != null) v.setRemoteSurface(null, null);
			}
		});

		CallService svc = CallService.get();
		if (svc != null && svc.settings() != null && svc.settings().selfView) addSelfView();

		status = new TextView(this);
		status.setTextColor(Color.WHITE);
		status.setTextSize(TypedValue.COMPLEX_UNIT_SP, 28);
		status.setShadowLayer(4, 0, 0, Color.BLACK);
		FrameLayout.LayoutParams tp = new FrameLayout.LayoutParams(FrameLayout.LayoutParams.WRAP_CONTENT,
				FrameLayout.LayoutParams.WRAP_CONTENT, Gravity.TOP | Gravity.START);
		tp.setMargins(dp(20), dp(16), dp(20), dp(16));
		root.addView(status, tp);

		Button hangup = new Button(this);
		hangup.setText("Hang up");
		hangup.setAllCaps(false);
		hangup.setTextColor(Color.WHITE);
		hangup.setTextSize(TypedValue.COMPLEX_UNIT_SP, 26);
		GradientDrawable red = new GradientDrawable();
		red.setCornerRadius(dp(36));
		red.setColor(0xffd32f2f);
		hangup.setBackground(red);
		hangup.setOnClickListener(v -> {
			CallService s = CallService.get();
			if (s != null) s.hangup();
			finish();
		});
		FrameLayout.LayoutParams bp = new FrameLayout.LayoutParams(dp(220), dp(72),
				Gravity.BOTTOM | Gravity.CENTER_HORIZONTAL);
		bp.setMargins(0, 0, 0, dp(20));
		root.addView(hangup, bp);

		root.addOnLayoutChangeListener((v, l, t, r, b, ol, ot, or, ob) -> layoutRemote());
		setContentView(root);
	}

	@Override
	protected void onResume() {
		super.onResume();
		CallService s = CallService.get();
		if (s == null || s.state() == CallService.State.IDLE) {
			finish();
			return;
		}
		s.setListener(this);
		onStateChanged();
	}

	@Override
	protected void onPause() {
		CallService s = CallService.get();
		if (s != null) s.setListener(null);
		super.onPause();
	}

	// The back key callback that Android 7 has (OnBackInvokedCallback came with Android 13).
	@SuppressWarnings("deprecation")
	@Override
	public void onBackPressed() {
		// Back must not leave a call running unseen: it hangs up like the button.
		CallService s = CallService.get();
		if (s != null) s.hangup();
		super.onBackPressed();
	}

	@Override
	public void onStateChanged() {
		CallService s = CallService.get();
		if (s == null || s.state() == CallService.State.IDLE) {
			finish();
			return;
		}
		String st = s.status();
		status.setText(st.isEmpty() ? s.peer() : s.peer() + " - " + st);
	}

	/** The own picture, small in a corner; the camera's frame is drawn into it by the GPU, mirrored. */
	private void addSelfView() {
		self = new TextureView(this);
		FrameLayout.LayoutParams sp = new FrameLayout.LayoutParams(dp(176), dp(99), Gravity.TOP | Gravity.END);
		sp.setMargins(dp(12), dp(12), dp(12), dp(12));
		root.addView(self, sp);
		self.setSurfaceTextureListener(new TextureView.SurfaceTextureListener() {
			@Override
			public void onSurfaceTextureAvailable(SurfaceTexture t, int w, int h) {
				VideoBridge v = video();
				if (v == null) return;
				v.setSelfTexture(t, w, h);
				int[] size = v.selfSize();
				if (size != null) onSelfSize(size[0], size[1]);
			}

			@Override
			public void onSurfaceTextureSizeChanged(SurfaceTexture t, int w, int h) {
				VideoBridge v = video();
				if (v != null) v.setSelfTexture(t, w, h);
			}

			@Override
			public boolean onSurfaceTextureDestroyed(SurfaceTexture t) {
				VideoBridge v = video();
				if (v != null) v.setSelfTexture(null, 0, 0);
				return true;
			}

			@Override
			public void onSurfaceTextureUpdated(SurfaceTexture t) {
				// Nothing to do per frame.
			}
		});
	}

	@Override
	public void onSelfSize(int width, int height) {
		main.post(() -> {
			if (self == null || width <= 0 || height <= 0) return;
			FrameLayout.LayoutParams lp = (FrameLayout.LayoutParams) self.getLayoutParams();
			lp.width = dp(176);
			lp.height = dp(176) * height / width;
			self.setLayoutParams(lp);
		});
	}

	@Override
	public void onRemoteSize(int width, int height) {
		main.post(() -> {
			remoteW = width;
			remoteH = height;
			layoutRemote();
		});
	}

	/**
	 * The far end's picture fills the screen, keeping its shape: what sticks out beyond the screen's edges is cut
	 * off (the middle stays), instead of black bars beside or above it.
	 */
	private void layoutRemote() {
		int w = root.getWidth(), h = root.getHeight();
		if (w == 0 || h == 0) return;
		int vw = w, vh = w * remoteH / remoteW;
		if (vh < h) {
			vh = h;
			vw = h * remoteW / remoteH;
		}
		FrameLayout.LayoutParams lp = (FrameLayout.LayoutParams) remote.getLayoutParams();
		if (lp.width != vw || lp.height != vh) {
			lp.width = vw;
			lp.height = vh;
			remote.setLayoutParams(lp);
		}
	}

	// These flags are what Android 7 has; the replacements came with Android 8.1 and 11.
	@SuppressWarnings("deprecation")
	private void showOverEverything() {
		// A call has to be seen even when the screen was off or showed the clock.
		getWindow().addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON
				| WindowManager.LayoutParams.FLAG_SHOW_WHEN_LOCKED
				| WindowManager.LayoutParams.FLAG_TURN_SCREEN_ON
				| WindowManager.LayoutParams.FLAG_DISMISS_KEYGUARD);
		getWindow().getDecorView().setSystemUiVisibility(View.SYSTEM_UI_FLAG_FULLSCREEN
				| View.SYSTEM_UI_FLAG_HIDE_NAVIGATION | View.SYSTEM_UI_FLAG_IMMERSIVE_STICKY
				| View.SYSTEM_UI_FLAG_LAYOUT_FULLSCREEN | View.SYSTEM_UI_FLAG_LAYOUT_HIDE_NAVIGATION
				| View.SYSTEM_UI_FLAG_LAYOUT_STABLE);
		if (Build.VERSION.SDK_INT >= 28) {
			// The picture also under a phone's camera hole: otherwise that edge stays a black strip.
			WindowManager.LayoutParams lp = getWindow().getAttributes();
			lp.layoutInDisplayCutoutMode = WindowManager.LayoutParams.LAYOUT_IN_DISPLAY_CUTOUT_MODE_SHORT_EDGES;
			getWindow().setAttributes(lp);
		}
	}

	private static VideoBridge video() {
		CallService s = CallService.get();
		return s != null ? s.video() : null;
	}

	private int dp(int v) {
		return Math.round(v * getResources().getDisplayMetrics().density);
	}
}
