package org.minitv;

import android.app.Service;
import android.content.BroadcastReceiver;
import android.content.ComponentName;
import android.content.Context;
import android.content.Intent;
import android.content.IntentFilter;
import android.content.SharedPreferences;
import android.content.pm.PackageManager;
import android.media.AudioManager;
import android.os.IBinder;
import android.os.SystemClock;
import android.util.Log;
import com.hivemq.client.mqtt.MqttClient;
import com.hivemq.client.mqtt.MqttClientSslConfig;
import com.hivemq.client.mqtt.datatypes.MqttQos;
import com.hivemq.client.mqtt.mqtt3.Mqtt3AsyncClient;
import com.hivemq.client.mqtt.mqtt3.Mqtt3ClientBuilder;
import com.hivemq.client.mqtt.mqtt3.message.connect.Mqtt3ConnectBuilder;
import com.hivemq.client.mqtt.mqtt3.message.connect.connack.Mqtt3ConnAck;
import com.hivemq.client.mqtt.mqtt3.message.publish.Mqtt3Publish;
import java.io.ByteArrayInputStream;
import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.FileInputStream;
import java.io.InputStream;
import java.nio.charset.StandardCharsets;
import java.security.KeyStore;
import java.security.cert.Certificate;
import java.security.cert.CertificateFactory;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.ScheduledFuture;
import java.util.concurrent.TimeUnit;
import javax.net.ssl.TrustManagerFactory;
import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

/**
 * The optional remote control over MQTT (HiveMQ client, TLS, last will). Nothing runs without /sdcard/mqtt.conf.
 *
 * Topics below the configured base: status (retained: online, or offline with the reason; the broker publishes the
 * offline message itself, the last will, when the connection breaks), state (retained: channel, picture, volume),
 * set/channel, set/volume, cmd/play, cmd/jitsi, cmd/call, cmd/say, cmd/shell, cmd/reload-playlist, and result (answers of shell commands).
 */
public class MqttService extends Service {
	// What MainActivity reports; published as <base>/state.
	private static volatile MqttService instance;
	private static volatile String channelName = "";
	private static volatile boolean picture;
	private static volatile String screen = "ok";
	private static volatile long lastKey;
	private static volatile boolean messageOn;
	private static volatile String messageMode = "";
	private static volatile String currentUrl = "";
	// The room of the video call that is open, empty when there is none.
	private static volatile String callRoom = "";
	private static volatile long watchingSince = System.currentTimeMillis();
	// The newest error (also kept across restarts, in the retained telemetry) and errors that happened while the
	// broker could not be reached; the same error is reported once a minute at most.
	private static volatile JSONObject lastError;
	// The newest stream error (every failed attempt), kept apart because the red screen's event replaces last_error.
	private static volatile JSONObject lastStreamError;
	private static volatile boolean lastStreamErrorSaved = true;
	// The code of the stream error that explains the red or yellow screen while it is shown, else empty.
	private static volatile String streamError = "";
	private static final java.util.ArrayDeque<JSONObject> PENDING_ERRORS = new java.util.ArrayDeque<>();
	private static final java.util.Map<String, Long> RECENT_ERRORS = new java.util.HashMap<>();
	private static final int ERROR_REPEAT_S = 60;
	private static final int PENDING_ERRORS_MAX = 50;

	private static final int KEEP_ALIVE_S = 60;
	private static final int STATE_DELAY_MS = 500;
	private static final int STATE_EVERY_S = 60;
	private static final int SHELL_TIMEOUT_S = 30;
	private static final int SHELL_OUTPUT_MAX = 16384;

	private MqttConfig cfg;
	private Mqtt3AsyncClient client;
	private SharedPreferences prefs;
	private final ScheduledExecutorService worker = Executors.newSingleThreadScheduledExecutor();
	private final ExecutorService shells = Executors.newCachedThreadPool();
	private ScheduledFuture<?> statePending;
	private boolean stopped;
	// Topics whose retained command this device just cleared: the empty message that comes back is not a command.
	private final java.util.Set<String> clearing = java.util.Collections.synchronizedSet(new java.util.HashSet<>());
	private long since;
	private JSONObject previousShutdown;

	/** Called by MainActivity whenever the channel, the picture or the screen kind changes. */
	static void report(String name, String url, boolean hasPicture, String screenKind, String streamErrorCode) {
		streamError = streamErrorCode == null ? "" : streamErrorCode;
		String newName = name == null ? "" : name, newUrl = url == null ? "" : url;
		if (!newName.equals(channelName) || !newUrl.equals(currentUrl)) watchingSince = System.currentTimeMillis();
		currentUrl = newUrl;
		channelName = newName;
		picture = hasPicture;
		screen = screenKind;
		MqttService s = instance;
		if (s != null) s.scheduleState();
	}

	/**
	 * Something went wrong (a stream that fails, no picture, no internet, no HDMI input, a crash, ...): published as an
	 * event on <base>/errors and kept as last_error in the retained telemetry. Nothing is shown on the screen for it.
	 */
	static void error(String type, String message, String code) {
		error(type, message, code, 0);
	}

	/** The same for a failed stream attempt (attempt above 0): also kept as last_stream_error, even when not sent again. */
	static void error(String type, String message, String code, int attempt) {
		try {
			JSONObject e = new JSONObject().put("time", iso(System.currentTimeMillis())).put("type", type)
					.put("channel", channelName).put("url", currentUrl).put("code", code == null ? "" : code)
					.put("message", message == null ? "" : message);
			if (attempt > 0) e.put("attempt", attempt);
			if ("stream".equals(type)) {
				lastStreamError = e;
				lastStreamErrorSaved = false;
			}
			String key = type + "|" + currentUrl + "|" + code;
			long now = SystemClock.elapsedRealtime();
			synchronized (RECENT_ERRORS) {
				Long last = RECENT_ERRORS.get(key);
				if (last != null && now - last < ERROR_REPEAT_S * 1000L) return;
				RECENT_ERRORS.put(key, now);
			}
			lastError = e;
			MqttService s = instance;
			if (s != null) s.worker.execute(() -> s.publishError(e));
			else synchronized (PENDING_ERRORS) {
				if (PENDING_ERRORS.size() < PENDING_ERRORS_MAX) PENDING_ERRORS.add(e);
			}
		} catch (JSONException ignored) {
			// Cannot happen: only plain strings are put.
		}
	}

	/** Remembers an uncaught exception, to report it after the restart. */
	static void installCrashHandler(Context context) {
		Thread.UncaughtExceptionHandler previous = Thread.getDefaultUncaughtExceptionHandler();
		SharedPreferences p = context.getSharedPreferences("mqtt", MODE_PRIVATE);
		Thread.setDefaultUncaughtExceptionHandler((thread, throwable) -> {
			java.io.StringWriter text = new java.io.StringWriter();
			throwable.printStackTrace(new java.io.PrintWriter(text));
			String trace = text.toString();
			p.edit().putString("crash", iso(System.currentTimeMillis()) + "\n" + trace.substring(0, Math.min(trace.length(), 1500)))
					.commit();
			if (previous != null) previous.uncaughtException(thread, throwable);
		});
	}

	private static String iso(long millis) {
		java.text.SimpleDateFormat f = new java.text.SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss'Z'", java.util.Locale.US);
		f.setTimeZone(java.util.TimeZone.getTimeZone("UTC"));
		return f.format(new java.util.Date(millis));
	}

	/** The playlist was reloaded: the channel list in Home Assistant has to follow. */
	static void playlistChanged() {
		MqttService s = instance;
		if (s != null) s.worker.execute(() -> {
			s.publishDiscovery();
			s.publishPlaylist();
		});
	}

	/** Called when the screen part of a message comes or goes, and when the active message (its mode, or "") changes. */
	static void messageChanged(boolean shown, String mode) {
		if (shown == messageOn && mode.equals(messageMode)) return;
		messageOn = shown;
		messageMode = mode;
		MqttService s = instance;
		if (s != null) s.scheduleState();
	}

	/** Called when a video call opens (with its room) or ends (null). */
	static void callChanged(String room) {
		callRoom = room == null ? "" : room;
		MqttService s = instance;
		if (s != null) s.scheduleState();
	}

	/** Called when speech starts or ends. */
	static void speakingChanged() {
		MqttService s = instance;
		if (s != null) s.scheduleState();
	}

	static void keyPressed() {
		lastKey = SystemClock.elapsedRealtime();
	}

	@Override
	public int onStartCommand(Intent intent, int flags, int startId) {
		if (client == null && !stopped) begin();
		return START_STICKY;
	}

	@Override
	public IBinder onBind(Intent intent) {
		return null;
	}

	private void begin() {
		Settings.init(this);
		Playlist.init(this);
		Speech.init(this);
		// The voices of an installed Ivona engine are known only after it has started: the config follows then.
		IvonaTts.load(this, this::publishConfig);
		cfg = MqttConfig.load(this);
		if (cfg != null) {
			Speech.setAllowedByConfig(cfg.speech);
			Settings.defaultName = cfg.name;
		}
		Log.i("minitv", cfg == null ? "mqtt: no usable mqtt.conf" : "mqtt: starting");
		if (cfg == null) {
			stopSelf();
			return;
		}
		prefs = getSharedPreferences("mqtt", MODE_PRIVATE);
		since = System.currentTimeMillis() / 1000;
		String previous = !prefs.contains("clean") ? "first start"
				: prefs.getBoolean("clean", false) ? prefs.getString("reason", "unknown") : "unclean (power loss, crash or killed)";
		boolean wasClean = prefs.getBoolean("clean", false);
		// When the previous run ended: exact after a clean stop, else the last heartbeat (up to a minute early).
		try {
			previousShutdown = new JSONObject()
					.put("time", iso(wasClean ? prefs.getLong("stopped_at", 0) : prefs.getLong("alive", 0)))
					.put("approximate", !wasClean).put("reason", previous);
			if (prefs.getLong("alive", 0) == 0 && !wasClean) previousShutdown = null;
			String saved = prefs.getString("last_error", null);
			if (saved != null && lastError == null) lastError = new JSONObject(saved);
			saved = prefs.getString("last_stream_error", null);
			if (saved != null && lastStreamError == null) lastStreamError = new JSONObject(saved);
		} catch (JSONException e) {
			previousShutdown = null;
		}
		prefs.edit().putBoolean("clean", false).apply();
		String crash = prefs.getString("crash", null);
		if (crash != null) {
			prefs.edit().remove("crash").apply();
			error("crash", crash, "uncaught-exception");
		}
		try {
			Mqtt3ClientBuilder b = MqttClient.builder().useMqttVersion3().identifier(cfg.clientId)
					.serverHost(cfg.host).serverPort(cfg.port);
			if (cfg.tls) b = b.sslConfig(sslConfig());
			client = b.automaticReconnectWithDefaultConfig().addConnectedListener(context -> onConnected(previous))
					.addDisconnectedListener(context -> Log.w("minitv", "mqtt disconnected: " + context.getCause()))
					.buildAsync();
		} catch (Exception e) {
			Log.w("minitv", "mqtt: " + e);
			stopSelf();
			return;
		}
		instance = this;
		registerReceiver(shutdown, new IntentFilter(Intent.ACTION_SHUTDOWN));
		// The last will: the broker publishes this when the connection breaks without a goodbye.
		Mqtt3ConnectBuilder.Send<CompletableFuture<Mqtt3ConnAck>> connect = client.connectWith();
		if (!cfg.user.isEmpty()) {
			connect = connect.simpleAuth().username(cfg.user).password(cfg.password.getBytes(StandardCharsets.UTF_8))
					.applySimpleAuth();
		}
		connect.keepAlive(KEEP_ALIVE_S).cleanSession(true).willPublish().topic(cfg.topic + "/status")
				.payload(offline("connection lost (crash, power loss or network)", false).getBytes(StandardCharsets.UTF_8))
				.qos(MqttQos.AT_LEAST_ONCE).retain(true).applyWillPublish().send()
				.whenComplete((ack, error) -> Log.i("minitv", error != null ? "mqtt connect failed: " + error
						: "mqtt connected to " + cfg.host + ":" + cfg.port));
		worker.scheduleWithFixedDelay(this::publishState, STATE_EVERY_S, STATE_EVERY_S, TimeUnit.SECONDS);
	}

	/** The system's trust store, or the CA file of mqtt.conf instead. */
	private MqttClientSslConfig sslConfig() throws Exception {
		TrustManagerFactory tmf = TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm());
		if (cfg.caFile == null) {
			tmf.init((KeyStore) null);
		} else {
			KeyStore store = KeyStore.getInstance(KeyStore.getDefaultType());
			store.load(null, null);
			try (InputStream in = new FileInputStream(new File(cfg.caFile))) {
				int i = 0;
				for (Certificate c : CertificateFactory.getInstance("X.509").generateCertificates(in)) {
					store.setCertificateEntry("ca" + i++, c);
				}
			}
			tmf.init(store);
		}
		// Server certificate and host name are always verified.
		return MqttClientSslConfig.builder().trustManagerFactory(tmf).build();
	}

	private void onConnected(String previous) {
		publish("status", online(previous), true);
		worker.execute(this::publishDiscovery);
		worker.execute(this::publishPlaylist);
		worker.execute(this::publishConfig);
		client.subscribeWith().topicFilter(cfg.topic + "/set/#").qos(MqttQos.AT_LEAST_ONCE)
				.callback(this::onMessage).send();
		client.subscribeWith().topicFilter(cfg.topic + "/cmd/#").qos(MqttQos.AT_LEAST_ONCE)
				.callback(this::onMessage).send();
		publishState();
		flushErrors();
	}

	private void publishError(JSONObject e) {
		if (client == null || !client.getState().isConnected()) {
			synchronized (PENDING_ERRORS) {
				if (PENDING_ERRORS.size() < PENDING_ERRORS_MAX) PENDING_ERRORS.add(e);
			}
			return;
		}
		publish("errors", e.toString(), false);
		prefs.edit().putString("last_error", e.toString()).apply();
		scheduleState();
	}

	private void flushErrors() {
		while (true) {
			JSONObject e;
			synchronized (PENDING_ERRORS) {
				e = PENDING_ERRORS.poll();
			}
			if (e == null) return;
			publish("errors", e.toString(), false);
			prefs.edit().putString("last_error", e.toString()).apply();
		}
	}

	/**
	 * Home Assistant MQTT discovery: retained configuration messages, one per entity, below
	 * <prefix>/<component>/<device>/<entity>/config. Entities of switched-off features get an empty retained
	 * message, which removes them again.
	 */
	private void publishDiscovery() {
		if (!cfg.discovery || client == null) return;
		try {
			String uid = cfg.topic.replaceAll("[^A-Za-z0-9]", "_");
			String state = cfg.topic + "/state";
			Playlist playlist = Playlist.load();
			JSONArray options = new JSONArray();
			for (String name : playlist.names) options.put(name);
			entity(uid, "select", "channel", new JSONObject().put("name", "Channel").put("icon", "mdi:television")
					.put("state_topic", state).put("value_template", "{{ value_json.channel }}")
					.put("command_topic", cfg.topic + "/set/channel").put("options", options));
			entity(uid, "number", "volume", new JSONObject().put("name", "Volume").put("icon", "mdi:volume-high")
					.put("state_topic", state).put("value_template", "{{ value_json.volume }}")
					.put("command_topic", cfg.topic + "/set/volume").put("min", 0).put("max", 100).put("step", 1)
					.put("unit_of_measurement", "%"));
			entity(uid, "binary_sensor", "picture", new JSONObject().put("name", "Picture").put("icon", "mdi:image")
					.put("state_topic", state).put("value_template", "{{ 'ON' if value_json.picture else 'OFF' }}"));
			String config = cfg.topic + "/config";
			for (String[] sw : new String[][] { { "footer", "Footer" }, { "header", "Header" }, { "header_number", "Header with number" }, { "clock", "Clock" }, { "guide", "Programme guide" } }) {
				entity(uid, "switch", sw[0], new JSONObject().put("name", sw[1]).put("entity_category", "config")
						.put("state_topic", config).put("value_template", "{{ 'ON' if value_json." + sw[0] + " else 'OFF' }}")
						.put("command_topic", cfg.topic + "/set/" + sw[0]).put("payload_on", "on").put("payload_off", "off"));
			}
			entity(uid, "select", "header_mode", new JSONObject().put("name", "Header stays").put("entity_category", "config")
					.put("state_topic", config).put("value_template", "{{ value_json.header_mode }}")
					.put("command_topic", cfg.topic + "/set/header_mode").put("options", new JSONArray().put("picture").put("seconds")));
			entity(uid, "number", "header_seconds", new JSONObject().put("name", "Header seconds").put("entity_category", "config")
					.put("state_topic", config).put("value_template", "{{ value_json.header_seconds }}")
					.put("command_topic", cfg.topic + "/set/header_seconds").put("min", Settings.SECONDS_MIN)
					.put("max", Settings.SECONDS_MAX).put("step", 1).put("unit_of_measurement", "s"));
			entity(uid, "number", "no_picture_seconds", new JSONObject().put("name", "Red screen after").put("entity_category", "config")
					.put("state_topic", config).put("value_template", "{{ value_json.no_picture_seconds }}")
					.put("command_topic", cfg.topic + "/set/no_picture_seconds").put("min", Settings.numberMin("no_picture_seconds"))
					.put("max", Settings.numberMax("no_picture_seconds")).put("step", 1).put("unit_of_measurement", "s"));
			entity(uid, "number", "footer_seconds", new JSONObject().put("name", "Footer seconds").put("entity_category", "config")
					.put("state_topic", config).put("value_template", "{{ value_json.footer_seconds }}")
					.put("command_topic", cfg.topic + "/set/footer_seconds").put("min", Settings.SECONDS_MIN)
					.put("max", Settings.SECONDS_MAX).put("step", 1).put("unit_of_measurement", "s"));
			entity(uid, "binary_sensor", "message_shown", new JSONObject().put("name", "Message shown")
					.put("icon", "mdi:message-text").put("state_topic", state)
					.put("value_template", "{{ 'ON' if value_json.message else 'OFF' }}"));
			entity(uid, "select", "message_mode", new JSONObject().put("name", "Message presentation").put("entity_category", "config")
					.put("state_topic", config).put("value_template", "{{ value_json.message_mode }}")
					.put("command_topic", cfg.topic + "/set/message_mode")
					.put("options", new JSONArray().put(MessageSpec.TEXT).put(MessageSpec.SPEECH).put(MessageSpec.BOTH)));
			entity(uid, "number", "message_seconds", new JSONObject().put("name", "Message seconds on screen").put("entity_category", "config")
					.put("state_topic", config).put("value_template", "{{ value_json.message_seconds }}")
					.put("command_topic", cfg.topic + "/set/message_seconds").put("min", Settings.numberMin("message_seconds"))
					.put("max", Settings.numberMax("message_seconds")).put("step", 1).put("unit_of_measurement", "s"));
			entity(uid, "text", "message", new JSONObject().put("name", "Message").put("icon", "mdi:message-text")
					.put("command_topic", cfg.topic + "/cmd/message").put("max", 255)
					.put("retain", true));
			String telemetry = cfg.topic + "/telemetry";
			entity(uid, "sensor", "last_boot", new JSONObject().put("name", "Last boot").put("entity_category", "diagnostic")
					.put("device_class", "timestamp").put("state_topic", telemetry).put("value_template", "{{ value_json.boot }}"));
			entity(uid, "sensor", "online_since", new JSONObject().put("name", "Online since").put("entity_category", "diagnostic")
					.put("device_class", "timestamp").put("state_topic", telemetry)
					.put("value_template", "{{ value_json.online_since }}"));
			entity(uid, "sensor", "watching_since", new JSONObject().put("name", "Watching since")
					.put("device_class", "timestamp").put("state_topic", telemetry)
					.put("value_template", "{{ value_json.watching_since }}"));
			entity(uid, "sensor", "stream_url", new JSONObject().put("name", "Stream address").put("entity_category", "diagnostic")
					.put("state_topic", telemetry).put("value_template", "{{ value_json.url[:250] }}"));
			entity(uid, "sensor", "last_shutdown", new JSONObject().put("name", "Last shutdown").put("entity_category", "diagnostic")
					.put("device_class", "timestamp").put("state_topic", telemetry)
					.put("value_template", "{{ value_json.previous_shutdown.time if value_json.previous_shutdown else none }}")
					.put("json_attributes_topic", telemetry)
					.put("json_attributes_template", "{{ value_json.previous_shutdown | tojson }}"));
			entity(uid, "sensor", "last_error", new JSONObject().put("name", "Last error").put("entity_category", "diagnostic")
					.put("icon", "mdi:alert-circle").put("state_topic", telemetry)
					.put("value_template", "{{ (value_json.last_error.type ~ ': ' ~ value_json.last_error.message)[:250] if value_json.last_error else 'none' }}")
					.put("json_attributes_topic", telemetry)
					.put("json_attributes_template", "{{ value_json.last_error | tojson }}"));
			entity(uid, "sensor", "screen", new JSONObject().put("name", "Screen").put("entity_category", "diagnostic")
					.put("state_topic", state).put("value_template", "{{ value_json.screen }}"));
			entity(uid, "sensor", "uptime", new JSONObject().put("name", "Uptime").put("entity_category", "diagnostic")
					.put("device_class", "duration").put("unit_of_measurement", "s").put("state_topic", state)
					.put("value_template", "{{ value_json.uptime_s }}"));
			// Online: how the previous run ended; offline: why it went offline.
			entity(uid, "sensor", "last_stop", new JSONObject().put("name", "Last stop").put("entity_category", "diagnostic")
					.put("state_topic", cfg.topic + "/status").put("value_template",
							"{{ value_json.reason if value_json.state == 'offline' else value_json.previous_stop }}"));
			entity(uid, "button", "reload_playlist", new JSONObject().put("name", "Reload playlist")
					.put("entity_category", "config").put("command_topic", cfg.topic + "/cmd/reload-playlist")
					.put("payload_press", "1"));
			entity(uid, "text", "play_url", cfg.url ? new JSONObject().put("name", "Play address")
					.put("entity_category", "config").put("command_topic", cfg.topic + "/cmd/play").put("max", 255) : null);
			entity(uid, "text", "video_call", cfg.jitsi ? new JSONObject().put("name", "Video call").put("icon", "mdi:video")
					.put("command_topic", cfg.topic + "/cmd/jitsi").put("state_topic", state)
					.put("value_template", "{{ value_json.room }}").put("max", 255) : null);
			entity(uid, "binary_sensor", "in_call", cfg.jitsi ? new JSONObject().put("name", "In call").put("icon", "mdi:video")
					.put("state_topic", state).put("value_template", "{{ 'ON' if value_json.call else 'OFF' }}") : null);
			entity(uid, "text", "jitsi_room", cfg.jitsi ? new JSONObject().put("name", "Video call room (green key)")
					.put("entity_category", "config").put("state_topic", config).put("value_template", "{{ value_json.jitsi_room }}")
					.put("command_topic", cfg.topic + "/set/jitsi_room").put("max", 64) : null);
			entity(uid, "number", "jitsi_max_minutes", cfg.jitsi ? new JSONObject().put("name", "Video call time limit")
					.put("entity_category", "config").put("state_topic", config).put("value_template", "{{ value_json.jitsi_max_minutes }}")
					.put("command_topic", cfg.topic + "/set/jitsi_max_minutes").put("min", 0).put("max", Settings.CALL_MINUTES_MAX)
					.put("step", 1).put("unit_of_measurement", "min") : null);
			entity(uid, "text", "say", cfg.speech ? new JSONObject().put("name", "Say").put("icon", "mdi:account-voice")
					.put("command_topic", cfg.topic + "/cmd/say").put("max", 255) : null);
			for (String[] sw : new String[][] { { "speech", "Speech" }, { "speak_channel", "Speak channel name" } }) {
				entity(uid, "switch", sw[0], cfg.speech ? new JSONObject().put("name", sw[1]).put("entity_category", "config")
						.put("state_topic", config).put("value_template", "{{ 'ON' if value_json." + sw[0] + " else 'OFF' }}")
						.put("command_topic", cfg.topic + "/set/" + sw[0]).put("payload_on", "on").put("payload_off", "off") : null);
			}
			// The switch of the former setting speak_messages (now message_mode): an empty config makes Home Assistant drop it.
			entity(uid, "switch", "speak_messages", null);
			for (String[] n : new String[][] { { "speech_volume", "Speech volume", "%" }, { "speech_boost_db", "Speech boost", "dB" },
					{ "speech_duck_percent", "TV volume while speaking", "%" }, { "speech_cache_mb", "Speech cache size", "MB" },
					{ "speech_repeat", "Speech repeats", "x" }, { "speech_pause", "Pause between repeats", "s" } }) {
				entity(uid, "number", n[0], cfg.speech ? new JSONObject().put("name", n[1]).put("entity_category", "config")
						.put("state_topic", config).put("value_template", "{{ value_json." + n[0] + " }}")
						.put("command_topic", cfg.topic + "/set/" + n[0]).put("min", Settings.numberMin(n[0]))
						.put("max", Settings.numberMax(n[0])).put("step", 1).put("unit_of_measurement", n[2]) : null);
			}
			for (String[] text : new String[][] { { "speech_voice", "Speech voice" }, { "speech_rate", "Speech rate" } }) {
				entity(uid, "text", text[0], cfg.speech ? new JSONObject().put("name", text[1]).put("entity_category", "config")
						.put("state_topic", config).put("value_template", "{{ value_json." + text[0] + " }}")
						.put("command_topic", cfg.topic + "/set/" + text[0]).put("max", 64) : null);
			}
			entity(uid, "text", "shell", cfg.shell ? new JSONObject().put("name", "Shell command")
					.put("entity_category", "config").put("command_topic", cfg.topic + "/cmd/shell").put("max", 255) : null);
			entity(uid, "sensor", "shell_result", cfg.shell ? new JSONObject().put("name", "Shell result")
					.put("entity_category", "diagnostic").put("state_topic", cfg.topic + "/result")
					.put("value_template", "{{ value_json.exit if value_json.exit is defined else value_json.message }}")
					.put("json_attributes_topic", cfg.topic + "/result") : null);
		} catch (JSONException e) {
			Log.w("minitv", "mqtt discovery: " + e);
		}
	}

	/** Publishes one entity's discovery message; null removes the entity. */
	private void entity(String uid, String component, String id, JSONObject config) throws JSONException {
		String topic = cfg.discoveryPrefix + "/" + component + "/" + uid + "/" + id + "/config";
		byte[] payload = new byte[0];
		if (config != null) {
			config.put("unique_id", uid + "_" + id).put("has_entity_name", true)
					.put("device", new JSONObject().put("identifiers", new JSONArray().put(uid)).put("name", Settings.deviceName())
							.put("manufacturer", "minitv").put("model", "TV app"))
					.put("availability", new JSONArray().put(new JSONObject().put("topic", cfg.topic + "/status")
							.put("value_template", "{{ value_json.state }}").put("payload_available", "online")
							.put("payload_not_available", "offline")));
			payload = config.toString().getBytes(StandardCharsets.UTF_8);
		}
		client.publishWith().topic(topic).payload(payload).qos(MqttQos.AT_LEAST_ONCE).retain(true).send();
	}

	private String online(String previousStop) {
		try {
			return new JSONObject().put("state", "online").put("since", since).put("previous_stop", previousStop)
					.put("url", cfg.url).put("shell", cfg.shell).put("jitsi", cfg.jitsi).put("speech", cfg.speech).toString();
		} catch (JSONException e) {
			return "{\"state\":\"online\"}";
		}
	}

	/** The last will cannot know the time (it is fixed when the connection is made); a clean stop adds it. */
	private static String offline(String reason, boolean stamped) {
		try {
			JSONObject o = new JSONObject().put("state", "offline").put("reason", reason);
			if (stamped) o.put("time", iso(System.currentTimeMillis()));
			return o.toString();
		} catch (JSONException e) {
			return "{\"state\":\"offline\"}";
		}
	}

	private void publish(String suffix, String payload, boolean retain) {
		if (client == null) return;
		client.publishWith().topic(cfg.topic + "/" + suffix).payload(payload.getBytes(StandardCharsets.UTF_8))
				.qos(MqttQos.AT_LEAST_ONCE).retain(retain).send();
	}

	private synchronized void scheduleState() {
		if (statePending == null || statePending.isDone()) {
			statePending = worker.schedule(this::publishState, STATE_DELAY_MS, TimeUnit.MILLISECONDS);
		}
	}

	private void publishState() {
		try {
			AudioManager audio = (AudioManager) getSystemService(AUDIO_SERVICE);
			int max = Math.max(audio.getStreamMaxVolume(AudioManager.STREAM_MUSIC), 1);
			long now = SystemClock.elapsedRealtime();
			JSONObject o = new JSONObject().put("channel", channelName).put("picture", picture).put("screen", screen).put("message", messageOn).put("message_mode", messageMode).put("stream_error", streamError)
					.put("call", !callRoom.isEmpty()).put("room", callRoom).put("speaking", Speech.speaking())
					.put("volume", audio.getStreamVolume(AudioManager.STREAM_MUSIC) * 100 / max)
					.put("uptime_s", now / 1000)
					.put("last_key_s_ago", lastKey == 0 ? JSONObject.NULL : (Object) ((now - lastKey) / 1000));
			publish("state", o.toString(), true);
			publish("telemetry", telemetry(now).toString(), true);
			prefs.edit().putLong("alive", System.currentTimeMillis()).apply();
		} catch (Exception e) {
			Log.w("minitv", "mqtt state: " + e);
		}
	}

	/**
	 * Retained telemetry: the timestamps stay on the broker while the TV is off. Times are ISO 8601 in UTC.
	 * boot: when the system started; online_since: when this app run connected; previous_shutdown: how and when the
	 * run before ended; watching_since: since when the current channel or address plays.
	 */
	private JSONObject telemetry(long uptimeMs) throws JSONException {
		long now = System.currentTimeMillis();
		JSONObject o = new JSONObject().put("time", iso(now)).put("boot", iso(now - uptimeMs)).put("uptime_s", uptimeMs / 1000)
				.put("online_since", iso(since * 1000)).put("channel", channelName).put("url", currentUrl)
				.put("watching_since", iso(watchingSince)).put("watching_s", (now - watchingSince) / 1000)
				.put("previous_shutdown", previousShutdown == null ? JSONObject.NULL : previousShutdown)
				.put("last_error", lastError == null ? JSONObject.NULL : lastError)
				.put("last_stream_error", lastStreamError == null ? JSONObject.NULL : lastStreamError)
				.put("speech_cache", Speech.cacheStats());
		JSONObject stream = lastStreamError;
		if (!lastStreamErrorSaved && stream != null) {
			lastStreamErrorSaved = true;
			prefs.edit().putString("last_stream_error", stream.toString()).apply();
		}
		if (lastKey != 0) o.put("last_key", iso(now - (uptimeMs - lastKey)));
		return o;
	}

	private void onMessage(Mqtt3Publish message) {
		String topic = message.getTopic().toString();
		String payload = new String(message.getPayloadAsBytes(), StandardCharsets.UTF_8).trim();
		String command = topic.substring(cfg.topic.length() + 1);
		// The empty message that clears a retained command comes back to us as well.
		if (payload.isEmpty() && clearing.remove(topic)) return;
		// A command that was left retained (sent while the TV was off, say) runs once: the device clears it on the
		// broker first, and does nothing if that fails, so that it can never run again at the next connect.
		// (Not on this thread: it also delivers the broker's confirmation that clearRetained waits for.)
		if (message.isRetain()) {
			worker.execute(() -> {
				if (clearRetained(topic)) run(command, payload);
			});
		} else {
			run(command, payload);
		}
	}

	private void run(String command, String payload) {
		try {
			switch (command) {
			case "set/channel":
				if (payload.matches("\\d{1,3}")) send(new Intent().putExtra("channel", Integer.parseInt(payload)));
				else send(new Intent().putExtra("channel_name", payload));
				break;
			case "set/volume":
				setVolume(Integer.parseInt(payload));
				break;
			case "cmd/play":
				if (cfg.url && payload.matches("[A-Za-z][A-Za-z0-9+.-]*://\\S+")) send(new Intent().putExtra("url", payload));
				else result(command, false, cfg.url ? "not an address" : "disabled");
				break;
			case "cmd/jitsi":
				jitsi(payload);
				break;
			case "cmd/call":
				call(payload);
				break;
			case "cmd/message":
				message(payload);
				break;
			case "set/footer":
			case "set/footer_seconds":
			case "set/header_seconds":
			case "set/header_mode":
			case "set/schedule":
			case "set/header_number":
			case "set/header":
			case "set/clock":
			case "set/guide":
			case "set/device_name":
			case "set/jitsi_room":
			case "set/jitsi_max_minutes":
			case "set/speech":
			case "set/speech_voice":
			case "set/speech_rate":
			case "set/speech_volume":
			case "set/speech_boost_db":
			case "set/speech_duck_percent":
			case "set/speech_cache_mb":
			case "set/speech_repeat":
			case "set/speech_pause":
			case "set/speak_channel":
			case "set/no_picture_seconds":
			case "set/message_mode":
			case "set/message_seconds": {
				String problem = Settings.set(command.substring(4), payload);
				result(command, problem == null, problem == null ? "ok" : problem);
				if (problem == null) {
					publishConfig();
					// The device name is also the name of the Home Assistant device.
					if (command.equals("set/device_name")) publishDiscovery();
				}
				break;
			}
			case "set/config":
				setConfig(payload);
				break;
			case "cmd/say":
				say(payload);
				break;
			case "cmd/say-prefetch":
				sayPrefetch(payload);
				break;
			case "cmd/say-stop":
				Speech.stop();
				result(command, true, "stopped");
				break;
			case "set/playlist":
				if (cfg.url) setPlaylist(payload);
				else result(command, false, "disabled");
				break;
			case "cmd/screen":
				// "off" powers the display down where a root service does it (Echo Show: echoscreen), "on" back up.
				if (payload.trim().equals("off") || payload.trim().equals("on")) {
					try {
						Screen.setOff(payload.trim().equals("off"));
						result(command, true, "screen " + payload.trim());
					} catch (java.io.IOException e) {
						result(command, false, e.getMessage());
					}
				} else {
					result(command, false, "payload must be off or on");
				}
				break;
			case "cmd/reload-playlist":
				send(new Intent().putExtra("reload", true));
				break;
			case "cmd/shell":
				if (cfg.shell) shell(payload);
				else result(command, false, "disabled");
				break;
			default:
				break;
			}
		} catch (RuntimeException e) {
			result(command, false, String.valueOf(e));
		}
	}

	/** Replaces the retained message of the topic with an empty one; false if the broker did not confirm in time. */
	private boolean clearRetained(String topic) {
		clearing.add(topic);
		try {
			client.publishWith().topic(topic).payload(new byte[0]).qos(MqttQos.AT_LEAST_ONCE).retain(true).send()
					.get(2, TimeUnit.SECONDS);
			return true;
		} catch (Exception e) {
			clearing.remove(topic);
			Log.w("minitv", "mqtt: retained command not cleared, not run: " + e);
			return false;
		}
	}

	/** The playlist file as text (<base>/playlist) and the visible channels as a list (<base>/channels), both retained. */
	private void publishPlaylist() {
		try {
			File file = Playlist.file();
			StringBuilder text = new StringBuilder();
			if (file.isFile()) {
				try (InputStream in = new FileInputStream(file)) {
					byte[] buffer = new byte[4096];
					for (int n; (n = in.read(buffer)) > 0;) text.append(new String(buffer, 0, n, StandardCharsets.ISO_8859_1));
				}
			}
			// Bytes were read as Latin-1 chunks only to keep them intact: decode the whole as UTF-8.
			publish("playlist", new String(text.toString().getBytes(StandardCharsets.ISO_8859_1), StandardCharsets.UTF_8), true);
			Playlist playlist = Playlist.load();
			JSONArray channels = new JSONArray();
			for (int i = 0; i < playlist.names.size(); i++) {
				channels.put(new JSONObject().put("number", i).put("name", playlist.names.get(i))
						.put("url", playlist.urls.get(i)).put("zapp_id", playlist.zappIds.get(i) == null
								? JSONObject.NULL : playlist.zappIds.get(i)));
			}
			publish("channels", channels.toString(), true);
		} catch (Exception e) {
			Log.w("minitv", "mqtt playlist: " + e);
		}
	}

	/** All settings as one retained JSON (<base>/config). Connection and security settings (mqtt.conf) are not in it. */
	private void publishConfig() {
		try {
			publish("config", Settings.toJson(Playlist.load().guide).toString(), true);
		} catch (JSONException e) {
			Log.w("minitv", "mqtt config: " + e);
		}
	}

	/** <base>/set/config: a JSON object with any of the settings; nothing is changed if one of them is wrong. */
	private void setConfig(String text) {
		try {
			JSONObject o = new JSONObject(text);
			java.util.Iterator<String> keys = o.keys();
			// Check every value first: all or nothing.
			StringBuilder problems = new StringBuilder();
			while (keys.hasNext()) {
				String key = keys.next();
				String problem = Settings.check(key, String.valueOf(o.get(key)));
				if (problem != null) problems.append(problem).append("; ");
			}
			if (problems.length() > 0) {
				result("set/config", false, problems.toString());
				return;
			}
			String nameBefore = Settings.deviceName();
			keys = o.keys();
			while (keys.hasNext()) {
				String key = keys.next();
				Settings.set(key, String.valueOf(o.get(key)));
			}
			result("set/config", true, "ok");
			publishConfig();
			if (!nameBefore.equals(Settings.deviceName())) publishDiscovery();
		} catch (JSONException e) {
			result("set/config", false, "not a JSON object");
		}
	}

	/** Replaces the playlist file with the M3U text (the old one is kept as playlist.m3u.bak) and reloads it. */
	private void setPlaylist(String text) {
		try {
			if (text.length() > 1000000) {
				result("set/playlist", false, "too large");
				return;
			}
			// Every address line must be an address ("hdmi:" for the HDMI input): nothing else is written to the file.
			for (String line : text.split("\\r?\\n")) {
				line = line.replace("\uFEFF", "").trim();
				if (!line.isEmpty() && line.charAt(0) != '#' && !line.equals(Playlist.HDMI)
						&& !line.matches("[A-Za-z][A-Za-z0-9+.-]*://\\S+")) {
					result("set/playlist", false, "not an address: " + line);
					return;
				}
			}
			Playlist parsed = Playlist.parse(new java.io.BufferedReader(new java.io.StringReader(text)));
			if (parsed.urls.isEmpty()) {
				result("set/playlist", false, "no channels in it");
				return;
			}
			File file = Playlist.file();
			File temp = new File(file.getPath() + ".tmp"), backup = new File(file.getPath() + ".bak");
			try (java.io.OutputStream out = new java.io.FileOutputStream(temp)) {
				out.write((text + "\n").getBytes(StandardCharsets.UTF_8));
			}
			if (file.isFile()) {
				backup.delete();
				file.renameTo(backup);
			}
			if (!temp.renameTo(file)) throw new java.io.IOException("could not write " + file);
			// Other tools (adb push) have to be able to replace it again.
			file.setReadable(true, false);
			file.setWritable(true, false);
			send(new Intent().putExtra("reload", true));
			result("set/playlist", true, parsed.urls.size() + " visible channels");
		} catch (Exception e) {
			result("set/playlist", false, String.valueOf(e));
		}
	}

	/** cmd/jitsi: a room name or a room address on the configured server opens the call; empty or "hangup" ends it. */
	private void jitsi(String payload) {
		if (!cfg.jitsi) {
			result("cmd/jitsi", false, "disabled", "jitsi");
			return;
		}
		if (payload.isEmpty() || payload.equalsIgnoreCase("hangup")) {
			send(new Intent().putExtra("jitsi", ""));
			result("cmd/jitsi", true, "hangup");
			return;
		}
		String url = VideoCall.roomUrl(payload, cfg.jitsiServer, cfg.jitsiAllowed);
		if (url == null) {
			result("cmd/jitsi", false, VideoCall.rejection(payload, cfg.jitsiAllowed), "jitsi");
			return;
		}
		send(new Intent().putExtra("jitsi", url));
		result("cmd/jitsi", true, url);
	}

	/**
	 * cmd/call: a contact's name for the video call app minicall, which calls it (and shows the call above the TV);
	 * empty or "hangup" ends the call. minicall only calls its own contacts, whoever asks.
	 */
	private void call(String payload) {
		String pkg = "org.minicall";
		try {
			getPackageManager().getPackageInfo(pkg, 0);
		} catch (PackageManager.NameNotFoundException e) {
			result("cmd/call", false, "minicall is not installed", "call");
			return;
		}
		boolean hangup = payload.isEmpty() || payload.equalsIgnoreCase("hangup");
		Intent i = new Intent(hangup ? pkg + ".action.HANGUP" : pkg + ".action.CALL")
				.setComponent(new ComponentName(pkg, pkg + ".CallReceiver"));
		if (!hangup) i.putExtra("to", payload);
		sendBroadcast(i);
		result("cmd/call", true, hangup ? "hangup" : payload);
	}

	/**
	 * cmd/say: plain text, or {"text":"...","voice":"...","repeat":n,"pause":s}; empty stops. Failures of the render come
	 * as error events.
	 */
	private void say(String payload) {
		String problem = speechRefused();
		if (problem != null) {
			result("cmd/say", false, problem, "speech");
			return;
		}
		String text = payload, voice = Settings.speechVoice();
		JSONObject o = new JSONObject();
		if (payload.startsWith("{")) {
			try {
				o = new JSONObject(payload);
				text = o.optString("text", "").trim();
				voice = o.optString("voice", voice).trim();
			} catch (JSONException e) {
				result("cmd/say", false, "not a JSON object", "speech");
				return;
			}
		}
		if (text.isEmpty()) {
			Speech.stop();
			result("cmd/say", true, "stopped");
			return;
		}
		if (text.length() > Speech.TEXT_MAX) problem = "text longer than " + Speech.TEXT_MAX + " characters";
		else if (!Settings.validVoice(voice)) problem = "voice: a voice like " + EdgeTts.DEFAULT_VOICE;
		else problem = MessageSpec.check(o);
		if (problem != null) {
			result("cmd/say", false, problem, "speech");
			return;
		}
		MessageSpec m = MessageSpec.of(o);
		Speech.say(text, voice, m.repeat, m.pause, null);
		result("cmd/say", true, "speaking");
	}

	/**
	 * cmd/message: a text (empty ends the active message), or {"text","mode","show_seconds","repeat","pause"} ("speak" is
	 * the older form of mode). A payload that is not a JSON object is shown as a plain text.
	 */
	private void message(String payload) {
		Intent extras = new Intent().putExtra("message", payload);
		if (payload.startsWith("{")) {
			try {
				JSONObject o = new JSONObject(payload);
				String problem = MessageSpec.check(o);
				if (problem != null) {
					result("cmd/message", false, problem);
					return;
				}
				extras = new Intent().putExtra("message_json", o.toString());
			} catch (JSONException e) {
				// Not JSON after all: a text that starts with a brace.
			}
		}
		send(extras);
	}

	/** cmd/say-prefetch: a JSON array of texts that are rendered and cached, not played. */
	private void sayPrefetch(String payload) {
		String problem = speechRefused();
		java.util.List<String> texts = new java.util.ArrayList<>();
		if (problem == null) {
			try {
				JSONArray a = new JSONArray(payload);
				if (a.length() > 50) problem = "50 texts at most";
				for (int i = 0; i < a.length() && problem == null; i++) {
					String text = a.optString(i, "").trim();
					if (text.isEmpty() || text.length() > Speech.TEXT_MAX) problem = "text " + i + ": 1 to " + Speech.TEXT_MAX + " characters";
					texts.add(text);
				}
			} catch (JSONException e) {
				problem = "not a JSON array of texts";
			}
		}
		if (problem != null) {
			result("cmd/say-prefetch", false, problem, "speech");
			return;
		}
		Speech.prefetch(texts, (rendered, cached, failed) -> result("cmd/say-prefetch", failed == 0,
				rendered + " rendered, " + cached + " cached already, " + failed + " failed", "speech"));
	}

	/** Why MQTT may not make the TV speak now, or null. */
	private String speechRefused() {
		return cfg.speech ? Speech.refusal() : "disabled";
	}

	private void send(Intent extras) {
		// MainActivity is singleTask: this arrives in its onNewIntent, and brings the TV to the front.
		Intent intent = new Intent(this, MainActivity.class).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
				.putExtra("token", VideoCall.TOKEN);
		if (extras.getExtras() != null) intent.putExtras(extras.getExtras());
		startActivity(intent);
	}

	private void setVolume(int percent) {
		AudioManager audio = (AudioManager) getSystemService(AUDIO_SERVICE);
		int max = audio.getStreamMaxVolume(AudioManager.STREAM_MUSIC);
		audio.setStreamVolume(AudioManager.STREAM_MUSIC, Math.max(0, Math.min(100, percent)) * max / 100, 0);
		scheduleState();
	}

	/** Runs the command as this app's user, 30 s at most, and publishes exit code and the first 16 KB of output. */
	private void shell(String command) {
		shells.execute(() -> {
			try {
				Process p = new ProcessBuilder("sh", "-c", command).redirectErrorStream(true).start();
				worker.schedule(p::destroyForcibly, SHELL_TIMEOUT_S, TimeUnit.SECONDS);
				ByteArrayOutputStream out = new ByteArrayOutputStream();
				try (InputStream in = p.getInputStream()) {
					byte[] buffer = new byte[4096];
					for (int n; (n = in.read(buffer)) > 0;) {
						if (out.size() < SHELL_OUTPUT_MAX) out.write(buffer, 0, Math.min(n, SHELL_OUTPUT_MAX - out.size()));
					}
				}
				int code = p.waitFor();
				publish("result", new JSONObject().put("command", command).put("exit", code)
						.put("output", out.toString("UTF-8")).toString(), false);
			} catch (Exception e) {
				result("cmd/shell", false, String.valueOf(e));
			}
		});
	}

	private void result(String command, boolean ok, String text) {
		result(command, ok, text, "command");
	}

	/** The answer on <base>/result; a failure is also an error event of the given type. */
	private void result(String command, boolean ok, String text, String errorType) {
		if (!ok) error(errorType, command + ": " + text, command);
		try {
			publish("result", new JSONObject().put("command", command).put("ok", ok).put("message", text).toString(), false);
		} catch (JSONException e) {
			Log.w("minitv", "mqtt result: " + e);
		}
	}

	// The system sends this before it powers off or reboots: tell the broker which of the two it is.
	private final BroadcastReceiver shutdown = new BroadcastReceiver() {
		@Override
		public void onReceive(Context context, Intent intent) {
			String reason = shutdownReason();
			Log.i("minitv", "mqtt: system shutdown (" + reason + ")");
			stopCleanly(reason);
		}
	};

	/** "sys.shutdown.requested" holds 1 for a reboot or 0 for a power off, then the reason the requester gave. */
	private String shutdownReason() {
		try {
			Process p = new ProcessBuilder("getprop", "sys.shutdown.requested").start();
			ByteArrayOutputStream out = new ByteArrayOutputStream();
			try (InputStream in = p.getInputStream()) {
				byte[] buffer = new byte[256];
				for (int n; (n = in.read(buffer)) > 0;) out.write(buffer, 0, n);
			}
			String value = out.toString("UTF-8").trim();
			String detail = value.length() > 1 ? " (" + value.substring(1) + ")" : "";
			return value.startsWith("1") ? "reboot" + detail : "power off" + detail;
		} catch (Exception e) {
			return "power off or reboot";
		}
	}

	private synchronized void stopCleanly(String reason) {
		if (stopped || client == null) return;
		stopped = true;
		prefs.edit().putBoolean("clean", true).putString("reason", reason).putLong("stopped_at", System.currentTimeMillis()).apply();
		Log.i("minitv", "mqtt: stopping, reason: " + reason);
		try {
			client.publishWith().topic(cfg.topic + "/status").payload(offline(reason, true).getBytes(StandardCharsets.UTF_8))
					.qos(MqttQos.AT_LEAST_ONCE).retain(true).send().get(2, TimeUnit.SECONDS);
			client.disconnect().get(2, TimeUnit.SECONDS);
		} catch (Exception e) {
			Log.w("minitv", "mqtt goodbye: " + e);
		}
	}

	@Override
	public void onDestroy() {
		stopCleanly("app stopped");
		instance = null;
		try {
			unregisterReceiver(shutdown);
		} catch (IllegalArgumentException e) {
			// Never registered: there was no usable configuration.
		}
		worker.shutdownNow();
		shells.shutdownNow();
		super.onDestroy();
	}
}
