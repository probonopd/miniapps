package org.minitv;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.Service;
import android.content.Intent;
import android.os.Build;
import android.os.IBinder;
import android.os.Process;
import android.util.Log;
import java.io.File;

/**
 * Loads the speech model in the background (the voice process) so that the first voice command is quick, and
 * keeps that process at service priority; an idle process is dropped within a minute on this unit.
 */
public class VoiceService extends Service {
	@Override
	public int onStartCommand(Intent intent, int flags, int startId) {
		new Thread(() -> {
			Process.setThreadPriority(Process.THREAD_PRIORITY_BACKGROUND);
			try {
				if (new File(android.os.Environment.getExternalStorageDirectory(), "voice-foreground").exists()) {
					keepLoaded();
				}
				VoiceActivity.preload(this);
			} catch (Exception e) {
				Log.e("minitv", "voice: preload failed", e);
			}
		}, "voice-preload").start();
		return START_NOT_STICKY;
	}

	/** For devices where voice starts by wake word: a reload would take longer than the person waits for an answer. */
	private void keepLoaded() {
		Notification.Builder builder;
		if (Build.VERSION.SDK_INT >= 26) {
			String channel = "voice";
			getSystemService(NotificationManager.class).createNotificationChannel(
					new NotificationChannel(channel, "Sprachsteuerung", NotificationManager.IMPORTANCE_MIN));
			builder = new Notification.Builder(this, channel);
		} else {
			builder = new Notification.Builder(this);
		}
		startForeground(1, builder.setSmallIcon(android.R.drawable.ic_btn_speak_now).setContentTitle("Sprachsteuerung").build());
	}

	@Override
	public IBinder onBind(Intent intent) {
		return null;
	}
}
