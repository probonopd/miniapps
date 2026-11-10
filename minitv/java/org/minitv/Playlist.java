package org.minitv;

import android.content.Context;
import android.media.tv.TvInputManager;
import android.os.Environment;
import java.io.BufferedReader;
import java.io.File;
import java.io.FileInputStream;
import java.io.IOException;
import java.io.InputStreamReader;
import java.util.ArrayList;
import java.util.List;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/** The channels of /sdcard/playlist.m3u: an address each, and the name from its #EXTINF line. */
final class Playlist {
	/** The address of the channel that shows the HDMI input instead of a stream. */
	static final String HDMI = "hdmi:";
	// False on a device without the TV input framework (a smart display): the HDMI channel is then not there at all.
	private static volatile boolean hdmiPresent = true;
	private static final Pattern ALIASES = Pattern.compile("aliases=\"([^\"]*)\"");
	private static final Pattern ZAPP_ID = Pattern.compile("zapp-id=\"([^\"]*)\"");
	private static final Pattern SPOKEN = Pattern.compile("spoken=\"([^\"]*)\"");

	// The programme guide (channel list with what is on, and the banner after a channel change) is on unless the
	// header says #EXTM3U guide="off".
	boolean guide = true;
	final List<String> names = new ArrayList<>();
	final List<String> urls = new ArrayList<>();
	// The id of the channel at the Zapp backend (zapp-id="..." in its #EXTINF line), or null: no guide then.
	final List<String> zappIds = new ArrayList<>();
	// What is said when the channel is announced: spoken="..." of its #EXTINF line, else SpokenName of its name.
	final List<String> spoken = new ArrayList<>();
	// What can be said for a channel: its name and the alternative names of aliases="a;b" in its #EXTINF line.
	final List<String> phrases = new ArrayList<>();
	final List<Integer> owners = new ArrayList<>();

	/** The index of the channel with this name (case does not matter), or -1. */
	int indexOfName(String name) {
		for (int i = 0; i < names.size(); i++) {
			if (names.get(i).equalsIgnoreCase(name == null ? "" : name.trim())) return i;
		}
		return -1;
	}

	/** Must run before the first load: finds out whether the HDMI channel can exist on this device. */
	static void init(Context context) {
		try {
			((TvInputManager) context.getSystemService(Context.TV_INPUT_SERVICE)).getTvInputList();
			hdmiPresent = true;
		} catch (NullPointerException e) {
			// getTvInputList throws this when the system has no TV input manager service.
			hdmiPresent = false;
		}
	}

	static File file() {
		return new File(Environment.getExternalStorageDirectory(), "playlist.m3u");
	}

	static Playlist load() {
		try (BufferedReader r = new BufferedReader(new InputStreamReader(new FileInputStream(file()), "UTF-8"))) {
			return parse(r);
		} catch (IOException e) {
			// No playlist: the callers hand over to another launcher.
			return new Playlist();
		}
	}

	/** The channels of an M3U text. */
	static Playlist parse(BufferedReader r) throws IOException {
		Playlist playlist = new Playlist();
		{
			String line, name = null, aliases = null, zappId = null, said = null;
			boolean hidden = false;
			while ((line = r.readLine()) != null) {
				line = line.replace("﻿", "").trim();
				if (line.startsWith("#EXTM3U")) {
					playlist.guide = !line.contains("guide=\"off\"");
				} else if (line.startsWith("#EXTINF")) {
					// The name follows the first comma after the attributes (quoted values may contain commas).
					int comma = line.indexOf(',', Math.max(line.lastIndexOf('"'), 0));
					name = comma < 0 ? null : line.substring(comma + 1).trim();
					Matcher m = ALIASES.matcher(line);
					aliases = m.find() ? m.group(1) : null;
					m = ZAPP_ID.matcher(line);
					zappId = m.find() && !m.group(1).isEmpty() ? m.group(1) : null;
					m = SPOKEN.matcher(line);
					said = m.find() && !m.group(1).trim().isEmpty() ? m.group(1).trim() : null;
					hidden = line.contains("hidden=\"yes\"");
				} else if (!line.isEmpty() && line.charAt(0) != '#' && (hidden || (!hdmiPresent && line.equals(HDMI)))) {
					// A channel hidden in the playlist (or HDMI without a TV input service) is not there at all: not zapped to,
					// not listed, not speakable.
					name = null;
					aliases = null;
					zappId = null;
					said = null;
					hidden = false;
				} else if (!line.isEmpty() && line.charAt(0) != '#') {
					int index = playlist.urls.size();
					playlist.urls.add(line);
					playlist.zappIds.add(zappId);
					playlist.names.add(name == null || name.isEmpty() ? line : name);
					playlist.spoken.add(said != null ? said : SpokenName.of(playlist.names.get(index)));
					playlist.phrases.add(playlist.names.get(index));
					playlist.owners.add(index);
					if (aliases != null) {
						for (String alias : aliases.split(";")) {
							if (!alias.trim().isEmpty()) {
								playlist.phrases.add(alias.trim());
								playlist.owners.add(index);
							}
						}
					}
					name = null;
					aliases = null;
					zappId = null;
					said = null;
				}
			}
		}
		return playlist;
	}
}
