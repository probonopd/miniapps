package org.minitv;

import android.Manifest;
import android.app.Activity;
import android.content.BroadcastReceiver;
import android.content.ComponentName;
import android.content.Context;
import android.content.Intent;
import android.content.IntentFilter;
import android.content.SharedPreferences;
import android.content.pm.ActivityInfo;
import android.content.pm.PackageManager;
import android.content.pm.ResolveInfo;
import android.media.AudioDeviceCallback;
import android.media.AudioDeviceInfo;
import android.media.AudioManager;
import android.media.audiofx.LoudnessEnhancer;
import android.media.tv.TvContract;
import android.media.tv.TvInputInfo;
import android.media.tv.TvInputManager;
import android.media.tv.TvView;
import android.net.ConnectivityManager;
import android.net.Network;
import android.net.NetworkCapabilities;
import android.net.NetworkRequest;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.os.PowerManager;
import android.os.SystemClock;
import android.text.Layout;
import android.text.StaticLayout;
import android.text.TextPaint;
import android.util.DisplayMetrics;
import android.animation.ValueAnimator;
import android.util.Log;
import android.util.TypedValue;
import android.view.Gravity;
import android.view.KeyEvent;
import android.view.MotionEvent;
import android.view.SurfaceView;
import android.view.View;
import android.view.animation.AccelerateInterpolator;
import android.view.animation.DecelerateInterpolator;
import android.view.WindowManager;
import android.widget.FrameLayout;
import android.widget.TextView;
import androidx.media3.common.C;
import androidx.media3.common.MediaItem;
import androidx.media3.common.MimeTypes;
import androidx.media3.common.PlaybackException;
import androidx.media3.common.Player;
import androidx.media3.common.VideoSize;
import androidx.media3.datasource.DefaultDataSource;
import androidx.media3.datasource.DefaultHttpDataSource;
import androidx.media3.exoplayer.DefaultLoadControl;
import androidx.media3.exoplayer.ExoPlayer;
import androidx.media3.exoplayer.source.DefaultMediaSourceFactory;
import java.util.ArrayList;
import java.util.List;

/** A TV set and home screen: full-screen picture, channel up/down, volume up/down, nothing else. */
public class MainActivity extends Activity implements Player.Listener, VideoCall.Listener {
	// After a failed attempt the stream is tried again after these delays, then every RETRY_MS, as long as it is selected.
	private static final int[] BACKOFF_MS = { 500, 1500, 3000 };
	private static final int RETRY_MS = 5000;
	// An attempt without a first frame after this long has failed.
	private static final int ATTEMPT_MS = 7000;
	private static final int BOOST_MB = 1000;
	private static final int HOLD_MS = 2500;
	private static final int HOME_PRESSES = 3;
	private static final int HOME_WINDOW_MS = 3000;
	private static final int LIST_MS = 15000;
	private static final int OUT_MS = 400;
	// 70% black behind the texts at the top and the bottom.
	private static final int BAR_COLOR = 0xB3000000;
	// A stream that buffers this long without a picture is tuned again (a dropped WLAN connection leaves it so).
	private static final int STALL_MS = 15000;
	// An answer is kept until its programme ends, but never longer than this (schedules change).
	private static final int GUIDE_MAX_AGE_MS = 15 * 60000;
	// After a failed request the guide stays quiet for this long instead of asking on every channel change.
	private static final int GUIDE_BACKOFF_MS = 30000;
	private static final int FETCH_DELAY_MS = 300;

	private final Handler handler = new Handler(Looper.getMainLooper());
	// Own handler so that tuning, which clears the retry messages, never cancels a key being held.
	private final Handler holds = new Handler(Looper.getMainLooper());
	private Playlist playlist = new Playlist();
	private FrameLayout root;
	private float touchX;
	private float touchY;
	private boolean touchEndedMessage;
	private SurfaceView video;
	private View shutter;
	// The optional programme guide: time, title and subtitle after a channel change.
	private TextView guide;
	// Date and time at the bottom right: with the banner after a channel change, and in the channel list.
	private TextView clock;
	// The time at the top right, permanently, when the setting clock is on.
	private TextView timeLabel;
	// Holds the programme text and the clock on one bar at the bottom.
	private FrameLayout footer;
	// The number and name of the channel, shown at once when it is switched to (before anything is loaded).
	private TextView channelLabel;
	// The screen part of a message (pushed or scheduled): the picture dimmed, the text centered.
	private TextView message;
	// The key that ended the message: its repeats and its release do nothing either.
	private int swallowedKey = KeyEvent.KEYCODE_UNKNOWN;
	// The active message: its spec, a token that every delayed action of it checks (a newer message or an ending
	// raises it, so nothing of an old message fires later), and the generation of its voice series (0: none).
	private MessageSpec active;
	private int messageGeneration, messageSpeech;
	// Whether the screen part is shown: the state, not the view, which is still visible while it fades out.
	private boolean screenShown;
	// Raised by every fade, so that the end of an old fade out never hides a text that came back meanwhile.
	private int screenFade;
	private static final int MESSAGE_FADE_IN_MS = 300, MESSAGE_FADE_OUT_MS = 400;
	private boolean holdFired;
	// An address played on request over MQTT, instead of the channel of the playlist; null otherwise.
	private String transientUrl;
	// A video call over everything but a message; the TV is stopped while it is open.
	private VideoCall call;
	// The HDMI input is shown by the system's TV input framework in a TvView, inside this app.
	private TvView tvView;
	// A plain screen, no text, when there is no picture: red if the channel does not come up (10 s after switching),
	// yellow if there is no internet (20 s); nothing before that.
	private View notice;
	private final Handler notices = new Handler(Looper.getMainLooper());
	private boolean pictureUp;
	private boolean buffering;
	// The channel list opened with OK, and what the guide said recently: zapp-id -> show and when it was fetched.
	private ChannelList list;
	// Set while the list shows the other home apps instead of the channels.
	private List<ActivityInfo> launchers;
	private final java.util.Map<String, Guide.Show> shows = new java.util.HashMap<>();
	private final java.util.Map<String, Long> fetched = new java.util.HashMap<>();
	private long guideQuietUntil;
	// Set by a channel change; the banner is requested when the first picture is up, so that the request never
	// competes with the stream for the start.
	private boolean bannerPending;
	private ExoPlayer player;
	private AudioManager audio;
	private SharedPreferences prefs;
	private int channel;
	private boolean asHls;
	private boolean noAudio;
	private VideoSize size = VideoSize.UNKNOWN;
	private boolean volumeWatched;
	private boolean networkWatched;
	private boolean voicePreloaded;
	private boolean retryPending;
	// Failed attempts in a row for the selected channel, and the error code of the last one (for the state topic).
	private int attempts;
	private String streamError = "";
	private LoudnessEnhancer boost;
	private int homePresses;
	private long lastHomePress;

	private final BroadcastReceiver volumeChanged = new BroadcastReceiver() {
		@Override
		public void onReceive(Context context, Intent intent) {
			applyVolume();
		}
	};

	private final AudioDeviceCallback devicesChanged = new AudioDeviceCallback() {
		@Override
		public void onAudioDevicesAdded(AudioDeviceInfo[] devices) {
			applyVolume();
		}

		@Override
		public void onAudioDevicesRemoved(AudioDeviceInfo[] devices) {
			applyVolume();
		}
	};

	// Tune again the moment a network comes up, instead of waiting for the retry timer: at boot the first
	// attempts fail until WLAN has connected.
	private final ConnectivityManager.NetworkCallback networkUp = new ConnectivityManager.NetworkCallback() {
		@Override
		public void onAvailable(Network network) {
			handler.post(() -> {
				if ((retryPending || buffering) && player != null) tune();
			});
		}
	};

	@Override
	protected void onCreate(Bundle state) {
		super.onCreate(state);
		MqttService.installCrashHandler(this);
		Settings.init(this);
		Playlist.init(this);
		Settings.onChange = () -> runOnUiThread(this::applySettings);
		Speech.init(this);
		Speech.onChange = () -> {
			followSpeech();
			reportMessage();
		};
		getWindow().addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
		audio = (AudioManager) getSystemService(AUDIO_SERVICE);
		prefs = getPreferences(MODE_PRIVATE);

		root = new FrameLayout(this);
		video = new SurfaceView(this);
		// Opaque black on top of the picture while a channel is tuning, like the blank screen of an analog set.
		shutter = new View(this);
		shutter.setBackgroundColor(0xFF000000);
		root.setBackgroundColor(0xFF000000);
		root.addView(video, new FrameLayout.LayoutParams(-1, -1, Gravity.CENTER));
		root.addView(shutter, new FrameLayout.LayoutParams(-1, -1));
		guide = new TextView(this);
		guide.setTextColor(0xFFFFFFFF);
		guide.setTextSize(28);
		guide.setShadowLayer(6, 0, 0, 0xFF000000);
		guide.setPadding(80, 40, 80, 60);
		guide.setVisibility(View.GONE);
		// One black bar behind the programme text and the clock, for contrast; it is as high as what is shown
		// in it, and gone when nothing is.
		footer = new FrameLayout(this);
		footer.setBackgroundColor(BAR_COLOR);
		footer.addView(guide, new FrameLayout.LayoutParams(-1, -2));
		root.addView(footer, new FrameLayout.LayoutParams(-1, -2, Gravity.BOTTOM));
		tvView = new TvView(this);
		tvView.setVisibility(View.GONE);
		tvView.setCallback(new TvView.TvInputCallback() {
			@Override
			public void onVideoAvailable(String inputId) {
				if (Settings.headerUntilPicture()) hideChannelLabelNow();
			}
		});
		root.addView(tvView, 1, new FrameLayout.LayoutParams(-1, -1));
		notice = new View(this);
		notice.setVisibility(View.GONE);
		root.addView(notice, new FrameLayout.LayoutParams(-1, -1));
		channelLabel = new TextView(this);
		channelLabel.setTextColor(0xFFFFFFFF);
		channelLabel.setTextSize(44);
		channelLabel.setShadowLayer(8, 0, 0, 0xFF000000);
		channelLabel.setPadding(80, 50, 80, 40);
		channelLabel.setVisibility(View.GONE);
		channelLabel.setBackgroundColor(BAR_COLOR);
		root.addView(channelLabel, new FrameLayout.LayoutParams(-1, -2, Gravity.TOP));
		timeLabel = new TextView(this);
		timeLabel.setTextColor(0xFFFFFFFF);
		timeLabel.setTextSize(32);
		timeLabel.setShadowLayer(8, 0, 0, 0xFF000000);
		timeLabel.setPadding(80, 40, 80, 40);
		timeLabel.setVisibility(View.GONE);
		root.addView(timeLabel, new FrameLayout.LayoutParams(-2, -2, Gravity.TOP | Gravity.END));
		clock = new TextView(this);
		clock.setTextColor(0xFFFFFFFF);
		clock.setTextSize(28);
		clock.setShadowLayer(6, 0, 0, 0xFF000000);
		clock.setPadding(80, 40, 80, 60);
		clock.setVisibility(View.GONE);
		footer.addView(clock, new FrameLayout.LayoutParams(-2, -2, Gravity.BOTTOM | Gravity.END));
		list = new ChannelList(this);
		root.addView(list, new FrameLayout.LayoutParams(getResources().getDisplayMetrics().widthPixels * 2 / 5, -1,
				Gravity.START));
		message = new TextView(this);
		message.setBackgroundColor(0xB3000000);
		message.setTextColor(0xFFFFFFFF);
		message.setTextSize(48);
		message.setGravity(Gravity.CENTER);
		message.setPadding(160, 80, 160, 80);
		message.setVisibility(View.GONE);
		root.addView(message, new FrameLayout.LayoutParams(-1, -1));
		call = new VideoCall(this, root, this);
		root.addOnLayoutChangeListener((v, l, t, r, b, ol, ot, or, ob) -> fit());
		setContentView(root);
		applySettings();
	}

	@Override
	protected void onStart() {
		super.onStart();
		if (checkSelfPermission(Manifest.permission.READ_EXTERNAL_STORAGE) == PackageManager.PERMISSION_GRANTED) {
			start();
		} else {
			// Only shown when the app was installed without "adb install -g".
			requestPermissions(new String[] { Manifest.permission.READ_EXTERNAL_STORAGE }, 0);
		}
	}

	@Override
	public void onRequestPermissionsResult(int code, String[] permissions, int[] results) {
		if (results.length > 0 && results[0] == PackageManager.PERMISSION_GRANTED && player == null) start();
	}

	private void start() {
		playlist = Playlist.load();
		// Without a playlist this home screen would be a black screen nobody can leave.
		if (playlist.urls.isEmpty()) {
			otherLauncher();
			return;
		}
		// The channel is remembered by name: the numbers change when the playlist does.
		channel = playlist.names.indexOf(prefs.getString("name", null));
		// Never start on the HDMI input by default.
		if (channel < 0) channel = Playlist.HDMI.equals(playlist.urls.get(0)) && playlist.urls.size() > 1 ? 1 : 0;
		createPlayer();
		watchVolume();
		((ConnectivityManager) getSystemService(CONNECTIVITY_SERVICE)).registerNetworkCallback(
				new NetworkRequest.Builder().addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET).build(), networkUp);
		networkWatched = true;
		tune();
		scheduleNotice();
		checkSchedule();
		prefetchSpeech();
		// Starts the optional MQTT remote control; the service ends itself when there is no /sdcard/mqtt.conf.
		startService(new Intent(this, MqttService.class));
	}

	private void createPlayer() {
		DefaultHttpDataSource.Factory http = new DefaultHttpDataSource.Factory().setAllowCrossProtocolRedirects(true);
		// Start playing after 1 s of media instead of 2.5 s; live TV does not need a big buffer, and a short
		// one makes every channel change faster.
		player = new ExoPlayer.Builder(this)
				.setLoadControl(new DefaultLoadControl.Builder().setBufferDurationsMs(15000, 30000, 1000, 2500).build())
				.setMediaSourceFactory(new DefaultMediaSourceFactory(new DefaultDataSource.Factory(this, http)))
				.build();
		player.addListener(this);
		player.setVideoSurfaceView(video);
		attachBoost(player.getAudioSessionId());
		applyVolume();
	}

	/** A player that keeps failing may be stuck (seen after long runs under memory pressure): a new one starts clean. */
	private void recreatePlayer() {
		Log.i("minitv", "player: recreated before attempt " + (attempts + 1));
		releaseBoost();
		player.removeListener(this);
		player.release();
		createPlayer();
	}

	@Override
	protected void onStop() {
		timeLabel.removeCallbacks(scheduleTick);
		handler.removeCallbacksAndMessages(null);
		holds.removeCallbacksAndMessages(null);
		if (volumeWatched) {
			unregisterReceiver(volumeChanged);
			audio.unregisterAudioDeviceCallback(devicesChanged);
			volumeWatched = false;
		}
		if (networkWatched) {
			((ConnectivityManager) getSystemService(CONNECTIVITY_SERVICE)).unregisterNetworkCallback(networkUp);
			networkWatched = false;
		}
		releaseBoost();
		notices.removeCallbacksAndMessages(null);
		tvView.reset();
		if (player != null) {
			player.release();
			player = null;
		}
		// No call goes on unseen (and the WebView is too big to keep); without a player nothing is tuned again.
		call.hangup("app stopped");
		super.onStop();
	}

	/** Pressing Home while this is already the home screen arrives here. */
	@Override
	protected void onNewIntent(Intent intent) {
		super.onNewIntent(intent);
		if (intent.hasExtra("channel")) {
			channelTo(intent.getIntExtra("channel", -1));
			return;
		}
		if (intent.hasExtra("message_json")) {
			try {
				present(MessageSpec.of(new org.json.JSONObject(intent.getStringExtra("message_json"))));
			} catch (org.json.JSONException e) {
				// MqttService only sends what it parsed and checked.
				Log.w("minitv", "message: " + e);
			}
			return;
		}
		if (intent.hasExtra("message")) {
			present(MessageSpec.of(intent.getStringExtra("message")));
			return;
		}
		if (intent.hasExtra("channel_name")) {
			channelTo(playlist.indexOfName(intent.getStringExtra("channel_name")));
			return;
		}
		if (intent.hasExtra("url")) {
			playUrl(intent.getStringExtra("url"));
			return;
		}
		if (intent.hasExtra("jitsi")) {
			// Only the MQTT service of this process may open a call: this activity can be started by any app.
			if (VideoCall.TOKEN.equals(intent.getStringExtra("token"))) joinCall(intent.getStringExtra("jitsi"));
			return;
		}
		if (intent.hasExtra("reload")) {
			reloadPlaylist();
			return;
		}
		if (!intent.hasCategory(Intent.CATEGORY_HOME)) return;
		endMessage();
		long now = SystemClock.uptimeMillis();
		homePresses = now - lastHomePress < HOME_WINDOW_MS ? homePresses + 1 : 1;
		lastHomePress = now;
		if (homePresses >= HOME_PRESSES) {
			homePresses = 0;
			otherLauncher();
		}
	}

	@Override
	public void onWindowFocusChanged(boolean focus) {
		super.onWindowFocusChanged(focus);
		if (focus) {
			getWindow().getDecorView().setSystemUiVisibility(View.SYSTEM_UI_FLAG_LAYOUT_STABLE
					| View.SYSTEM_UI_FLAG_LAYOUT_HIDE_NAVIGATION | View.SYSTEM_UI_FLAG_LAYOUT_FULLSCREEN
					| View.SYSTEM_UI_FLAG_HIDE_NAVIGATION | View.SYSTEM_UI_FLAG_FULLSCREEN
					| View.SYSTEM_UI_FLAG_IMMERSIVE_STICKY);
		}
	}

	/**
	 * A click or tap on the left half of the screen zaps down, on the right half up (the remote's channel keys for a
	 * mouse or a touch screen). A touch while a message is shown ends the message and does nothing else; nothing happens while the list is open; in a call the touches go to the call page, and a
	 * movement of more than a few dozen pixels is not a click.
	 */
	@Override
	public boolean dispatchTouchEvent(MotionEvent e) {
		// In a call the page of the call gets every touch, as in a browser.
		if (call.active()) return super.dispatchTouchEvent(e);
		switch (e.getActionMasked()) {
		case MotionEvent.ACTION_DOWN:
			touchX = e.getX();
			touchY = e.getY();
			// A click or tap ends a message (its screen and its voice) just as a key does, and does nothing else.
			touchEndedMessage = screenShown || Speech.busy();
			if (touchEndedMessage) {
				Log.i("minitv", "message: ended by touch");
				endMessage();
			}
			return true;
		case MotionEvent.ACTION_UP:
			if (touchEndedMessage) {
				touchEndedMessage = false;
				return true;
			}
			if (Math.abs(e.getX() - touchX) > 48 || Math.abs(e.getY() - touchY) > 48) return true;
			MqttService.keyPressed();
			if (list.isOpen()) return true;
			zap(e.getX() < root.getWidth() / 2f ? -1 : 1);
			return true;
		default:
			return true;
		}
	}

	/**
	 * Every key is swallowed, so the only things that do anything are the channel and volume keys and the
	 * ways out: hold Back, Menu or OK for 2.5 s to open the home app chooser (see also onNewIntent).
	 */
	@Override
	public boolean dispatchKeyEvent(KeyEvent e) {
		MqttService.keyPressed();
		// Any key ends a message (its screen and its voice: the key is the acknowledgement), and does nothing else: not
		// even when it is released (OK would open the list then).
		if (swallowedKey == e.getKeyCode()) {
			if (e.getAction() == KeyEvent.ACTION_UP) swallowedKey = KeyEvent.KEYCODE_UNKNOWN;
			return true;
		}
		if (screenShown || Speech.busy()) {
			if (e.getAction() == KeyEvent.ACTION_DOWN) {
				swallowedKey = e.getKeyCode();
				Log.i("minitv", "message: ended by key " + KeyEvent.keyCodeToString(e.getKeyCode()));
				endMessage();
			}
			return true;
		}
		boolean down = e.getAction() == KeyEvent.ACTION_DOWN;
		// In a call only Back (hang up) and the volume do anything.
		if (call.active()) {
			if (e.getKeyCode() == KeyEvent.KEYCODE_BACK) {
				if (down && e.getRepeatCount() == 0) {
					swallowedKey = KeyEvent.KEYCODE_BACK;
					call.hangup("Back key");
				}
			} else {
				volumeKey(e);
			}
			return true;
		}
		switch (e.getKeyCode()) {
		case KeyEvent.KEYCODE_BACK:
		case KeyEvent.KEYCODE_MENU:
		// The remote's Menu button: remapped to this key (see README), because the vendor window manager swallows
		// the Menu and Settings keys.
		case KeyEvent.KEYCODE_PROG_RED:
		case KeyEvent.KEYCODE_DPAD_CENTER:
		case KeyEvent.KEYCODE_ENTER:
			boolean ok = e.getKeyCode() == KeyEvent.KEYCODE_DPAD_CENTER || e.getKeyCode() == KeyEvent.KEYCODE_ENTER;
			if (!down) {
				holds.removeCallbacksAndMessages(null);
				if (!holdFired) {
					if (ok) {
						if (!guideOn() && launchers == null) break;
						if (list.isOpen()) chooseListed();
						else openList();
					} else if (e.getKeyCode() == KeyEvent.KEYCODE_BACK && list.isOpen()) {
						closeList();
					}
				}
			} else if (e.getRepeatCount() == 0) {
				holdFired = false;
				holds.removeCallbacksAndMessages(null);
				holds.postDelayed(this::chooseHome, HOLD_MS);
			}
			break;
		case KeyEvent.KEYCODE_CHANNEL_UP:
		case KeyEvent.KEYCODE_DPAD_UP:
		case KeyEvent.KEYCODE_PAGE_UP:
			if (down && list.isOpen()) moveList(-1);
			else if (down && e.getRepeatCount() == 0) zap(1);
			break;
		case KeyEvent.KEYCODE_CHANNEL_DOWN:
		case KeyEvent.KEYCODE_DPAD_DOWN:
		case KeyEvent.KEYCODE_PAGE_DOWN:
			if (down && list.isOpen()) moveList(1);
			else if (down && e.getRepeatCount() == 0) zap(-1);
			break;
		// The family's room (setting jitsi_room), to call back with one key; nothing when it is not set.
		case KeyEvent.KEYCODE_PROG_GREEN:
			if (down && e.getRepeatCount() == 0 && !Settings.jitsiRoom().isEmpty()) joinCall(Settings.jitsiRoom());
			break;
		default:
			volumeKey(e);
			break;
		}
		return true;
	}

	private void volumeKey(KeyEvent e) {
		if (e.getAction() != KeyEvent.ACTION_DOWN) return;
		switch (e.getKeyCode()) {
		case KeyEvent.KEYCODE_VOLUME_UP:
		case KeyEvent.KEYCODE_DPAD_RIGHT:
			audio.adjustStreamVolume(AudioManager.STREAM_MUSIC, AudioManager.ADJUST_RAISE, 0);
			break;
		case KeyEvent.KEYCODE_VOLUME_DOWN:
		case KeyEvent.KEYCODE_DPAD_LEFT:
			audio.adjustStreamVolume(AudioManager.STREAM_MUSIC, AudioManager.ADJUST_LOWER, 0);
			break;
		default:
			break;
		}
	}

	/**
	 * Opens the video call for a room name or room address (checked against the configured server here as well), or
	 * ends it for an empty text or "hangup". The TV stops while the call is open and is tuned again after it.
	 */
	private void joinCall(String payload) {
		String text = payload == null ? "" : payload.trim();
		if (text.isEmpty() || text.equalsIgnoreCase("hangup")) {
			call.hangup("hung up remotely");
			return;
		}
		if (player == null) return;
		MqttConfig cfg = MqttConfig.loadOrDefaults(this);
		String url = cfg.jitsi ? VideoCall.roomUrl(text, cfg.jitsiServer, cfg.jitsiAllowed) : null;
		if (url == null) {
			MqttService.error("jitsi", cfg.jitsi ? VideoCall.rejection(text, cfg.jitsiAllowed) : "video calls are off (jitsi=off)", "rejected");
			return;
		}
		closeList();
		hideChannelLabelNow();
		boolean unspoken = active != null && !active.screen() && Speech.busy(messageSpeech);
		if (!call.start(url, Settings.deviceName(), root.indexOfChild(message), Settings.jitsiMaxMinutes())) return;
		// The call stops the speech: a message that was to be heard only is shown, so that it is not lost.
		if (unspoken) showScreen(active);
		handler.removeCallbacksAndMessages(null);
		notices.removeCallbacksAndMessages(null);
		notice.setVisibility(View.GONE);
		player.stop();
		tvView.reset();
		applyVolume();
	}

	@Override
	public void onCallEnded() {
		if (player == null) return;
		scheduleNotice();
		tune();
	}

	/** Broadcast audio is mastered around -24 LUFS, about 10 dB below what other apps play (YouTube: -14), so lift it. */
	private void attachBoost(int session) {
		releaseBoost();
		if (session == C.AUDIO_SESSION_ID_UNSET) return;
		try {
			boost = new LoudnessEnhancer(session);
			boost.setTargetGain(BOOST_MB);
			boost.setEnabled(true);
		} catch (RuntimeException e) {
			// No effect engine: play without the lift rather than lose the picture.
			boost = null;
		}
	}

	private void releaseBoost() {
		if (boost != null) {
			boost.release();
			boost = null;
		}
	}

	private void watchVolume() {
		IntentFilter volumeActions = new IntentFilter("android.media.VOLUME_CHANGED_ACTION");
		// Mute is not a volume change: it has its own broadcasts.
		volumeActions.addAction("android.media.STREAM_MUTE_CHANGED_ACTION");
		volumeActions.addAction("android.media.MASTER_MUTE_CHANGED_ACTION");
		registerReceiver(volumeChanged, volumeActions);
		audio.registerAudioDeviceCallback(devicesChanged, handler);
		volumeWatched = true;
		applyVolume();
	}

	/** The TV (and the speech) follow the system volume, see Volume. */
	private void applyVolume() {
		Speech.applyVolume();
		if (player == null) return;
		float gain = Volume.gain(audio);
		float tv = gain * duck;
		if (duckFade == null || !duckFade.isRunning()) Log.i("minitv", "volume: gain " + gain + ", TV " + tv);
		player.setVolume(tv);
		call.setVolume(gain);
		// The HDMI input is played by the system, not by the player: the same gain applies there.
		tvView.setStreamVolume(tv);
	}

	// The share of its gain the TV keeps: speech_duck_percent while speech plays, so the words are understood over the
	// programme. A quick fade down and a slower one back up sound less abrupt than a jump.
	private float duck = 1f;
	private ValueAnimator duckFade;

	private boolean ducking;

	private void followSpeech() {
		boolean on = Speech.ducked();
		if (on == ducking) return;
		ducking = on;
		if (duckFade != null) duckFade.cancel();
		duckFade = ValueAnimator.ofFloat(duck, on ? Settings.speechDuckPercent() / 100f : 1f).setDuration(on ? 150 : 500);
		duckFade.addUpdateListener(a -> {
			duck = (float) a.getAnimatedValue();
			applyVolume();
			if (a.getAnimatedFraction() >= 1f) Log.i("minitv", "volume: TV at " + Math.round(duck * 100) + "% of its gain");
		});
		duckFade.start();
	}

	/** The other home apps on the system (not this one, not the placeholder shown while the system boots). */
	private List<ActivityInfo> otherLaunchers() {
		Intent home = new Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_HOME);
		List<ActivityInfo> others = new ArrayList<>();
		for (ResolveInfo r : getPackageManager().queryIntentActivities(home, 0)) {
			ActivityInfo a = r.activityInfo;
			if (!a.packageName.equals(getPackageName()) && !a.name.endsWith(".FallbackHome")) others.add(a);
		}
		return others;
	}

	private void launch(ActivityInfo a) {
		startActivity(new Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_HOME)
				.setComponent(new ComponentName(a.packageName, a.name)).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK));
	}

	/** Opens the next home app that is not this one; every call picks the following one. */
	private void otherLauncher() {
		List<ActivityInfo> others = otherLaunchers();
		if (others.isEmpty()) return;
		int next = prefs.getInt("other", 0);
		prefs.edit().putInt("other", next + 1).apply();
		launch(others.get(next % others.size()));
	}

	/**
	 * Holding Back, Menu or OK: the list of the other home apps, to start one of them. The default home app stays
	 * this one; nothing here changes it.
	 */
	private void chooseHome() {
		holdFired = true;
		launchers = otherLaunchers();
		if (launchers.isEmpty()) return;
		List<String> labels = new ArrayList<>();
		for (ActivityInfo a : launchers) labels.add(String.valueOf(a.loadLabel(getPackageManager())));
		list.open(labels, 0);
		clock.setVisibility(View.GONE);
		list.removeCallbacks(closeList);
		list.postDelayed(closeList, LIST_MS);
	}

	/** Short press of OK: all channels, to move through with up and down; OK tunes the selected one. */
	private void openList() {
		list.open(playlist.names, channel);
		showClock(false);
		list.removeCallbacks(closeList);
		list.postDelayed(closeList, LIST_MS);
		fetchSelected();
	}

	private void moveList(int step) {
		list.move(step);
		if (launchers != null) {
			list.removeCallbacks(closeList);
			list.postDelayed(closeList, LIST_MS);
			return;
		}
		showClock(false);
		list.removeCallbacks(closeList);
		list.postDelayed(closeList, LIST_MS);
		list.removeCallbacks(fetchSelected);
		list.postDelayed(fetchSelected, FETCH_DELAY_MS);
	}

	private void closeList() {
		list.removeCallbacks(closeList);
		list.removeCallbacks(fetchSelected);
		list.close();
		launchers = null;
		clock.setVisibility(View.GONE);
	}

	private void chooseListed() {
		int index = list.cursor();
		ActivityInfo other = launchers == null ? null : launchers.get(index);
		closeList();
		if (other != null) launch(other);
		else if (index != channel) channelTo(index);
	}

	private final Runnable closeList = this::closeList;
	private final Runnable fetchSelected = this::fetchSelected;

	/**
	 * What is on, from memory while the programme is still running, else from the guide. Never shows a programme
	 * that is over, and shows nothing, silently, when the guide has nothing.
	 */
	private void whatIsOn(int index, Guide.Callback callback) {
		String id = guideOn() && index < playlist.zappIds.size() ? playlist.zappIds.get(index) : null;
		if (id == null) return;
		long age = SystemClock.elapsedRealtime();
		Guide.Show known = shows.get(id);
		if (known != null && System.currentTimeMillis() < known.endMs && age - fetched.get(id) < GUIDE_MAX_AGE_MS) {
			callback.onShow(known);
			return;
		}
		if (age < guideQuietUntil) return;
		Guide.now(id, show -> handler.post(() -> {
			if (show == null || show.endMs <= System.currentTimeMillis()) {
				MqttService.error("guide", show == null ? "programme guide not available" : "programme guide out of date", "guide");
				guideQuietUntil = SystemClock.elapsedRealtime() + GUIDE_BACKOFF_MS;
				return;
			}
			shows.put(id, show);
			fetched.put(id, SystemClock.elapsedRealtime());
			callback.onShow(show);
		}));
	}

	private void fetchSelected() {
		int index = list.cursor();
		whatIsOn(index, show -> {
			if (list.isOpen()) list.setDetail(index, show.line());
		});
	}

	/** After a channel change: time, title and subtitle for a few seconds. */
	private void showBanner() {
		if (transientUrl != null || !Settings.footer()) return;
		showClock(true);
		int shown = channel;
		whatIsOn(shown, show -> {
			// The answer may arrive after the channel was changed again.
			if (shown != channel) return;
			guide.setText(show.full());
			resetAnimation(footer);
			guide.setVisibility(View.VISIBLE);
			handler.removeCallbacks(hideGuide);
			handler.postDelayed(hideGuide, Settings.footerSeconds() * 1000L);
		});
	}

	// The header slides up and fades, the footer slides down and fades (bar and text together); when the other
	// text of the footer is still shown, only this one fades.
	private final Runnable hideGuide = () -> {
		if (clock.getVisibility() == View.VISIBLE) guide.setVisibility(View.GONE);
		else animateOut(footer, footer.getHeight(), () -> guide.setVisibility(View.GONE));
	};
	private final Runnable hideClock = () -> {
		if (guide.getVisibility() == View.VISIBLE) animateOut(clock, 0, () -> clock.setVisibility(View.GONE));
		else animateOut(footer, footer.getHeight(), () -> clock.setVisibility(View.GONE));
	};

	private void animateOut(View view, float dy, Runnable done) {
		view.animate().cancel();
		view.animate().alpha(0f).translationY(dy).setDuration(OUT_MS).setInterpolator(new AccelerateInterpolator())
				.withEndAction(() -> {
					done.run();
					resetAnimation(view);
				}).start();
	}

	/** Cancels a running fade and puts the view back where it is shown. */
	private void resetAnimation(View view) {
		view.animate().cancel();
		view.setAlpha(1f);
		view.setTranslationY(0f);
	}

	/** Shows the date and time; for a few seconds only when it comes with the banner of a channel change. */
	private final Runnable tickTime = new Runnable() {
		@Override
		public void run() {
			timeLabel.setText(new java.text.SimpleDateFormat("HH:mm", java.util.Locale.GERMANY).format(new java.util.Date()));
			timeLabel.postDelayed(this, 10000);
		}
	};

	/** Follows the settings that change the screen at once: the permanent time. */
	private void applySettings() {
		checkSchedule();
		if (!Settings.speech()) Speech.stop();
		Speech.trim();
		prefetchSpeech();
		timeLabel.removeCallbacks(tickTime);
		if (Settings.clock()) {
			timeLabel.setVisibility(View.VISIBLE);
			tickTime.run();
		} else {
			timeLabel.setVisibility(View.GONE);
		}
	}

	// Which scheduled message was shown in which minute (UTC minutes since 1970), so that a minute fires it once.
	private final java.util.Map<String, Long> scheduleFired = new java.util.HashMap<>();

	/**
	 * Scheduled messages (Settings.schedule): inside the window from..to (it may cross midnight; from = to is the whole
	 * day) the message comes up at the start and then every "every" minutes, like any pushed message. Checked every full
	 * minute while the TV is in front.
	 */
	private void checkSchedule() {
		timeLabel.removeCallbacks(scheduleTick);
		org.json.JSONArray entries = Settings.schedule();
		java.util.Calendar now = java.util.Calendar.getInstance();
		int minute = now.get(java.util.Calendar.HOUR_OF_DAY) * 60 + now.get(java.util.Calendar.MINUTE);
		long total = System.currentTimeMillis() / 60000;
		for (int i = 0; i < entries.length(); i++) {
			org.json.JSONObject o = entries.optJSONObject(i);
			if (o == null || !o.optBoolean("enabled", true)) continue;
			int from = Settings.minutes(o.optString("from", "")), to = Settings.minutes(o.optString("to", ""));
			if (from < 0 || to < 0) continue;
			boolean inside = from == to || (from < to ? minute >= from && minute < to : minute >= from || minute < to);
			int since = (minute - from + 1440) % 1440;
			String id = o.optString("id", "#" + i);
			if (inside && since % Math.max(o.optInt("every", 1), 1) == 0 && !Long.valueOf(total).equals(scheduleFired.get(id))) {
				scheduleFired.put(id, total);
				present(MessageSpec.of(o));
			}
		}
		// Again at the next full minute.
		timeLabel.postDelayed(scheduleTick, 60000 - System.currentTimeMillis() % 60000 + 200);
	}

	private final Runnable scheduleTick = this::checkSchedule;

	private void showClock(boolean temporary) {
		clock.setText(new java.text.SimpleDateFormat("EEE dd.MM.  HH:mm", java.util.Locale.GERMANY).format(new java.util.Date()));
		resetAnimation(footer);
		resetAnimation(clock);
		clock.setVisibility(View.VISIBLE);
		handler.removeCallbacks(hideClock);
		if (temporary) handler.postDelayed(hideClock, Settings.footerSeconds() * 1000L);
	}

	private void zap(int step) {
		channelTo((channel + step + playlist.urls.size()) % playlist.urls.size());
	}

	/**
	 * Makes this the active message, pushed or scheduled, replacing the one before completely; an empty text only ends
	 * the old one. A voice part that cannot be spoken (speech off, a video call, no audio for the text) is shown instead,
	 * so that a reminder is never lost.
	 */
	private void present(MessageSpec m) {
		// A text that replaces a shown one takes over the screen without fading out and in.
		endMessage(m.text.isEmpty() || !m.screen());
		if (m.text.isEmpty()) return;
		active = m;
		int generation = messageGeneration;
		Log.i("minitv", "message: " + m.mode + ", " + m.showSeconds + " s on screen, " + m.repeat + " x with " + m.pause + " s pause");
		if (m.screen()) showScreen(m);
		String refused = m.voice() ? Speech.refusal() : null;
		if (m.voice() && refused == null) {
			// The screen part stays while the text is spoken; show_seconds counts from the end of the last repeat.
			messageSpeech = Speech.say(m.text, Settings.speechVoice(), m.repeat, m.pause, rendered -> {
				if (generation != messageGeneration) return;
				// A text that could not be rendered is shown instead (the failure is already an error event).
				if (!rendered && !m.screen()) showScreen(m);
				startScreenTimer(m, generation);
			});
		} else {
			if (refused != null) {
				MqttService.error("speech", "message not spoken: " + refused, "not-spoken");
				if (!m.screen()) showScreen(m);
			}
			startScreenTimer(m, generation);
		}
		reportMessage();
	}

	private void showScreen(MessageSpec m) {
		fitMessage(m.text);
		message.setText(m.text);
		boolean full = screenShown && message.getVisibility() == View.VISIBLE && message.getAlpha() == 1f;
		screenShown = true;
		if (!full) {
			int fade = ++screenFade;
			message.animate().cancel();
			if (message.getVisibility() != View.VISIBLE) {
				message.setAlpha(0f);
				message.setVisibility(View.VISIBLE);
			}
			Log.i("minitv", "message: fade in from alpha " + message.getAlpha());
			message.animate().alpha(1f).setDuration(MESSAGE_FADE_IN_MS).setInterpolator(new DecelerateInterpolator())
					.withEndAction(() -> {
						if (fade == screenFade) Log.i("minitv", "message: fade in done, alpha " + message.getAlpha());
					});
		}
		closeList();
		reportMessage();
	}

	/** The screen part fades out; the message is over at once (keys work normally during the fade). */
	private void hideScreen() {
		if (!screenShown && message.getVisibility() != View.VISIBLE) return;
		screenShown = false;
		int fade = ++screenFade;
		message.animate().cancel();
		Log.i("minitv", "message: fade out from alpha " + message.getAlpha());
		message.animate().alpha(0f).setDuration(MESSAGE_FADE_OUT_MS).setInterpolator(new AccelerateInterpolator())
				.withEndAction(() -> {
					if (fade != screenFade) return;
					message.setVisibility(View.GONE);
					Log.i("minitv", "message: fade out done, alpha " + message.getAlpha());
				});
		reportMessage();
	}

	/** The screen part goes show_seconds from now (0: it stays until a key is pressed). */
	private void startScreenTimer(MessageSpec m, int generation) {
		if (m.showSeconds <= 0 || !screenShown) return;
		Log.i("minitv", "message: screen part ends in " + m.showSeconds + " s");
		message.postDelayed(() -> {
			if (generation != messageGeneration) return;
			Log.i("minitv", "message: screen part ended after " + m.showSeconds + " s");
			hideScreen();
		}, m.showSeconds * 1000L);
	}

	/** Ends the active message (screen and voice) and any other speech; nothing of it fires afterwards. */
	private void endMessage() {
		endMessage(true);
	}

	/** The same; hide false leaves the text on the screen for the message that replaces it. */
	private void endMessage(boolean hide) {
		messageGeneration++;
		messageSpeech = 0;
		active = null;
		if (hide) hideScreen();
		Speech.stop();
		reportMessage();
	}

	/** <base>/state: whether the screen part is shown, and the mode of the active message while any part of it runs. */
	private void reportMessage() {
		boolean shown = screenShown;
		boolean running = active != null && (shown || Speech.busy(messageSpeech));
		MqttService.messageChanged(shown, running ? active.mode : "");
	}

	/**
	 * As large as fits the screen: the biggest size (160 sp down to 24 sp) at which no word is broken in the
	 * middle and the whole text fits in the height.
	 */
	private void fitMessage(String text) {
		DisplayMetrics metrics = getResources().getDisplayMetrics();
		int width = metrics.widthPixels - message.getPaddingLeft() - message.getPaddingRight();
		int height = metrics.heightPixels - message.getPaddingTop() - message.getPaddingBottom();
		TextPaint paint = new TextPaint(message.getPaint());
		for (int size = 160; size >= 24; size -= 4) {
			paint.setTextSize(size * metrics.scaledDensity);
			float widest = 0;
			for (String word : text.split("\\s+")) widest = Math.max(widest, paint.measureText(word));
			if (widest > width) continue;
			StaticLayout layout = new StaticLayout(text, paint, width, Layout.Alignment.ALIGN_CENTER, 1f, 0f, false);
			if (layout.getHeight() <= height) {
				message.setTextSize(TypedValue.COMPLEX_UNIT_SP, size);
				return;
			}
		}
		message.setTextSize(TypedValue.COMPLEX_UNIT_SP, 24);
	}

	/**
	 * The channel number (0 is HDMI) and name, drawn before the new channel is even tuned. It stays until the first
	 * picture is up, so that a stream that never starts keeps showing which channel it is.
	 */
	private void showChannelLabel() {
		if (!Settings.header()) return;
		channelLabel.setText((Settings.headerNumber() ? channel + "  " : "") + playlist.names.get(channel));
		resetAnimation(channelLabel);
		channelLabel.removeCallbacks(hideChannelLabel);
		channelLabel.setVisibility(View.VISIBLE);
		if (!Settings.headerUntilPicture()) channelLabel.postDelayed(hideChannelLabel, Settings.headerSeconds() * 1000L);
	}

	private final Runnable hideChannelLabel = () ->
			animateOut(channelLabel, -channelLabel.getHeight(), () -> channelLabel.setVisibility(View.GONE));

	/** The first picture is up: the name has done its job, away with it at once, without animation. */
	private void hideChannelLabelNow() {
		channelLabel.removeCallbacks(hideChannelLabel);
		resetAnimation(channelLabel);
		channelLabel.setVisibility(View.GONE);
	}


	/** The programme guide (channel list, programme texts): the setting, else the playlist header. */
	private boolean guideOn() {
		return Settings.guide(playlist.guide);
	}

	private String currentUrl() {
		return transientUrl != null ? transientUrl : playlist.urls.get(channel);
	}

	/** Plays an address that is not in the playlist (asked for over MQTT); the next channel change ends it. */
	private void playUrl(String url) {
		if (player == null || url == null) return;
		transientUrl = url;
		asHls = false;
		noAudio = false;
		closeList();
		hideChannelLabelNow();
		tune();
		scheduleNotice();
	}

	private void reloadPlaylist() {
		Playlist fresh = Playlist.load();
		if (player == null || fresh.urls.isEmpty()) return;
		String name = playlist.names.get(channel);
		playlist = fresh;
		MqttService.playlistChanged();
		prefetchSpeech();
		int index = playlist.names.indexOf(name);
		channel = index < 0 ? 0 : index;
		transientUrl = null;
		closeList();
		hideChannelLabelNow();
		tune();
		scheduleNotice();
	}

	private void channelTo(int index) {
		if (player == null || index < 0 || index >= playlist.urls.size()) return;
		if (index != channel || transientUrl != null) announceChannel();
		transientUrl = null;
		channel = index;
		showChannelLabel();
		asHls = false;
		noAudio = false;
		list.close();
		prefs.edit().putString("name", playlist.names.get(channel)).apply();
		tune();
		scheduleNotice();
		bannerPending = true;
	}

	// The station name is spoken only once the channel has stayed this long, so that zapping does not queue speech.
	private static final int ANNOUNCE_DELAY_MS = 600;
	private final Runnable announce = () -> {
		// Never over a message or a call, and never for an address played by cmd/play.
		if (call.active() || transientUrl != null || screenShown || Speech.busy()) return;
		Speech.announce(playlist.spoken.get(channel));
	};

	private void announceChannel() {
		Speech.stopAnnouncement();
		// Not on handler: tune() clears that right after.
		message.removeCallbacks(announce);
		if (Settings.speakChannel()) message.postDelayed(announce, ANNOUNCE_DELAY_MS);
	}

	/**
	 * Renders ahead what will be spoken: scheduled reminders (so that they work when the network is down at night) and,
	 * with speak_channel, the names of the visible channels (so that they come at once and offline).
	 */
	private void prefetchSpeech() {
		// A set/config changes its keys one by one, each reporting a change: ask once, after the last of them.
		message.removeCallbacks(prefetch);
		message.postDelayed(prefetch, PREFETCH_DELAY_MS);
	}

	private static final int PREFETCH_DELAY_MS = 500;
	private final Runnable prefetch = this::prefetchNow;

	private void prefetchNow() {
		List<String> texts = new ArrayList<>(Settings.spokenSchedule());
		if (Settings.speakChannel() && playlist != null) {
			texts.addAll(playlist.spoken.subList(0, Math.min(playlist.spoken.size(), CHANNEL_NAMES_PREFETCH_MAX)));
		}
		if (!texts.isEmpty()) Speech.prefetch(texts, null);
	}

	private static final int CHANNEL_NAMES_PREFETCH_MAX = 50;

	private void tune() {
		// A retry can still be on its way when the player was released in onStop.
		if (player == null) return;
		handler.removeCallbacksAndMessages(null);
		// A channel chosen during a call (over MQTT) is remembered and tuned when the call ends.
		if (call.active()) return;
		retryPending = false;
		guide.setVisibility(View.GONE);
		if (!list.isOpen()) clock.setVisibility(View.GONE);
		resetAnimation(footer);
		Log.i("minitv", "tune channel " + channel + " at " + SystemClock.elapsedRealtime() + " ms since boot");
		if (Playlist.HDMI.equals(currentUrl())) {
			tuneHdmi();
			return;
		}
		handler.postDelayed(attemptTimeout, ATTEMPT_MS);
		if (tvView.getVisibility() == View.VISIBLE) {
			tvView.reset();
			tvView.setVisibility(View.GONE);
			video.setVisibility(View.VISIBLE);
		}
		shutter.setVisibility(View.VISIBLE);
		size = VideoSize.UNKNOWN;
		MediaItem.Builder item = new MediaItem.Builder().setUri(currentUrl());
		// Many IPTV lists have HLS URLs without a .m3u8 suffix; onPlayerError flips this on a parse error.
		if (asHls) item.setMimeType(MimeTypes.APPLICATION_M3U8);
		player.setTrackSelectionParameters(player.getTrackSelectionParameters().buildUpon()
				.setTrackTypeDisabled(C.TRACK_TYPE_AUDIO, noAudio).build());
		player.setMediaItem(item.build());
		player.prepare();
		player.play();
	}

	/**
	 * The HDMI channel: the first HDMI input of the TV input framework (HW1, the only one wired on this projector),
	 * shown in a TvView. Without a signal the input service draws its own "no signal" screen there.
	 */
	private void tuneHdmi() {
		player.stop();
		player.clearMediaItems();
		video.setVisibility(View.GONE);
		shutter.setVisibility(View.GONE);
		tvView.setVisibility(View.VISIBLE);
		String best = null;
		// A device without the TV input framework (a smart display) has no manager service and getTvInputList throws.
		java.util.List<TvInputInfo> inputs;
		try {
			inputs = ((TvInputManager) getSystemService(TV_INPUT_SERVICE)).getTvInputList();
		} catch (NullPointerException e) {
			inputs = java.util.Collections.emptyList();
		}
		for (TvInputInfo info : inputs) {
			if (info.getType() == TvInputInfo.TYPE_HDMI && (best == null || info.getId().compareTo(best) < 0)) {
				best = info.getId();
			}
		}
		pictureUp = true;
		notice.setVisibility(View.GONE);
		report(true, "hdmi");
		if (Settings.footer()) showClock(true);
		if (best == null) MqttService.error("hdmi", "no HDMI input found", "no-hdmi-input");
		if (best != null) tvView.tune(best, TvContract.buildChannelUriForPassthroughInput(best));
	}

	/** What the MQTT service publishes as the state of the TV; on the red or yellow screen with the code of the last stream error. */
	private void report(boolean hasPicture, String screenKind) {
		boolean notice = screenKind.startsWith("red") || screenKind.startsWith("yellow");
		MqttService.report(transientUrl != null ? "URL" : playlist.names.get(channel), currentUrl(), hasPicture, screenKind,
				notice ? streamError : "");
	}

	/** A channel (or address) was selected: its attempts start from zero, and so does the clock of the red screen. */
	private void scheduleNotice() {
		attempts = 0;
		streamError = "";
		startNoticeClock();
	}

	/**
	 * The red screen comes when there is no picture no_picture_seconds after the clock started, the yellow one after
	 * twice that without internet; the attempts go on in the background either way.
	 */
	private void startNoticeClock() {
		notices.removeCallbacksAndMessages(null);
		notice.setVisibility(View.GONE);
		pictureUp = false;
		if (Playlist.HDMI.equals(currentUrl())) {
			report(true, "hdmi");
			return;
		}
		report(false, "no-picture-yet");
		long ms = Settings.noPictureSeconds() * 1000L;
		notices.postDelayed(() -> showNotice(false), ms);
		notices.postDelayed(() -> showNotice(true), 2 * ms);
	}

	private void showNotice(boolean late) {
		if (pictureUp || call.active()) return;
		boolean online = isOnline();
		if (!online && !late) return;
		notice.setBackgroundColor(online ? 0xFFC00000 : 0xFFE0C000);
		report(false, online ? "red-no-picture" : "yellow-no-internet");
		int seconds = Settings.noPictureSeconds() * (late ? 2 : 1);
		MqttService.error(online ? "no-picture" : "no-internet", online ? "no picture after " + seconds + " s" : "no internet connection", online ? "no-picture" : "no-internet");
		notice.setVisibility(View.VISIBLE);
	}

	private boolean isOnline() {
		ConnectivityManager cm = (ConnectivityManager) getSystemService(CONNECTIVITY_SERVICE);
		NetworkCapabilities caps = cm.getNetworkCapabilities(cm.getActiveNetwork());
		return caps != null && caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED);
	}

	private final Runnable stallRetry = () -> failed("the stream stalled (buffering for " + STALL_MS / 1000 + " s)", "stall");
	private final Runnable attemptTimeout = () -> failed("no picture within " + ATTEMPT_MS / 1000 + " s", "no-first-frame");
	private final Runnable nextAttempt = () -> {
		if (attempts % 2 == 0) recreatePlayer();
		tune();
	};

	/**
	 * An attempt failed: the stream is tried again, like a set that keeps looking for its signal, sooner at first and
	 * then every RETRY_MS, never giving up while the channel is selected; every second time with a new player. The
	 * pending attempt is on handler, which every tune and channel change clears, so an old channel is never tried again.
	 */
	private void failed(String message, String code) {
		attempts++;
		streamError = code;
		MqttService.error("stream", message + ", attempt " + attempts, code, attempts);
		handler.removeCallbacks(attemptTimeout);
		handler.removeCallbacks(stallRetry);
		handler.removeCallbacks(nextAttempt);
		// A stream that had a picture and lost it starts the clock of the red screen again.
		if (pictureUp && !Playlist.HDMI.equals(currentUrl())) startNoticeClock();
		long delay = attempts <= BACKOFF_MS.length ? BACKOFF_MS[attempts - 1] : RETRY_MS;
		Log.i("minitv", "attempt " + attempts + " failed (" + code + "), next in " + delay + " ms");
		retryPending = true;
		handler.postDelayed(nextAttempt, delay);
	}

	private void fit() {
		int w = root.getWidth(), h = root.getHeight();
		if (w == 0 || h == 0 || size.width == 0 || size.height == 0) return;
		float aspect = size.width * size.pixelWidthHeightRatio / size.height;
		int vw = w, vh = Math.round(w / aspect);
		if (vh > h) {
			vh = h;
			vw = Math.round(h * aspect);
		}
		video.setLayoutParams(new FrameLayout.LayoutParams(vw, vh, Gravity.CENTER));
	}

	@Override
	public void onVideoSizeChanged(VideoSize s) {
		size = s;
		fit();
	}

	@Override
	public void onAudioSessionIdChanged(int sessionId) {
		attachBoost(sessionId);
	}

	@Override
	public void onRenderedFirstFrame() {
		// The picture is up; now is a good time to load the speech model for the voice button.
		if (!voicePreloaded) {
			voicePreloaded = true;
			startService(new Intent(this, VoiceService.class));
		}
		Log.i("minitv", "first frame at " + SystemClock.elapsedRealtime() + " ms since boot");
		handler.removeCallbacks(attemptTimeout);
		attempts = 0;
		shutter.setVisibility(View.GONE);
		pictureUp = true;
		notice.setVisibility(View.GONE);
		if (Settings.headerUntilPicture()) hideChannelLabelNow();
		report(true, "ok");
		if (bannerPending) {
			bannerPending = false;
			showBanner();
		}
	}

	@Override
	public void onPlayerError(PlaybackException e) {
		// E.g. AC-3 that this projector's audio output cannot play: keep the picture, drop the sound.
		if (e.errorCode == PlaybackException.ERROR_CODE_AUDIO_TRACK_INIT_FAILED) noAudio = true;
		if (e.errorCode / 1000 == 3) asHls = !asHls;
		failed(e.getErrorCodeName() + (e.getCause() == null ? "" : ": " + e.getCause()), e.getErrorCodeName());
	}

	@Override
	public void onPlaybackStateChanged(int state) {
		buffering = state == Player.STATE_BUFFERING;
		handler.removeCallbacks(stallRetry);
		if (buffering) handler.postDelayed(stallRetry, STALL_MS);
		if (state == Player.STATE_ENDED) failed("the stream ended", "ended");
	}
}
