/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
package org.minicall;

import android.media.MediaCodec;
import android.media.MediaFormat;
import android.graphics.SurfaceTexture;
import android.net.LocalSocket;
import android.util.Log;
import android.view.Surface;
import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.nio.ByteBuffer;
import java.util.function.IntSupplier;

/**
 * Camera and screen for the helper's sockvideo module, coded in the device's hardware: MediaCodec H.264 encodes
 * the camera, and decodes the far end straight onto the call screen's surface. Software coding in the helper
 * cost a whole core of the Echo Show for 640x360.
 */
final class VideoBridge {
	private static final String TAG = "minicall";
	private static final String AVC = MediaFormat.MIMETYPE_VIDEO_AVC;

	/** The call screen's views, which may come and go during a call. */
	interface Display {
		/** On a codec thread: the picture size of the far end, for the layout. */
		void onRemoteSize(int width, int height);

		/** The size of the own picture as sent, for the self view's shape. */
		void onSelfSize(int width, int height);
	}

	final String cameraName;
	final String displayName;
	private final SocketServer camera;
	private final SocketServer display;
	private final Object lock = new Object();
	private Surface remoteSurface;
	private Display listener;
	private volatile boolean decoderReset;
	// Counters for the log line every few seconds.
	private volatile int decFrames;
	private CameraEncoder activeCamera;
	private SurfaceTexture selfTexture;
	private int selfW, selfH;
	// The shape the far end's screen has (device calls), or null: the camera picture is cropped to it.
	private volatile int[] targetShape;
	// The screen's rotation in degrees: the camera picture is turned to match it.
	private final IntSupplier displayDegrees;

	VideoBridge(String prefix, IntSupplier displayDegrees) throws IOException {
		this.displayDegrees = displayDegrees;
		cameraName = prefix + ".cam";
		displayName = prefix + ".disp";
		camera = new SocketServer(cameraName, this::runCamera);
		display = new SocketServer(displayName, this::runDisplay);
	}

	void close() {
		camera.close();
		display.close();
	}

	/** The call screen's surface for the far end, or null when it is gone. */
	void setRemoteSurface(Surface s, Display l) {
		synchronized (lock) {
			remoteSurface = s;
			listener = l;
		}
		decoderReset = true;
	}

	/** The far end's screen shape for the next camera start, or null to send the own shape. */
	void setTargetShape(int[] shape) {
		targetShape = shape;
	}

	/** The self view's texture and size, or null; only devices that show the own picture set one. */
	void setSelfTexture(SurfaceTexture t, int w, int h) {
		CameraEncoder cam;
		synchronized (lock) {
			selfTexture = t;
			selfW = w;
			selfH = h;
			cam = activeCamera;
		}
		if (cam != null) cam.setPreview(t, w, h);
	}

	/** The size of the own picture of the call in progress, or null. */
	int[] selfSize() {
		synchronized (lock) {
			return activeCamera != null ? new int[] {activeCamera.width, activeCamera.height} : null;
		}
	}

	// --- camera -> encoder -> helper

	private void runCamera(LocalSocket s) throws IOException {
		String[] p = SocketServer.readLine(s.getInputStream()).split(" ");
		if (p.length != 4) throw new IOException("bad camera header");
		int wantW = Integer.parseInt(p[0]), wantH = Integer.parseInt(p[1]);
		int fps = Math.max(5, Integer.parseInt(p[2])), bitrate = Integer.parseInt(p[3]);
		int[] shape = targetShape;
		if (shape != null) {
			// The same number of pixels in the far end's shape, in blocks of 16.
			double aspect = (double) shape[0] / shape[1], pixels = (double) wantW * wantH;
			wantW = (int) Math.round(Math.sqrt(pixels * aspect) / 16) * 16;
			wantH = (int) Math.round(Math.sqrt(pixels / aspect) / 16) * 16;
		}
		CameraEncoder cam = new CameraEncoder(wantW, wantH, shape != null, fps, bitrate,
				CameraEncoder.rotation(displayDegrees.getAsInt()));
		Display l;
		synchronized (lock) {
			activeCamera = cam;
			l = listener;
			if (selfTexture != null) cam.setPreview(selfTexture, selfW, selfH);
		}
		if (l != null) l.onSelfSize(cam.width, cam.height);
		MediaCodec enc = cam.codec();
		InputStream in = s.getInputStream();
		OutputStream out = s.getOutputStream();
		RunFlag running = new RunFlag();
		// Keyframe requests from the helper ('k': the far end lost the picture).
		Thread control = new Thread(() -> {
			try {
				for (int c; (c = in.read()) >= 0; ) {
					if (c == 'k') cam.requestKeyframe();
				}
			} catch (IOException | IllegalStateException ignored) {
				// The call ended.
			}
			running.clear();
		}, "camera-control");
		control.start();

		MediaCodec.BufferInfo info = new MediaCodec.BufferInfo();
		byte[] config = new byte[0];
		int sent = 0;
		long bytes = 0, since = System.nanoTime();
		try {
			while (running.get()) {
				int idx = enc.dequeueOutputBuffer(info, 100000);
				if (idx < 0) continue;
				ByteBuffer bb = enc.getOutputBuffer(idx);
				byte[] data = new byte[info.size];
				if (bb != null) {
					bb.position(info.offset);
					bb.get(data, 0, info.size);
				}
				enc.releaseOutputBuffer(idx, false);
				if ((info.flags & MediaCodec.BUFFER_FLAG_CODEC_CONFIG) != 0) {
					config = data;
					continue;
				}
				boolean key = (info.flags & MediaCodec.BUFFER_FLAG_KEY_FRAME) != 0;
				bytes += data.length;
				if (++sent % 150 == 1) {
					long now = System.nanoTime();
					Log.i(TAG, "video out: camera " + cam.frames + " frames, encoded " + sent + ", "
							+ bytes * 8000 / Math.max(1, (now - since) / 1000000) / 1000 + " kbit/s");
					bytes = 0;
					since = now;
				}
				// SPS and PPS with every keyframe, so a receiver can start at any keyframe.
				if (key) data = concat(config, data);
				writeFrame(out, data, key, info.presentationTimeUs);
			}
		} catch (IOException ignored) {
			// The helper closed the camera: the call ended.
		} finally {
			running.clear();
			synchronized (lock) {
				activeCamera = null;
			}
			cam.close();
		}
	}

	// --- helper -> decoder -> screen

	private void runDisplay(LocalSocket s) throws IOException {
		InputStream in = s.getInputStream();
		OutputStream out = s.getOutputStream();
		byte[] hdr = new byte[13];
		byte[] data = new byte[0];
		MediaCodec dec = null;
		Drain drain = null;
		Surface on = null;
		boolean waitKey = true;
		try {
			while (true) {
				SocketServer.readFully(in, hdr, 13);
				int len = (hdr[0] & 0xff) << 24 | (hdr[1] & 0xff) << 16 | (hdr[2] & 0xff) << 8 | (hdr[3] & 0xff);
				boolean key = (hdr[4] & 1) != 0;
				long ts = 0;
				for (int i = 5; i < 13; i++) {
					ts = ts << 8 | (hdr[i] & 0xff);
				}
				if (len <= 0 || len > 4 << 20) throw new IOException("bad frame length " + len);
				if (data.length < len) data = new byte[len];
				SocketServer.readFully(in, data, len);

				Surface target;
				Display l;
				synchronized (lock) {
					target = remoteSurface;
					l = listener;
				}
				if (decoderReset || target != on) {
					decoderReset = false;
					if (dec != null) {
						drain.finish();
						dec.stop();
						dec.release();
						dec = null;
					}
					on = target;
					waitKey = true;
				}
				if (on == null) continue;
				if (waitKey && !key) {
					out.write('k');
					continue;
				}
				if (dec == null) {
					dec = MediaCodec.createDecoderByType(AVC);
					MediaFormat f = MediaFormat.createVideoFormat(AVC, 640, 480);
					f.setInteger(MediaFormat.KEY_MAX_WIDTH, 1920);
					f.setInteger(MediaFormat.KEY_MAX_HEIGHT, 1088);
					dec.configure(f, on, null, 0);
					dec.start();
					drain = new Drain(dec, l);
					drain.start();
				}
				waitKey = false;
				int idx = dec.dequeueInputBuffer(200000);
				ByteBuffer bb = idx >= 0 ? dec.getInputBuffer(idx) : null;
				if (bb == null || bb.capacity() < len) {
					// A frame lost here breaks the ones that refer to it: start again at a keyframe.
					if (idx >= 0) dec.queueInputBuffer(idx, 0, 0, ts, 0);
					waitKey = true;
					out.write('k');
					continue;
				}
				bb.clear();
				bb.put(data, 0, len);
				dec.queueInputBuffer(idx, 0, len, ts, 0);
			}
		} catch (MediaCodec.CodecException e) {
			Log.w(TAG, "decoder: " + e.getDiagnosticInfo());
		} finally {
			if (dec != null) {
				drain.finish();
				dec.stop();
				dec.release();
			}
		}
	}

	/**
	 * Takes decoded pictures to the screen as soon as they are ready. In the feeding loop this waited for input
	 * buffers first, and the Echo Show showed 17 of 30 frames a second.
	 */
	private final class Drain extends Thread {
		private final MediaCodec dec;
		private final Display listener;
		private volatile boolean running = true;

		Drain(MediaCodec dec, Display listener) {
			super("decoder-out");
			this.dec = dec;
			this.listener = listener;
		}

		@Override
		public void run() {
			MediaCodec.BufferInfo info = new MediaCodec.BufferInfo();
			try {
				while (running) {
					int o = dec.dequeueOutputBuffer(info, 20000);
					if (o >= 0) {
						// Now: the RTP time of the picture is no screen time, and the display held pictures back
						// for it, which put the video seconds behind the sound.
						dec.releaseOutputBuffer(o, System.nanoTime());
						if (++decFrames % 150 == 1) Log.i(TAG, "video in: decoded " + decFrames);
					} else if (o == MediaCodec.INFO_OUTPUT_FORMAT_CHANGED && listener != null) {
						MediaFormat of = dec.getOutputFormat();
						int w = of.getInteger(MediaFormat.KEY_WIDTH), h = of.getInteger(MediaFormat.KEY_HEIGHT);
						if (of.containsKey("crop-right") && of.containsKey("crop-left")) {
							w = of.getInteger("crop-right") - of.getInteger("crop-left") + 1;
							h = of.getInteger("crop-bottom") - of.getInteger("crop-top") + 1;
						}
						listener.onRemoteSize(w, h);
					}
				}
			} catch (IllegalStateException e) {
				// The decoder was stopped under us: the call or the screen went away.
			}
		}

		void finish() {
			running = false;
			try {
				join(500);
			} catch (InterruptedException e) {
				Thread.currentThread().interrupt();
			}
		}
	}

	private static void writeFrame(OutputStream out, byte[] data, boolean key, long ts) throws IOException {
		ByteArrayOutputStream b = new ByteArrayOutputStream(13 + data.length);
		int len = data.length;
		b.write(len >>> 24);
		b.write(len >>> 16);
		b.write(len >>> 8);
		b.write(len);
		b.write(key ? 1 : 0);
		for (int i = 56; i >= 0; i -= 8) {
			b.write((int) (ts >>> i));
		}
		b.write(data, 0, len);
		out.write(b.toByteArray());
	}

	private static byte[] concat(byte[] a, byte[] b) {
		byte[] r = new byte[a.length + b.length];
		System.arraycopy(a, 0, r, 0, a.length);
		System.arraycopy(b, 0, r, a.length, b.length);
		return r;
	}

	/** A stop flag shared by the encoder's threads. */
	private static final class RunFlag {
		private volatile boolean on = true;

		boolean get() {
			return on;
		}

		void clear() {
			on = false;
		}
	}
}
