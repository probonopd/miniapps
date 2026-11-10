package org.minitv;

import android.content.Context;
import android.os.Environment;
import android.util.Log;
import java.io.BufferedReader;
import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.net.InetAddress;

/**
 * The optional MQTT settings: /sdcard/mqtt.conf, lines of key=value. At the first read the file is moved into
 * the app's private storage and deleted from the card, because other apps can read the card and the file holds
 * the broker password.
 */
final class MqttConfig {
	String host;
	int port = 8883;
	boolean tls = true;
	String user = "";
	String password = "";
	// All topics of this device are below this one: <topic>/status, <topic>/state, <topic>/set/..., <topic>/cmd/...
	String topic = "minitv";
	String clientId;
	// A PEM file with the CA certificate of the broker, for brokers whose certificate is not from a public CA.
	String caFile;
	// Playing any address and running shell commands are separate switches; shell commands are off unless asked for.
	boolean url = true;
	boolean shell = false;
	// Home Assistant MQTT discovery: the TV announces its entities under <prefix>/<component>/..., so that it
	// shows up without any configuration there.
	boolean discovery = true;
	String discoveryPrefix = "homeassistant";
	// The name of the device in Home Assistant, also the name the TV has in video calls.
	String name = "TV";
	// Video calls (cmd/jitsi): a room name is opened on jitsi-server, an address on any server jitsi-allowed lets through.
	boolean jitsi = true;
	String jitsiServer = VideoCall.DEFAULT_SERVER;
	// Which servers an address in cmd/jitsi may name: "*" for any host name, or a comma separated list.
	String jitsiAllowed = "*";
	// Whether MQTT may make the TV speak (cmd/say, cmd/say-prefetch) and Home Assistant gets the speech entities.
	boolean speech = true;

	/** The settings, or null when there is no mqtt.conf or it is not usable (then there is no MQTT at all). */
	static MqttConfig load(Context context) {
		File priv = new File(context.getFilesDir(), "mqtt.conf");
		File pub = new File(Environment.getExternalStorageDirectory(), "mqtt.conf");
		if (pub.isFile()) {
			try (InputStream in = new FileInputStream(pub); OutputStream out = new FileOutputStream(priv)) {
				byte[] buffer = new byte[4096];
				for (int n; (n = in.read(buffer)) > 0;) out.write(buffer, 0, n);
			} catch (IOException e) {
				Log.w("minitv", "mqtt.conf not moved: " + e);
				return null;
			}
			priv.setReadable(false, false);
			priv.setReadable(true, true);
			if (!pub.delete()) Log.w("minitv", "mqtt.conf could not be deleted from the card");
		}
		if (!priv.isFile()) return null;
		MqttConfig c = new MqttConfig();
		try (BufferedReader r = new BufferedReader(new InputStreamReader(new FileInputStream(priv), "UTF-8"))) {
			String line;
			while ((line = r.readLine()) != null) {
				line = line.trim();
				int eq = line.indexOf('=');
				if (line.isEmpty() || line.startsWith("#") || eq < 1) continue;
				String key = line.substring(0, eq).trim().toLowerCase(), value = line.substring(eq + 1).trim();
				switch (key) {
				case "host": c.host = value; break;
				case "port": c.port = Integer.parseInt(value); break;
				case "tls": c.tls = !value.equalsIgnoreCase("off"); break;
				case "user": c.user = value; break;
				case "password": c.password = value; break;
				case "topic": c.topic = value.replaceAll("^/+|/+$", ""); break;
				case "client-id": c.clientId = value; break;
				case "cafile": c.caFile = value; break;
				case "url": c.url = !value.equalsIgnoreCase("off"); break;
				case "shell": c.shell = value.equalsIgnoreCase("on"); break;
				case "discovery": c.discovery = !value.equalsIgnoreCase("off"); break;
				case "discovery-prefix": c.discoveryPrefix = value.replaceAll("^/+|/+$", ""); break;
				case "name": c.name = value; break;
				case "jitsi": c.jitsi = !value.equalsIgnoreCase("off"); break;
				case "jitsi-allowed": c.jitsiAllowed = value; break;
				case "speech": c.speech = !value.equalsIgnoreCase("off"); break;
				case "jitsi-server": c.jitsiServer = value.toLowerCase(java.util.Locale.ROOT); break;
				default: Log.w("minitv", "mqtt.conf: unknown key " + key);
				}
			}
		} catch (IOException | NumberFormatException e) {
			Log.w("minitv", "mqtt.conf unreadable: " + e);
			return null;
		}
		if (c.host == null || c.host.isEmpty() || c.topic.isEmpty()) return null;
		// Over the internet only with TLS: plain MQTT is accepted for a broker in the local network only.
		if (!c.tls && !isLocal(c.host)) {
			Log.w("minitv", "mqtt.conf: tls=off is only allowed for a broker in the local network");
			return null;
		}
		// A server that is not a plain host name could widen what counts as the allowed host: no calls then.
		if (!c.jitsiServer.matches("[a-z0-9-]+(\\.[a-z0-9-]+)+")) {
			Log.w("minitv", "mqtt.conf: jitsi-server is not a host name, video calls are off");
			c.jitsi = false;
		}
		if (c.clientId == null) c.clientId = "minitv-" + c.topic.replaceAll("[^A-Za-z0-9]", "-");
		return c;
	}

	/** The settings, or the defaults when there is no usable mqtt.conf (video calls then still go to the default server). */
	static MqttConfig loadOrDefaults(Context context) {
		MqttConfig c = load(context);
		return c != null ? c : new MqttConfig();
	}

	private static boolean isLocal(String host) {
		try {
			InetAddress a = InetAddress.getByName(host);
			return a.isLoopbackAddress() || a.isSiteLocalAddress() || a.isLinkLocalAddress();
		} catch (IOException e) {
			return false;
		}
	}
}
