package org.minitv;

import android.media.AudioDeviceInfo;
import android.media.AudioManager;

/**
 * This firmware leaves the music stream at 0 dB on USB audio outputs, so the volume keys change nothing there. On
 * those, the system volume index becomes gain of the app's own players (the TV and the speech): -30 dB at the lowest
 * step, rising linearly in dB to 0 dB at the top, 0 when muted. Other outputs are scaled by the system itself.
 */
final class Volume {
	private Volume() {
	}

	/** The gain the app's players apply on top of what the system does. */
	static float gain(AudioManager audio) {
		if (!usbOutput(audio)) return 1f;
		int index = audio.getStreamVolume(AudioManager.STREAM_MUSIC);
		float percent = 100f * index / audio.getStreamMaxVolume(AudioManager.STREAM_MUSIC);
		return index == 0 || audio.isStreamMute(AudioManager.STREAM_MUSIC) || audio.isMasterMute() ? 0f
				: (float) Math.pow(10, -30 * (100 - percent) / 100 / 20);
	}

	private static boolean usbOutput(AudioManager audio) {
		for (AudioDeviceInfo d : audio.getDevices(AudioManager.GET_DEVICES_OUTPUTS)) {
			if (d.getType() == AudioDeviceInfo.TYPE_USB_DEVICE || d.getType() == AudioDeviceInfo.TYPE_USB_HEADSET) return true;
		}
		return false;
	}
}
