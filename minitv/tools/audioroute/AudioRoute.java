/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */

import android.media.AudioDevicePort;
import android.media.AudioPort;
import android.media.AudioSystem;
import android.media.IAudioService;
import android.os.ServiceManager;
import android.util.Log;

import java.io.BufferedReader;
import java.io.File;
import java.io.FileReader;
import java.io.IOException;
import java.util.ArrayList;
import java.util.List;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * Root helper, run with app_process: puts the audio policy into "output on the USB card that can play, input from the
 * USB card that can only capture (a webcam microphone)". UsbAlsaManager selects only the newest USB sound card, so
 * this state cannot be reached through the framework alone.
 *
 * Changes go through IAudioService.setWiredDeviceConnectionState (not AudioSystem directly) so that AudioService's
 * device inventory stays in line with the policy: it then replays our devices after an audioserver restart, and the
 * framework's own later connect/disconnect calls for the same devices are not rejected as inconsistent.
 *
 * Usage: app_process /system/bin AudioRoute [apply|status]. Exit status 0 = desired state reached or nothing to do.
 */
public final class AudioRoute {
	private static final String TAG = "audioroute";
	private static final Pattern CARD_LINE = Pattern.compile("^\\s*(\\d+)\\s+\\[[^\\]]*\\]:\\s+USB-Audio - (.*)$");
	private static final Pattern CARD_ADDR = Pattern.compile("card=(\\d+);device=0;?");

	private static final class Card {
		final int num;
		final String name;
		final boolean play;
		final boolean capture;

		Card(int num, String name) {
			this.num = num;
			this.name = name;
			File dir = new File("/proc/asound/card" + num);
			boolean p = false, c = false;
			String[] entries = dir.list();
			if (entries != null) {
				for (String e : entries) {
					if (e.matches("pcm\\d+p")) p = true;
					if (e.matches("pcm\\d+c")) c = true;
				}
			}
			play = p;
			capture = c;
		}

		String address() {
			return "card=" + num + ";device=0;";
		}
	}

	private static final class Dev {
		final int type;
		final int card;
		final String address;

		Dev(int type, int card, String address) {
			this.type = type;
			this.card = card;
			this.address = address;
		}

		boolean isInput() {
			return (type & AudioSystem.DEVICE_BIT_IN) != 0;
		}
	}

	private final IAudioService audio;

	private AudioRoute(IAudioService audio) {
		this.audio = audio;
	}

	public static void main(String[] args) {
		String cmd = args.length > 0 ? args[0] : "apply";
		IAudioService audio = IAudioService.Stub.asInterface(ServiceManager.getService("audio"));
		if (audio == null) {
			System.err.println("audio service not running");
			System.exit(2);
		}
		AudioRoute r = new AudioRoute(audio);
		try {
			if (cmd.equals("status")) {
				r.status();
				System.exit(0);
			} else if (cmd.equals("apply")) {
				System.exit(r.apply() ? 0 : 1);
			}
			System.err.println("usage: AudioRoute [apply|status]");
			System.exit(2);
		} catch (Exception e) {
			Log.e(TAG, "failed", e);
			e.printStackTrace();
			System.exit(1);
		}
	}

	private static boolean isUsb(int type) {
		return type == AudioSystem.DEVICE_OUT_USB_HEADSET || type == AudioSystem.DEVICE_OUT_USB_DEVICE
				|| type == AudioSystem.DEVICE_IN_USB_HEADSET || type == AudioSystem.DEVICE_IN_USB_DEVICE;
	}

	private static List<Card> usbCards() throws IOException {
		List<Card> cards = new ArrayList<>();
		try (BufferedReader in = new BufferedReader(new FileReader("/proc/asound/cards"))) {
			for (String line; (line = in.readLine()) != null;) {
				Matcher m = CARD_LINE.matcher(line);
				if (m.matches()) cards.add(new Card(Integer.parseInt(m.group(1)), m.group(2).trim()));
			}
		}
		return cards;
	}

	private static List<Dev> usbDevices() {
		ArrayList<AudioPort> ports = new ArrayList<>();
		int status = AudioSystem.listAudioPorts(ports, new int[1]);
		if (status != AudioSystem.SUCCESS) throw new IllegalStateException("listAudioPorts: " + status);
		List<Dev> devs = new ArrayList<>();
		for (AudioPort p : ports) {
			if (!(p instanceof AudioDevicePort)) continue;
			AudioDevicePort d = (AudioDevicePort) p;
			if (!isUsb(d.type())) continue;
			Matcher m = CARD_ADDR.matcher(d.address() == null ? "" : d.address());
			devs.add(new Dev(d.type(), m.find() ? Integer.parseInt(m.group(1)) : -1, d.address()));
		}
		return devs;
	}

	private static Dev find(List<Dev> devs, int card, boolean input) {
		for (Dev d : devs) {
			if (d.card == card && d.isInput() == input) return d;
		}
		return null;
	}

	private static Card findCard(List<Card> cards, int num) {
		for (Card c : cards) {
			if (c.num == num) return c;
		}
		return null;
	}

	private void set(int type, boolean connect, String address, String name) throws Exception {
		Log.i(TAG, (connect ? "connect " : "disconnect ") + "0x" + Integer.toHexString(type) + " " + address);
		audio.setWiredDeviceConnectionState(type, connect ? 1 : 0,
				address, name, TAG);
	}

	/** AudioService applies connection changes asynchronously; wait until the policy shows the result. */
	private static boolean waitFor(int card, boolean input, boolean present) throws InterruptedException {
		for (int i = 0; i < 30; i++) {
			if ((find(usbDevices(), card, input) != null) == present) return true;
			Thread.sleep(100);
		}
		return false;
	}

	private void status() throws IOException {
		for (Card c : usbCards()) {
			System.out.println("card " + c.num + " play=" + c.play + " capture=" + c.capture + " " + c.name);
		}
		for (Dev d : usbDevices()) {
			System.out.println("device 0x" + Integer.toHexString(d.type) + " " + d.address);
		}
	}

	private boolean apply() throws Exception {
		List<Card> cards = usbCards();

		// Devices of cards that are gone: the framework only disconnects the one card it selected, so a card we
		// connected ourselves would stay in the policy and route the sound to nowhere.
		boolean inputCleared = false;
		for (Dev d : usbDevices()) {
			if (d.card >= 0 && findCard(cards, d.card) == null) {
				set(d.type, false, d.address, "");
				inputCleared |= d.isInput();
			}
		}

		Card out = null, mic = null;
		int outs = 0, mics = 0;
		for (Card c : cards) {
			if (c.play) {
				out = c;
				outs++;
			} else if (c.capture) {
				mic = c;
				mics++;
			}
		}
		// More than one candidate is not the setup this is made for; stock behavior is the safe choice.
		if (outs != 1 || mics > 1) return true;

		List<Dev> devs = usbDevices();
		Dev outDev = find(devs, out.num, false);
		if (mic == null) {
			Dev outIn = find(devs, out.num, true);
			// Removing a stale input cleared the formats of the shared input mix port (see below): refresh.
			if (outIn != null && inputCleared) {
				set(outIn.type, false, outIn.address, out.name);
				waitFor(out.num, true, false);
				outIn = null;
			}
			// Bring back the headset microphone we took away while a camera microphone was present.
			if (outDev != null && out.capture && outIn == null) {
				int inType = outDev.type == AudioSystem.DEVICE_OUT_USB_HEADSET
						? AudioSystem.DEVICE_IN_USB_HEADSET : AudioSystem.DEVICE_IN_USB_DEVICE;
				set(inType, true, out.address(), out.name);
				return waitFor(out.num, true, true);
			}
			return true;
		}

		boolean ok = true;
		if (outDev == null) {
			// Same type choice as UsbAlsaDevice: a card that also captures is a headset.
			set(out.capture ? AudioSystem.DEVICE_OUT_USB_HEADSET : AudioSystem.DEVICE_OUT_USB_DEVICE, true,
					out.address(), out.name);
			ok = waitFor(out.num, false, true);
		}
		Dev outIn = find(devs, out.num, true);
		Dev micIn = find(devs, mic.num, true);
		if (outIn == null && micIn != null && !inputCleared) return ok;
		// Both USB inputs share the one "usb_device input" mix port, whose formats the policy reads from the HAL when
		// an input connects and clears when any input of it disconnects. So the headset input goes first and the
		// camera input is (re)connected after it, or the camera input is left without formats and every capture fails
		// (getInputForDevice could not find profile, createRecord error -38).
		if (outIn != null) {
			set(outIn.type, false, outIn.address, out.name);
			if (!waitFor(out.num, true, false)) return false;
		}
		if (micIn != null) {
			set(micIn.type, false, micIn.address, mic.name);
			waitFor(mic.num, true, false);
		}
		set(AudioSystem.DEVICE_IN_USB_DEVICE, true, mic.address(), mic.name);
		if (waitFor(mic.num, true, true)) return ok;
		// Never leave the user without the microphone the framework had chosen.
		if (outIn != null) {
			set(outIn.type, true, outIn.address, out.name);
			waitFor(out.num, true, true);
		}
		return false;
	}
}
