/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
package org.minicall;

import android.graphics.SurfaceTexture;
import android.hardware.Camera;
import android.media.MediaCodec;
import android.media.MediaCodecInfo;
import android.media.MediaFormat;
import android.opengl.EGL14;
import android.opengl.EGLConfig;
import android.opengl.EGLContext;
import android.opengl.EGLDisplay;
import android.opengl.EGLExt;
import android.opengl.EGLSurface;
import android.opengl.GLES11Ext;
import android.opengl.GLES20;
import android.opengl.Matrix;
import android.os.Bundle;
import android.os.Handler;
import android.os.HandlerThread;
import android.util.Log;
import android.view.Surface;
import java.io.IOException;
import java.nio.ByteBuffer;
import java.nio.ByteOrder;
import java.nio.FloatBuffer;
import java.util.List;

/**
 * Camera to H.264 entirely in hardware: the camera draws into a GPU texture, the GPU turns the picture upright and
 * draws it into the encoder's input surface. No frame passes through Java, which on the Echo Show cost frame rate
 * and in portrait would have needed a rotation per frame.
 */
@SuppressWarnings("deprecation") // android.hardware.Camera: camera2 on Android 7 devices of this kind is LEGACY anyway.
final class CameraEncoder {
	private static final String TAG = "minicall";
	private static final String AVC = MediaFormat.MIMETYPE_VIDEO_AVC;

	private static final String VERTEX = "uniform mat4 uTex;\nuniform mat4 uPos;\nattribute vec4 aPos;\n"
			+ "attribute vec4 aTex;\nvarying vec2 vTex;\n"
			+ "void main() { gl_Position = uPos * aPos; vTex = (uTex * aTex).xy; }\n";
	private static final String FRAGMENT = "#extension GL_OES_EGL_image_external : require\n"
			+ "precision mediump float;\nvarying vec2 vTex;\nuniform samplerExternalOES sTex;\n"
			+ "void main() { gl_FragColor = texture2D(sTex, vTex); }\n";

	final int width;
	final int height;
	private final MediaCodec codec;
	private final HandlerThread thread = new HandlerThread("camera-gl");
	private final Handler handler;
	private final float[] texMatrix = new float[16];
	private final float[] posMatrix = new float[16];
	private Camera camera;
	private SurfaceTexture texture;
	private Surface input;
	private EGLDisplay display = EGL14.EGL_NO_DISPLAY;
	private EGLContext context = EGL14.EGL_NO_CONTEXT;
	private EGLSurface surface = EGL14.EGL_NO_SURFACE;
	private EGLConfig eglConfig;
	// The self view: the same frame once more, mirrored, into the call screen's small picture.
	private EGLSurface preview = EGL14.EGL_NO_SURFACE;
	private int previewW, previewH;
	private final float[] mirrorMatrix = new float[16];
	private float scaleX = 1f, scaleY = 1f;
	private int program;
	private long startNs;
	private long lastNs;
	private final long frameNs;
	volatile int frames;

	/**
	 * Opens the front camera near width x height (landscape) and an encoder for the picture turned by rotation
	 * degrees (clockwise), so the far end gets it upright. With crop, the encoder takes exactly wantW x wantH
	 * (the far end's screen shape) and the camera picture is cut to it, keeping the middle.
	 */
	CameraEncoder(int wantW, int wantH, boolean crop, int fps, int bitrate, int rotation) throws IOException {
		thread.start();
		handler = new Handler(thread.getLooper());
		frameNs = 1000000000L / fps;
		int[] size = new int[2];
		IOException[] fail = new IOException[1];
		MediaCodec[] enc = new MediaCodec[1];
		runOnGl(() -> {
			try {
				int id = frontCamera();
				camera = Camera.open(id);
				Camera.Parameters prm = camera.getParameters();
				Camera.Size sz = pickSize(prm.getSupportedPreviewSizes(), wantW, wantH);
				if (sz == null) throw new IOException("no usable camera size");
				prm.setPreviewSize(sz.width, sz.height);
				int[] range = null;
				for (int[] r : prm.getSupportedPreviewFpsRange()) {
					if (r[1] >= fps * 1000 && (range == null || r[1] < range[1] || r[0] > range[0])) range = r;
				}
				if (range != null) prm.setPreviewFpsRange(range[0], range[1]);
				List<String> focus = prm.getSupportedFocusModes();
				if (focus != null && focus.contains(Camera.Parameters.FOCUS_MODE_CONTINUOUS_VIDEO)) {
					prm.setFocusMode(Camera.Parameters.FOCUS_MODE_CONTINUOUS_VIDEO);
				}
				camera.setParameters(prm);
				boolean upright = rotation % 180 != 0;
				int camW = upright ? sz.height : sz.width, camH = upright ? sz.width : sz.height;
				size[0] = crop ? wantW : camW;
				size[1] = crop ? wantH : camH;
				// Scale the camera picture so that it covers the encoder's picture; what sticks out is cut off.
				double cam = (double) camW / camH, out = (double) size[0] / size[1];
				scaleX = cam > out ? (float) (cam / out) : 1f;
				scaleY = cam > out ? 1f : (float) (out / cam);
				Log.i(TAG, "camera " + id + ": " + sz.width + "x" + sz.height + ", sent " + size[0] + "x" + size[1]
						+ " (turned " + rotation + "), " + fps + " fps, " + bitrate + " bit/s");

				MediaFormat f = MediaFormat.createVideoFormat(AVC, size[0], size[1]);
				f.setInteger(MediaFormat.KEY_COLOR_FORMAT, MediaCodecInfo.CodecCapabilities.COLOR_FormatSurface);
				f.setInteger(MediaFormat.KEY_BIT_RATE, bitrate);
				// Constant: the sender paces at the agreed rate, and every bit above it waits in a queue, which
				// put the picture seconds behind the sound.
				f.setInteger(MediaFormat.KEY_BITRATE_MODE,
						MediaCodecInfo.EncoderCapabilities.BITRATE_MODE_CBR);
				f.setInteger(MediaFormat.KEY_FRAME_RATE, fps);
				f.setInteger(MediaFormat.KEY_I_FRAME_INTERVAL, 2);
				f.setInteger(MediaFormat.KEY_PROFILE, MediaCodecInfo.CodecProfileLevel.AVCProfileBaseline);
				f.setInteger(MediaFormat.KEY_LEVEL, MediaCodecInfo.CodecProfileLevel.AVCLevel31);
				enc[0] = MediaCodec.createEncoderByType(AVC);
				enc[0].configure(f, null, null, MediaCodec.CONFIGURE_FLAG_ENCODE);
				input = enc[0].createInputSurface();
				enc[0].start();

				setUpGl(rotation);
				texture.setOnFrameAvailableListener(st -> draw(), handler);
				camera.setPreviewTexture(texture);
				camera.startPreview();
			} catch (IOException e) {
				fail[0] = e;
			} catch (RuntimeException e) {
				fail[0] = new IOException("camera: " + e.getMessage());
			}
		});
		codec = enc[0];
		width = size[0];
		height = size[1];
		if (fail[0] != null) {
			close();
			throw fail[0];
		}
	}

	MediaCodec codec() {
		return codec;
	}

	void requestKeyframe() {
		Bundle b = new Bundle();
		b.putInt(MediaCodec.PARAMETER_KEY_REQUEST_SYNC_FRAME, 0);
		codec.setParameters(b);
	}

	void close() {
		runOnGl(() -> {
			// Frames already queued on this thread run after this; they must find nothing left to draw on.
			if (texture != null) texture.setOnFrameAvailableListener(null);
			if (camera != null) {
				camera.stopPreview();
				camera.release();
				camera = null;
			}
			if (display != EGL14.EGL_NO_DISPLAY) {
				EGL14.eglMakeCurrent(display, EGL14.EGL_NO_SURFACE, EGL14.EGL_NO_SURFACE, EGL14.EGL_NO_CONTEXT);
				if (preview != EGL14.EGL_NO_SURFACE) EGL14.eglDestroySurface(display, preview);
				EGL14.eglDestroySurface(display, surface);
				EGL14.eglDestroyContext(display, context);
				EGL14.eglTerminate(display);
				display = EGL14.EGL_NO_DISPLAY;
			}
			if (texture != null) texture.release();
			texture = null;
			if (input != null) input.release();
		});
		thread.quitSafely();
		if (codec != null) {
			try {
				codec.stop();
			} catch (IllegalStateException ignored) {
				// Never started.
			}
			codec.release();
		}
	}

	/** Runs r on the GL thread and waits for it. */
	private void runOnGl(Runnable r) {
		Object done = new Object();
		boolean[] finished = new boolean[1];
		boolean posted = handler.post(() -> {
			try {
				r.run();
			} finally {
				synchronized (done) {
					finished[0] = true;
					done.notifyAll();
				}
			}
		});
		if (!posted) return;
		synchronized (done) {
			while (!finished[0]) {
				try {
					done.wait();
				} catch (InterruptedException e) {
					Thread.currentThread().interrupt();
					return;
				}
			}
		}
	}

	private void setUpGl(int rotation) {
		display = EGL14.eglGetDisplay(EGL14.EGL_DEFAULT_DISPLAY);
		int[] v = new int[2];
		EGL14.eglInitialize(display, v, 0, v, 1);
		// RECORDABLE: a surface that a video encoder can take.
		int[] attrs = {EGL14.EGL_RED_SIZE, 8, EGL14.EGL_GREEN_SIZE, 8, EGL14.EGL_BLUE_SIZE, 8,
				EGL14.EGL_RENDERABLE_TYPE, EGL14.EGL_OPENGL_ES2_BIT, 0x3142 /* EGL_RECORDABLE_ANDROID */, 1,
				EGL14.EGL_NONE};
		EGLConfig[] cfg = new EGLConfig[1];
		int[] n = new int[1];
		if (!EGL14.eglChooseConfig(display, attrs, 0, cfg, 0, 1, n, 0) || n[0] == 0) {
			throw new IllegalStateException("no recordable EGL config");
		}
		eglConfig = cfg[0];
		context = EGL14.eglCreateContext(display, cfg[0], EGL14.EGL_NO_CONTEXT,
				new int[] {EGL14.EGL_CONTEXT_CLIENT_VERSION, 2, EGL14.EGL_NONE}, 0);
		surface = EGL14.eglCreateWindowSurface(display, cfg[0], input, new int[] {EGL14.EGL_NONE}, 0);
		if (!EGL14.eglMakeCurrent(display, surface, surface, context)) throw new IllegalStateException("eglMakeCurrent");

		program = GLES20.glCreateProgram();
		GLES20.glAttachShader(program, shader(GLES20.GL_VERTEX_SHADER, VERTEX));
		GLES20.glAttachShader(program, shader(GLES20.GL_FRAGMENT_SHADER, FRAGMENT));
		GLES20.glLinkProgram(program);
		int[] tex = new int[1];
		GLES20.glGenTextures(1, tex, 0);
		GLES20.glBindTexture(GLES11Ext.GL_TEXTURE_EXTERNAL_OES, tex[0]);
		GLES20.glTexParameteri(GLES11Ext.GL_TEXTURE_EXTERNAL_OES, GLES20.GL_TEXTURE_MIN_FILTER, GLES20.GL_LINEAR);
		GLES20.glTexParameteri(GLES11Ext.GL_TEXTURE_EXTERNAL_OES, GLES20.GL_TEXTURE_MAG_FILTER, GLES20.GL_LINEAR);
		texture = new SurfaceTexture(tex[0]);
		// The picture turned clockwise on the way to the encoder (negative angle in GL's counter-clockwise sense).
		float[] turn = new float[16];
		Matrix.setRotateM(turn, 0, -rotation, 0, 0, 1);
		float[] cover = new float[16];
		Matrix.setIdentityM(cover, 0);
		Matrix.scaleM(cover, 0, scaleX, scaleY, 1);
		Matrix.multiplyMM(posMatrix, 0, cover, 0, turn, 0);
		// People expect to see themselves as in a mirror.
		float[] flip = new float[16];
		Matrix.setIdentityM(flip, 0);
		Matrix.scaleM(flip, 0, -1, 1, 1);
		Matrix.multiplyMM(mirrorMatrix, 0, flip, 0, posMatrix, 0);
	}

	/** The self view's texture (from a TextureView) and its size, or null to stop drawing it. */
	void setPreview(SurfaceTexture target, int w, int h) {
		runOnGl(() -> {
			if (display == EGL14.EGL_NO_DISPLAY) return;
			if (preview != EGL14.EGL_NO_SURFACE) {
				EGL14.eglMakeCurrent(display, surface, surface, context);
				EGL14.eglDestroySurface(display, preview);
				preview = EGL14.EGL_NO_SURFACE;
			}
			if (target != null) {
				preview = EGL14.eglCreateWindowSurface(display, eglConfig, target, new int[] {EGL14.EGL_NONE}, 0);
				previewW = w;
				previewH = h;
			}
		});
	}

	private static int shader(int type, String src) {
		int s = GLES20.glCreateShader(type);
		GLES20.glShaderSource(s, src);
		GLES20.glCompileShader(s);
		return s;
	}

	private static final FloatBuffer QUAD = floats(-1, -1, 0, 0, 1, -1, 1, 0, -1, 1, 0, 1, 1, 1, 1, 1);

	private static FloatBuffer floats(float... f) {
		FloatBuffer b = ByteBuffer.allocateDirect(f.length * 4).order(ByteOrder.nativeOrder()).asFloatBuffer();
		b.put(f).position(0);
		return b;
	}

	/** A camera frame: onto the encoder's surface, unless it comes faster than the frame rate asked for. */
	private void draw() {
		if (texture == null) return;
		texture.updateTexImage();
		long now = System.nanoTime();
		if (startNs == 0) startNs = now;
		if (lastNs != 0 && now - lastNs < frameNs * 3 / 4) return;
		lastNs = now;
		texture.getTransformMatrix(texMatrix);
		quad(posMatrix, width, height);
		EGLExt.eglPresentationTimeANDROID(display, surface, now - startNs);
		EGL14.eglSwapBuffers(display, surface);
		frames++;
		if (preview != EGL14.EGL_NO_SURFACE && EGL14.eglMakeCurrent(display, preview, preview, context)) {
			quad(mirrorMatrix, previewW, previewH);
			EGL14.eglSwapBuffers(display, preview);
			EGL14.eglMakeCurrent(display, surface, surface, context);
		}
	}

	private void quad(float[] pos, int w, int h) {
		GLES20.glViewport(0, 0, w, h);
		GLES20.glUseProgram(program);
		int aPos = GLES20.glGetAttribLocation(program, "aPos");
		int tex = GLES20.glGetAttribLocation(program, "aTex");
		QUAD.position(0);
		GLES20.glVertexAttribPointer(aPos, 2, GLES20.GL_FLOAT, false, 16, QUAD);
		GLES20.glEnableVertexAttribArray(aPos);
		QUAD.position(2);
		GLES20.glVertexAttribPointer(tex, 2, GLES20.GL_FLOAT, false, 16, QUAD);
		GLES20.glEnableVertexAttribArray(tex);
		GLES20.glUniformMatrix4fv(GLES20.glGetUniformLocation(program, "uTex"), 1, false, texMatrix, 0);
		GLES20.glUniformMatrix4fv(GLES20.glGetUniformLocation(program, "uPos"), 1, false, pos, 0);
		GLES20.glDrawArrays(GLES20.GL_TRIANGLE_STRIP, 0, 4);
	}

	/** The camera facing the person: phones have several, and the first one is usually the back camera. */
	static int frontCamera() {
		Camera.CameraInfo info = new Camera.CameraInfo();
		for (int i = 0; i < Camera.getNumberOfCameras(); i++) {
			Camera.getCameraInfo(i, info);
			if (info.facing == Camera.CameraInfo.CAMERA_FACING_FRONT) return i;
		}
		return 0;
	}

	/** Clockwise degrees that turn the front camera's picture upright for a screen turned by displayDegrees. */
	static int rotation(int displayDegrees) {
		Camera.CameraInfo info = new Camera.CameraInfo();
		Camera.getCameraInfo(frontCamera(), info);
		// The front camera's picture as the sensor delivers it, seen from the person (not mirrored).
		return info.facing == Camera.CameraInfo.CAMERA_FACING_FRONT
				? (info.orientation + displayDegrees) % 360
				: (info.orientation - displayDegrees + 360) % 360;
	}

	/** The size closest in shape first and in pixels second; hardware encoders want multiples of 16. */
	private static Camera.Size pickSize(List<Camera.Size> sizes, int w, int h) {
		Camera.Size best = null;
		double bestScore = Double.MAX_VALUE;
		for (Camera.Size s : sizes) {
			if (s.width % 16 != 0 || s.height % 16 != 0) continue;
			double shape = Math.abs(Math.log((double) s.width * h / ((double) s.height * w)));
			double area = Math.abs(Math.log((double) s.width * s.height / ((double) w * h)));
			double score = shape * 4 + area;
			if (score < bestScore) {
				best = s;
				bestScore = score;
			}
		}
		return best;
	}
}
