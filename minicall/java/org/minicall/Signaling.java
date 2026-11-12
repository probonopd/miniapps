/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
package org.minicall;

import android.util.Log;
import com.hivemq.client.mqtt.MqttClient;
import com.hivemq.client.mqtt.MqttClientSslConfig;
import com.hivemq.client.mqtt.MqttGlobalPublishFilter;
import com.hivemq.client.mqtt.datatypes.MqttQos;
import com.hivemq.client.mqtt.mqtt3.Mqtt3AsyncClient;
import com.hivemq.client.mqtt.mqtt3.Mqtt3ClientBuilder;
import com.hivemq.client.mqtt.mqtt3.message.publish.Mqtt3Publish;
import java.nio.charset.StandardCharsets;
import java.security.KeyStore;
import java.util.UUID;
import javax.net.ssl.TrustManagerFactory;
import org.json.JSONException;
import org.json.JSONObject;

/**
 * Calls from the web page (web/call.html), set up over an MQTT broker instead of a SIP account. Below the
 * configured topic T:
 *
 *   T/offer         caller -> device  {"id","name","type":"offer","sdp"} (all candidates included); another
 *                   device adds "from", its own topic
 *   T/answer/ID     device -> caller  {"type":"answer","sdp"} or {"error"}
 *   T/bye/ID        either way        {} the call with this id ends
 *   T/ring          device -> page    {"from","to"} asks the page named "to" to call
 *   T/status        device, retained  {"online","name"}; the broker's last will says offline
 *
 * A device calls another one by publishing its offer below that device's topic. Whoever may publish to T/offer
 * can call; the broker's access rules for T are what protects the camera.
 */
final class Signaling {
	private static final String TAG = "minicall";

	interface Listener {
		/** On an MQTT thread. from is the calling device's topic, or null for a web page. */
		void onOffer(String id, String name, String from, JSONObject offer);

		/** The answer of a device this one called. */
		void onAnswer(String id, JSONObject answer);

		void onBye(String id);
	}

	private final Settings s;
	// This device's screen shape ("1280x576"), sent along so that the other device sends exactly that shape.
	private String screen = "";
	private final Mqtt3AsyncClient client;
	private final Listener listener;

	Signaling(Settings s, Listener listener) throws Exception {
		this.s = s;
		this.listener = listener;
		Mqtt3ClientBuilder b = MqttClient.builder().useMqttVersion3().identifier("minicall-" + UUID.randomUUID())
				.serverHost(s.mqttHost).serverPort(s.mqttPort);
		if (s.mqttTls) {
			TrustManagerFactory tmf = TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm());
			tmf.init((KeyStore) null);
			// Server certificate and host name are always verified.
			b = b.sslConfig(MqttClientSslConfig.builder().trustManagerFactory(tmf).build());
		}
		// Login and last will on the client, not on the first connect only: the automatic reconnect (after a change
		// from WLAN to mobile data, say) used none, and the broker refused it.
		if (!s.mqttUser.isEmpty()) {
			b = b.simpleAuth().username(s.mqttUser).password(s.mqttPassword.getBytes(StandardCharsets.UTF_8))
					.applySimpleAuth();
		}
		b = b.willPublish().topic(s.mqttTopic + "/status").payload(status(false)).qos(MqttQos.AT_LEAST_ONCE)
				.retain(true).applyWillPublish();
		client = b.automaticReconnectWithDefaultConfig().addConnectedListener(c -> onConnected())
				.addDisconnectedListener(c -> Log.w(TAG, "mqtt disconnected: " + c.getCause().getMessage()))
				.buildAsync();
		client.publishes(MqttGlobalPublishFilter.SUBSCRIBED, this::onMessage);
		client.connectWith().keepAlive(60).cleanSession(true).send();
	}

	private byte[] status(boolean online) {
		try {
			return new JSONObject().put("online", online).put("name", s.displayName).toString()
					.getBytes(StandardCharsets.UTF_8);
		} catch (JSONException e) {
			throw new IllegalStateException(e);
		}
	}

	private void onConnected() {
		Log.i(TAG, "mqtt connected to " + s.mqttHost);
		client.publishWith().topic(s.mqttTopic + "/status").payload(status(true)).qos(MqttQos.AT_LEAST_ONCE)
				.retain(true).send();
		// A clean session: the broker forgets the subscriptions with every reconnect.
		client.subscribeWith().topicFilter(s.mqttTopic + "/offer").qos(MqttQos.AT_LEAST_ONCE).send();
		client.subscribeWith().topicFilter(s.mqttTopic + "/bye/+").qos(MqttQos.AT_LEAST_ONCE).send();
	}

	/**
	 * Every message of every subscription, through one handler registered once: a callback per subscription would
	 * be added again with each reconnect, and each message would then arrive several times.
	 */
	private void onMessage(Mqtt3Publish p) {
		String t = p.getTopic().toString();
		if (t.equals(s.mqttTopic + "/offer")) {
			onOffer(p);
		} else if (t.contains("/answer/")) {
			try {
				listener.onAnswer(t.substring(t.lastIndexOf('/') + 1),
						new JSONObject(new String(p.getPayloadAsBytes(), StandardCharsets.UTF_8)));
			} catch (JSONException e) {
				Log.w(TAG, "mqtt: bad answer: " + e.getMessage());
			}
		} else if (t.contains("/bye/")) {
			listener.onBye(t.substring(t.lastIndexOf('/') + 1));
		}
	}

	private void onOffer(Mqtt3Publish p) {
		try {
			JSONObject o = new JSONObject(new String(p.getPayloadAsBytes(), StandardCharsets.UTF_8));
			String id = o.getString("id");
			// The id becomes part of a topic: nothing that could reach other topics.
			if (!id.matches("[A-Za-z0-9_-]{1,64}")) throw new JSONException("bad id");
			String from = o.optString("from", "");
			if (!from.isEmpty() && !from.matches("[A-Za-z0-9_./-]{1,200}")) throw new JSONException("bad from");
			listener.onOffer(id, o.optString("name", "Web"), from.isEmpty() ? null : from, o);
		} catch (JSONException e) {
			Log.w(TAG, "mqtt: bad offer: " + e.getMessage());
		}
	}


	void setScreen(int w, int h) {
		screen = w + "x" + h;
	}

	/** "WxH" from an offer or answer of another device, or null (a web page sends none). */
	static int[] screenOf(JSONObject o) {
		String[] p = o.optString("screen", "").split("x");
		try {
			int w = Integer.parseInt(p[0]), h = Integer.parseInt(p[1]);
			return w > 0 && h > 0 && w <= 8192 && h <= 8192 ? new int[] {w, h} : null;
		} catch (NumberFormatException | ArrayIndexOutOfBoundsException e) {
			return null;
		}
	}

	void answer(String id, String sdp) {
		try {
			publish("answer/" + id, new JSONObject().put("type", "answer").put("sdp", sdp).put("screen", screen));
		} catch (JSONException e) {
			throw new IllegalStateException(e);
		}
	}

	void refuse(String id, String why) {
		try {
			publish("answer/" + id, new JSONObject().put("error", why));
		} catch (JSONException e) {
			throw new IllegalStateException(e);
		}
	}

	/** Ends a call this device answered. */
	void bye(String id) {
		publish("bye/" + id, new JSONObject());
	}

	/** Ends a call this device made to the device at topic. */
	void byeTo(String topic, String id) {
		client.publishWith().topic(topic + "/bye/" + id).payload("{}".getBytes(StandardCharsets.UTF_8))
				.qos(MqttQos.AT_LEAST_ONCE).send();
	}

	/** Calls the device at topic: its answer and its hangup come back to the listener under this id. */
	void offerTo(String topic, String id, String sdp, boolean babyphone) {
		client.subscribeWith().topicFilter(topic + "/answer/" + id).qos(MqttQos.AT_LEAST_ONCE).send();
		client.subscribeWith().topicFilter(topic + "/bye/" + id).qos(MqttQos.AT_LEAST_ONCE).send().thenRun(() -> {
			try {
				JSONObject o = new JSONObject().put("id", id).put("name", s.displayName).put("from", s.mqttTopic)
						.put("type", "offer").put("sdp", sdp).put("screen", screen);
				if (babyphone) o.put("babyphone", true);
				client.publishWith().topic(topic + "/offer").payload(o.toString().getBytes(StandardCharsets.UTF_8))
						.qos(MqttQos.AT_LEAST_ONCE).send();
			} catch (JSONException e) {
				throw new IllegalStateException(e);
			}
		});
	}

	/** Stops listening for a call made with offerTo. */
	void forget(String topic, String id) {
		client.unsubscribeWith().topicFilter(topic + "/answer/" + id).send();
		client.unsubscribeWith().topicFilter(topic + "/bye/" + id).send();
	}

	/** Asks the page of this web contact to call. */
	void ring(String to) {
		try {
			publish("ring", new JSONObject().put("from", s.displayName).put("to", to));
		} catch (JSONException e) {
			throw new IllegalStateException(e);
		}
	}

	private void publish(String suffix, JSONObject o) {
		client.publishWith().topic(s.mqttTopic + "/" + suffix).payload(o.toString().getBytes(StandardCharsets.UTF_8))
				.qos(MqttQos.AT_LEAST_ONCE).send();
	}

	void close() {
		client.publishWith().topic(s.mqttTopic + "/status").payload(status(false)).qos(MqttQos.AT_LEAST_ONCE)
				.retain(true).send().whenComplete((r, e) -> client.disconnect());
	}
}
