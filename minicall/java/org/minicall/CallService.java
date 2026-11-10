/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
package org.minicall;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.graphics.Point;
import android.media.AudioDeviceInfo;
import android.media.AudioManager;
import android.net.ConnectivityManager;
import android.net.LinkProperties;
import android.net.Network;
import android.media.ToneGenerator;
import android.os.Build;
import android.os.Handler;
import android.os.IBinder;
import android.os.Looper;
import android.util.Log;
import android.view.Surface;
import android.view.WindowManager;
import java.io.File;
import java.io.IOException;
import java.net.Inet6Address;
import java.net.InetAddress;
import java.util.ArrayList;
import java.util.List;
import java.util.UUID;
import org.json.JSONObject;

/**
 * Keeps the helper running (it has to stay registered to be reachable) and runs the calls: dialing, answering
 * callers from the contact list by itself, ringing, and the call screen.
 */
public final class CallService extends Service {
	private static final String TAG = "minicall";
	static final String ACTION_CALL = "org.minicall.action.CALL";
	static final String ACTION_HANGUP = "org.minicall.action.HANGUP";
	static final String EXTRA_TO = "to";
	// Seen by the wake word service (echowake), which then gives up the microphone for the call.
	private static final File IN_CALL = new File("/sdcard/minicall/in-call");
	private static final int RING_MS = 2500;
	private static final int NO_ANSWER_MS = 60000;
	/** Answered but not connected after this long: ICE or DTLS will not get through, end it instead of hanging. */
	private static final int CONNECT_MS = 15000;
	/** A device answers by itself within seconds: no answer by then means it is off or offline. */
	private static final int DEVICE_NO_ANSWER_MS = 15000;
	private static final int BUSY_MS = 3000;

	enum State { IDLE, CALLING, RINGING, IN_CALL }

	interface Listener {
		/** On the main thread. */
		void onStateChanged();
	}

	private static CallService instance;

	private final Handler main = new Handler(Looper.getMainLooper());
	private Settings settings;
	private Helper helper;
	private AudioBridge audio;
	private VideoBridge video;
	private ToneGenerator tones;
	private Listener listener;
	private State state = State.IDLE;
	private String peer = "";
	private String status = "";
	private boolean registered;
	private String error = "";
	private static final int HELPER_RETRY_MS = 2000;
	private boolean destroyed;
	private boolean restartPending;
	private Network network;
	private Signaling signaling;
	// The web call in progress or being set up (its MQTT id), else null.
	private String webId;
	// For a call this device made to another device: that device's topic; null when this device answered.
	private String webPeerTopic;

	/** Starts the service from anywhere: Android 8 and newer only allow that as a foreground service. */
	static void start(Context ctx, Intent intent) {
		if (Build.VERSION.SDK_INT >= 26) {
			ctx.startForegroundService(intent);
		} else {
			ctx.startService(intent);
		}
	}

	// Android 8 and newer show no notification without a channel, and then refuse the foreground service.
	@SuppressWarnings("deprecation")
	private Notification notification() {
		Notification.Builder b;
		if (Build.VERSION.SDK_INT >= 26) {
			NotificationManager nm = (NotificationManager) getSystemService(NOTIFICATION_SERVICE);
			nm.createNotificationChannel(new NotificationChannel("calls", "Calls", NotificationManager.IMPORTANCE_LOW));
			b = new Notification.Builder(this, "calls");
		} else {
			b = new Notification.Builder(this);
		}
		return b.setContentTitle("minicall").setSmallIcon(android.R.drawable.sym_call_incoming).build();
	}

	static CallService get() {
		return instance;
	}

	@Override
	public IBinder onBind(Intent intent) {
		return null;
	}

	@Override
	public void onCreate() {
		super.onCreate();
		instance = this;
		startForeground(1, notification());
		setInCall(false);
		tones = new ToneGenerator(AudioManager.STREAM_MUSIC, 80);
		try {
			settings = Settings.load(this);
			String prefix = "minicall." + UUID.randomUUID();
			audio = new AudioBridge(prefix, settings.micSource, settings.micGainDb, settings.speakerGainDb,
					settings.callAudio, settings.webrtcAudio);
			video = new VideoBridge(prefix, this::displayDegrees);
			startHelper();
			watchNetwork();
			if (settings.hasMqtt()) startSignaling();
		} catch (IOException e) {
			Log.e(TAG, "not started", e);
			error = e.getMessage();
		}
	}

	/**
	 * The helper with a configuration for the network of the moment (its DNS servers above all). It is started again
	 * whenever it ends, and restarted when the network changes while no call runs: the helper keeps the addresses
	 * and resolvers it started with, and on mobile data the WLAN's are useless.
	 */
	private void startHelper() {
		if (destroyed) return;
		try {
			Helper h = new Helper(getApplicationInfo().nativeLibraryDir + "/libbaresip.so",
					new File(getFilesDir(), "baresip"), config(), accounts(), new Helper.Listener() {
						@Override
						public void onEvent(JSONObject e) {
							main.post(() -> handleEvent(e));
						}

						@Override
						public void onExit() {
							main.post(CallService.this::helperExited);
						}
					});
			helper = h;
		} catch (IOException e) {
			Log.e(TAG, "helper not started", e);
			error = e.getMessage();
			main.postDelayed(this::startHelper, HELPER_RETRY_MS);
		}
	}

	/** A command for the helper; while it starts again after an exit there is none, and the command is dropped. */
	private void command(String name, Object params) {
		if (helper == null) {
			Log.w(TAG, "no call engine running: " + name + " dropped");
		} else if (params instanceof JSONObject) {
			helper.command(name, (JSONObject) params);
		} else {
			helper.command(name, (String) params);
		}
	}

	private void helperExited() {
		ended();
		helper = null;
		if (destroyed) return;
		// A pause, so that a helper that cannot run does not start again and again at full speed.
		main.postDelayed(this::startHelper, HELPER_RETRY_MS);
	}

	private void watchNetwork() {
		ConnectivityManager cm = (ConnectivityManager) getSystemService(CONNECTIVITY_SERVICE);
		cm.registerDefaultNetworkCallback(new ConnectivityManager.NetworkCallback() {
			@Override
			public void onAvailable(Network n) {
				main.post(() -> networkChanged(n));
			}
		});
	}

	private void networkChanged(Network n) {
		// The first report is the network the helper was just started on.
		if (network == null || network.equals(n)) {
			network = n;
			return;
		}
		network = n;
		Log.i(TAG, "network changed: the call engine starts again");
		restartPending = true;
		restartIfIdle();
	}

	/** After a network change, once no call runs (a call in progress keeps its connection as long as it can). */
	private void restartIfIdle() {
		if (!restartPending || state != State.IDLE || helper == null) return;
		restartPending = false;
		// Ending stdin ends the helper; onExit starts the new one.
		helper.stop();
	}

	private void startSignaling() {
		try {
			signaling = new Signaling(settings, new Signaling.Listener() {
				@Override
				public void onOffer(String id, String name, String from, JSONObject offer) {
					main.post(() -> webOffer(id, name, from, offer));
				}

				@Override
				public void onAnswer(String id, JSONObject answer) {
					main.post(() -> {
						if (!id.equals(webId) || webPeerTopic == null) return;
						if (answer.has("error")) {
							Log.i(TAG, peer + " did not take the call: " + answer.optString("error"));
							refused(answer.optString("error"));
							return;
						}
						video.setTargetShape(Signaling.screenOf(answer));
						command("webrtc_remote", answer);
						connecting();
					});
				}

				@Override
				public void onBye(String id) {
					main.post(() -> {
						if (!id.equals(webId)) return;
						command("webrtc_close", null);
						// The other side ended it: no bye back.
						if (webPeerTopic != null) signaling.forget(webPeerTopic, webId);
						webId = null;
						ended();
					});
				}
			});
			int[] own = screenSize();
			signaling.setScreen(own[0], own[1]);
		} catch (Exception e) {
			Log.e(TAG, "mqtt not started", e);
			error = "MQTT: " + e.getMessage();
		}
	}

	/**
	 * A page or another device calls (a page also after a ring); the offer is answered after ringing, or at once when
	 * this device asked the page to call. Devices are known by their topic, pages by their name.
	 */
	private void webOffer(String id, String name, String from, JSONObject offer) {
		// MQTT can deliver a message twice (QoS 1, and a subscription renewed after a reconnect): the copy of the
		// offer being answered must not be refused as busy.
		if (id.equals(webId)) return;
		video.setTargetShape(Signaling.screenOf(offer));
		Settings.Contact c = from != null ? settings.deviceContact(from) : settings.webContact(name);
		boolean wanted = from == null && state == State.CALLING && webId == null && c != null && c.name.equals(peer);
		if (!wanted && (state != State.IDLE || settings.webAnswer.equals("off")
				|| (settings.webAnswer.equals("contacts") && c == null))) {
			Log.i(TAG, "rejected web call from " + name);
			signaling.refuse(id, state != State.IDLE ? "busy" : "not accepted");
			return;
		}
		webId = id;
		peer = c != null ? c.name : name;
		if (wanted) {
			command("webrtc_answer", offer);
			connecting();
			return;
		}
		set(State.RINGING, "");
		setInCall(true);
		tones.startTone(ToneGenerator.TONE_SUP_RINGTONE, RING_MS);
		main.postDelayed(() -> {
			if (state == State.RINGING && id.equals(webId)) {
				tones.stopTone();
				command("webrtc_answer", offer);
				connecting();
			}
		}, RING_MS);
		showCallScreen();
	}

	@Override
	public int onStartCommand(Intent intent, int flags, int startId) {
		if (intent != null && ACTION_CALL.equals(intent.getAction())) {
			call(intent.getStringExtra(EXTRA_TO));
		} else if (intent != null && ACTION_HANGUP.equals(intent.getAction())) {
			hangup();
		}
		return START_STICKY;
	}

	@Override
	public void onDestroy() {
		destroyed = true;
		if (signaling != null) signaling.close();
		if (helper != null) helper.stop();
		if (audio != null) audio.close();
		if (video != null) video.close();
		tones.release();
		setInCall(false);
		instance = null;
		super.onDestroy();
	}

	// --- for the screens

	void setListener(Listener l) {
		listener = l;
	}

	State state() {
		return state;
	}

	String peer() {
		return peer;
	}

	String status() {
		return status;
	}

	String error() {
		return error;
	}

	boolean registered() {
		return registered;
	}

	Settings settings() {
		return settings;
	}

	VideoBridge video() {
		return video;
	}

	/** A contact's name or one of the contacts' addresses; anything else is refused. */
	void call(String to) {
		if (helper == null || to == null) return;
		if (state != State.IDLE) {
			Log.i(TAG, "call refused: busy");
			return;
		}
		Settings.Contact c = settings.contactNamed(to);
		if (c == null) c = settings.contactFor(to);
		if (c == null) {
			Log.w(TAG, "call refused: " + to + " is not a contact");
			return;
		}
		peer = c.name;
		// A device sends its screen shape with its answer; until then (and for SIP and web pages) the own shape.
		video.setTargetShape(null);
		if ((c.uri.startsWith("web:") || c.uri.startsWith("mqtt:")) && signaling == null) {
			Log.w(TAG, "call refused: " + c.name + " needs MQTT, which is not set up");
			return;
		}
		if (c.uri.startsWith("web:")) {
			set(State.CALLING, "calling");
			setInCall(true);
			signaling.ring(c.uri.substring(4));
		} else if (c.uri.startsWith("mqtt:")) {
			// The offer goes out once the helper has gathered its candidates (event OFFER).
			webId = UUID.randomUUID().toString().replace("-", "").substring(0, 16);
			webPeerTopic = c.uri.substring(5);
			set(State.CALLING, "calling");
			setInCall(true);
			command("webrtc_offer", null);
		} else {
			set(State.CALLING, "calling");
			setInCall(true);
			command("dial", c.uri);
		}
		main.postDelayed(noAnswer, c.uri.startsWith("mqtt:") ? DEVICE_NO_ANSWER_MS : NO_ANSWER_MS);
		showCallScreen();
	}

	/** Answered (by us or the other side): from now on CONNECT_MS for the media to get through. */
	private void connecting() {
		main.removeCallbacks(noAnswer);
		main.removeCallbacks(noConnection);
		main.postDelayed(noConnection, CONNECT_MS);
		status = "connecting";
		notifyListener();
	}

	/** The other side said no: busy gets the busy tone for a moment, so the caller hears why. */
	private void refused(String why) {
		main.removeCallbacks(noAnswer);
		main.removeCallbacks(noConnection);
		if (webId != null) {
			command("webrtc_close", null);
			// Refused, so there is nothing to say bye to.
			if (webPeerTopic != null && signaling != null) signaling.forget(webPeerTopic, webId);
			webId = null;
			webPeerTopic = null;
		}
		boolean busy = why.toLowerCase(java.util.Locale.ROOT).contains("busy") || why.startsWith("486");
		status = busy ? "busy" : "not accepted";
		notifyListener();
		tones.stopTone();
		if (busy) tones.startTone(ToneGenerator.TONE_SUP_BUSY, BUSY_MS);
		main.postDelayed(endAfterBusy, BUSY_MS);
	}

	void hangup() {
		if (webId != null) {
			command("webrtc_close", null);
		} else if (helper != null && state != State.IDLE) {
			command("hangup", null);
		}
		ended();
	}

	private final Runnable noAnswer = () -> {
		if (state == State.CALLING) {
			Log.i(TAG, peer + " did not answer");
			hangup();
		}
	};

	/** One object, so that a new call can cancel it: a method reference would be a new one each time. */
	private final Runnable endAfterBusy = this::ended;

	private final Runnable noConnection = () -> {
		if (state != State.IN_CALL && state != State.IDLE) {
			Log.i(TAG, "no connection to " + peer + " within " + CONNECT_MS / 1000 + " s, call ended");
			hangup();
		}
	};

	private final Runnable answer = () -> {
		if (state == State.RINGING) {
			command("accept", null);
			tones.stopTone();
			connecting();
		}
	};

	private void handleEvent(JSONObject e) {
		String type = e.optString("type");
		if (e.optString("class").equals("webrtc")) {
			webEvent(type, e);
			return;
		}
		String peerUri = e.optString("peeruri");
		switch (type) {
			case "REGISTER_OK":
				registered = true;
				error = "";
				notifyListener();
				break;
			case "REGISTER_FAIL":
				registered = false;
				error = "registration failed: " + e.optString("param");
				notifyListener();
				break;
			case "CALL_INCOMING": {
				Settings.Contact c = settings.contactFor(peerUri);
				if (state != State.IDLE || c == null || !settings.autoAnswerContacts) {
					// Strangers never get the camera; neither does a second caller.
					Log.i(TAG, "rejected call from " + peerUri);
					command("hangup", null);
					break;
				}
				peer = c.name;
				video.setTargetShape(null);
				set(State.RINGING, "");
				setInCall(true);
				tones.startTone(ToneGenerator.TONE_SUP_RINGTONE, RING_MS);
				main.postDelayed(answer, RING_MS);
				showCallScreen();
				break;
			}
			case "CALL_RINGING":
				if (state == State.CALLING) status = "ringing";
				notifyListener();
				break;
			case "CALL_ESTABLISHED":
				main.removeCallbacks(noAnswer);
				main.removeCallbacks(noConnection);
				set(State.IN_CALL, "");
				break;
			case "CALL_CLOSED": {
				String why = e.optString("param");
				Log.i(TAG, "call closed: " + why);
				if (state == State.CALLING && (why.startsWith("486") || why.toLowerCase(java.util.Locale.ROOT).contains("busy"))) {
					refused(why);
				} else {
					ended();
				}
				break;
			}
			default:
				break;
		}
	}

	private void webEvent(String type, JSONObject e) {
		if (webId == null) return;
		switch (type) {
			case "OFFER":
				if (webPeerTopic != null) signaling.offerTo(webPeerTopic, webId, e.optString("sdp"));
				break;
			case "ANSWER":
				signaling.answer(webId, e.optString("sdp"));
				break;
			case "ESTABLISHED":
				main.removeCallbacks(noAnswer);
				main.removeCallbacks(noConnection);
				set(State.IN_CALL, "");
				break;
			case "CLOSED":
				Log.i(TAG, "web call closed: " + e.optString("param"));
				ended();
				break;
			default:
				break;
		}
	}

	private void ended() {
		if (webId != null && signaling != null) {
			if (webPeerTopic != null) {
				signaling.byeTo(webPeerTopic, webId);
				signaling.forget(webPeerTopic, webId);
			} else {
				signaling.bye(webId);
			}
		}
		webId = null;
		webPeerTopic = null;
		main.removeCallbacks(answer);
		main.removeCallbacks(noAnswer);
		main.removeCallbacks(noConnection);
		main.removeCallbacks(endAfterBusy);
		tones.stopTone();
		if (state != State.IDLE) tones.startTone(ToneGenerator.TONE_PROP_PROMPT, 300);
		setInCall(false);
		set(State.IDLE, "");
		restartIfIdle();
	}

	private void set(State s, String st) {
		state = s;
		status = st;
		notifyListener();
	}

	private void notifyListener() {
		if (listener != null) listener.onStateChanged();
	}

	private void showCallScreen() {
		startActivity(new Intent(this, CallActivity.class).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK));
	}

	/**
	 * For the time of a call: the in-call flag for the wake word service, and on a phone call mode with the
	 * loudspeaker, in which its echo canceller works.
	 */
	private void setInCall(boolean on) {
		AudioManager am = (AudioManager) getSystemService(AUDIO_SERVICE);
		// Plugging in or out during a call routes again, on every device (a USB microphone on a TV box too).
		if (on && !watchingDevices) {
			am.registerAudioDeviceCallback(deviceChange, main);
		} else if (!on && watchingDevices) {
			am.unregisterAudioDeviceCallback(deviceChange);
		}
		watchingDevices = on;
		if (settings != null && settings.callAudio) callMode(on);
		if (on) route("call start");
		setInCallFlag(on);
	}

	/** Outputs a call may use, best first: what the user plugged in or paired wins over the loudspeaker. */
	private static final int[] CALL_OUTPUTS = { AudioDeviceInfo.TYPE_WIRED_HEADSET, AudioDeviceInfo.TYPE_WIRED_HEADPHONES,
			AudioDeviceInfo.TYPE_USB_HEADSET, AudioDeviceInfo.TYPE_BLE_HEADSET, AudioDeviceInfo.TYPE_BLUETOOTH_SCO,
			AudioDeviceInfo.TYPE_BUILTIN_SPEAKER };

	/** Headphones plugged in or out during a call: route again (Android does not do it for a set device). */
	private final android.media.AudioDeviceCallback deviceChange = new android.media.AudioDeviceCallback() {
		@Override
		public void onAudioDevicesAdded(AudioDeviceInfo[] added) {
			route("added " + names(added));
		}

		@Override
		public void onAudioDevicesRemoved(AudioDeviceInfo[] removed) {
			route("removed " + names(removed));
		}
	};
	/** Call mode (phones): the output is chosen by minicall. */
	private boolean inCallMode;
	private boolean watchingDevices;

	/** Microphones a call may use, best first; none of them: the device's own. */
	private static final int[] CALL_INPUTS = { AudioDeviceInfo.TYPE_WIRED_HEADSET, AudioDeviceInfo.TYPE_USB_HEADSET,
			AudioDeviceInfo.TYPE_USB_DEVICE, AudioDeviceInfo.TYPE_BLE_HEADSET, AudioDeviceInfo.TYPE_BLUETOOTH_SCO };

	/** The plugged-in or paired microphone, else null (the built-in one). */
	private static AudioDeviceInfo callInput(AudioManager am) {
		AudioDeviceInfo[] ins = am.getDevices(AudioManager.GET_DEVICES_INPUTS);
		for (int type : CALL_INPUTS) {
			for (AudioDeviceInfo d : ins) {
				if (d.getType() == type) return d;
			}
		}
		return null;
	}

	private static String names(AudioDeviceInfo[] ds) {
		StringBuilder b = new StringBuilder();
		for (AudioDeviceInfo d : ds) {
			b.append(b.length() == 0 ? "" : ", ").append(d.getType()).append(' ').append(d.getProductName());
		}
		return b.toString();
	}

	private void callMode(boolean on) {
		AudioManager am = (AudioManager) getSystemService(AUDIO_SERVICE);
		am.setMode(on ? AudioManager.MODE_IN_COMMUNICATION : AudioManager.MODE_NORMAL);
		inCallMode = on;
		Log.i(TAG, "audio: call mode " + (on ? "on" : "off") + ", mode now " + am.getMode());
		// On: the output follows in route(). Off: Android's own routing again.
		if (on) return;
		if (Build.VERSION.SDK_INT >= 31) {
			am.clearCommunicationDevice();
		} else {
			speakerphone(am, false);
		}
	}

	/**
	 * The call's output: a headset or headphones when there are some (3.5 mm, USB, Bluetooth), else the
	 * loudspeaker, so a phone works as a speakerphone. Android 12+ routes calls by the communication device
	 * (setSpeakerphoneOn() is ignored there); older ones by the speakerphone switch, with headsets taking over by
	 * themselves when it is off.
	 */
	private void route(String why) {
		AudioManager am = (AudioManager) getSystemService(AUDIO_SERVICE);
		// The microphone too: a headset's or a separate USB/Bluetooth one, before the device's own.
		AudioDeviceInfo in = callInput(am);
		if (audio != null) audio.preferInput(in);
		Log.i(TAG, "audio (" + why + "): microphone " + (in == null ? "built-in" : in.getProductName() + " type " + in.getType()));
		if (!inCallMode) return;
		if (Build.VERSION.SDK_INT >= 31) {
			java.util.List<AudioDeviceInfo> avail = am.getAvailableCommunicationDevices();
			for (int type : CALL_OUTPUTS) {
				for (AudioDeviceInfo d : avail) {
					if (d.getType() == type) {
						boolean ok = am.setCommunicationDevice(d);
						Log.i(TAG, "audio (" + why + "): " + d.getProductName() + " type " + type + (ok ? "" : " refused"));
						return;
					}
				}
			}
			Log.w(TAG, "audio (" + why + "): no output for calls");
		} else {
			boolean headset = false;
			for (AudioDeviceInfo d : am.getDevices(AudioManager.GET_DEVICES_OUTPUTS)) {
				for (int i = 0; i < CALL_OUTPUTS.length - 1; i++) {
					headset |= d.getType() == CALL_OUTPUTS[i];
				}
			}
			speakerphone(am, !headset);
			Log.i(TAG, "audio (" + why + "): " + (headset ? "headset" : "loudspeaker"));
		}
	}

	@SuppressWarnings("deprecation") // setSpeakerphoneOn() is what Android 11 and older have.
	private static void speakerphone(AudioManager am, boolean on) {
		am.setSpeakerphoneOn(on);
	}


	private static void setInCallFlag(boolean on) {
		if (on) {
			File dir = IN_CALL.getParentFile();
			try {
				if ((dir.isDirectory() || dir.mkdirs()) && (IN_CALL.isFile() || IN_CALL.createNewFile())) return;
			} catch (IOException ignored) {
				// Logged below.
			}
			Log.w(TAG, "could not create " + IN_CALL + ": the wake word service keeps the microphone");
		} else if (IN_CALL.exists() && !IN_CALL.delete()) {
			Log.w(TAG, "could not delete " + IN_CALL);
		}
	}

	/** How far the screen is turned from the device's natural orientation, in degrees. */
	@SuppressWarnings("deprecation") // getDefaultDisplay() is what Android 7 has.
	private int displayDegrees() {
		WindowManager wm = (WindowManager) getSystemService(WINDOW_SERVICE);
		switch (wm.getDefaultDisplay().getRotation()) {
			case Surface.ROTATION_90: return 90;
			case Surface.ROTATION_180: return 180;
			case Surface.ROTATION_270: return 270;
			default: return 0;
		}
	}

	/**
	 * The screen's shape as a landscape camera size, at most 1280x720 pixels: the camera then fills the far end's
	 * picture of this device without wasting pixels (the Echo Show, 960x480, gets 864x480).
	 */
	@SuppressWarnings("deprecation") // getDefaultDisplay() is what Android 7 has.
	private int[] screenSize() {
		Point p = new Point();
		((WindowManager) getSystemService(WINDOW_SERVICE)).getDefaultDisplay().getRealSize(p);
		double w = Math.max(p.x, p.y), h = Math.min(p.x, p.y);
		double scale = Math.min(1, Math.sqrt(1280.0 * 720 / (w * h)));
		// Multiples of 16: baresip refuses odd sizes (and falls back to 640x480), encoders want blocks of 16.
		return new int[] {(int) (w * scale) / 16 * 16, (int) (h * scale) / 16 * 16};
	}

	// --- helper configuration

	private String config() {
		StringBuilder b = new StringBuilder();
		b.append("sip_listen 0.0.0.0:5060\n");
		// Android has no resolv.conf for the helper's resolver.
		for (InetAddress dns : dnsServers()) {
			String a = dns.getHostAddress();
			b.append("dns_server ").append(dns instanceof Inet6Address ? "[" + a + "]" : a).append(":53\n");
		}
		b.append("audio_source sockaudio,").append(audio.micName).append('\n');
		b.append("audio_player sockaudio,").append(audio.speakerName).append('\n');
		// The Echo Show's processed microphone stream only carries sound at 16 kHz.
		b.append("ausrc_srate 16000\nauplay_srate 48000\nausrc_channels 1\nauplay_channels 1\n");
		b.append("video_source sockvideo,").append(video.cameraName).append('\n');
		b.append("sockvideo_display ").append(video.displayName).append('\n');
		b.append("video_display fakevideo,nil\n");
		int[] size = settings.videoWidth > 0 ? new int[] {settings.videoWidth, settings.videoHeight} : screenSize();
		// About 0.07 bit per pixel and frame for H.264 conversation video, unless set.
		int bitrate = settings.videoBitrate > 0 ? settings.videoBitrate
				: Math.max(500000, (int) (size[0] * (long) size[1] * settings.videoFps * 7 / 100));
		Log.i(TAG, "video: about " + size[0] + "x" + size[1] + " at " + settings.videoFps + " fps, " + bitrate
				+ " bit/s");
		b.append("video_size ").append(size[0]).append('x').append(size[1]).append('\n');
		b.append("video_fps ").append(settings.videoFps).append('\n');
		b.append("video_bitrate ").append(bitrate).append('\n');
		b.append("webrtc_stun stun:").append(settings.stun.get(0)).append('\n');
		for (String m : new String[] {"opus", "g711", "auconv", "auresamp", "sockaudio", "sockvideo", "fakevideo", "ice",
				"stun", "turn", "srtp", "dtls_srtp"}) {
			b.append("module ").append(m).append(".so\n");
		}
		if (settings.webrtcAudio) {
			// After the resampler: it then works on the codec's 48 kHz in both directions.
			b.append("module webrtc_aec.so\n");
			b.append("webrtc_ns_level high\n");
			// The Echo Show's echo-cancelled stream is very quiet: a fixed lift first, then the adaptive gain.
			double fixed = !Double.isNaN(settings.micGainDb) ? settings.micGainDb : settings.callAudio ? 0 : 15;
			b.append("webrtc_fixed_gain_db ").append((int) fixed).append('\n');
		}
		for (String m : new String[] {"account", "contact", "menu", "ctrl_stdio"}) {
			b.append("module_app ").append(m).append(".so\n");
		}
		return b.toString();
	}

	private List<InetAddress> dnsServers() {
		ConnectivityManager cm = (ConnectivityManager) getSystemService(CONNECTIVITY_SERVICE);
		Network n = cm.getActiveNetwork();
		LinkProperties lp = n != null ? cm.getLinkProperties(n) : null;
		List<InetAddress> list = new ArrayList<>();
		if (lp != null) {
			for (InetAddress a : lp.getDnsServers()) {
				// A link-local resolver would need a scope the config cannot carry.
				if (!a.isLinkLocalAddress()) list.add(a);
			}
		}
		if (list.isEmpty()) Log.w(TAG, "no DNS server known: names will not resolve");
		return list;
	}

	private String accounts() {
		String common = ";answermode=manual;medianat=ice;stunserver=\"stun:" + settings.stun.get(0)
				+ "\";mediaenc=srtp";
		if (!settings.hasAccount()) {
			// No registrar: reachable by address only, in the local network or with a forwarded port.
			return "\"" + settings.displayName + "\" <sip:minicall@0.0.0.0>;regint=0" + common + "\n";
		}
		String aor = "sip:" + settings.sipUser + "@" + settings.sipDomain;
		String t = ";transport=" + settings.sipTransport;
		return "\"" + settings.displayName + "\" <" + aor + t + ">;auth_pass=" + settings.sipPassword
				+ ";outbound=\"sip:" + settings.sipDomain + t + "\";regint=600" + common + "\n";
	}
}
