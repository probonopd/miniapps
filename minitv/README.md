# minitv

A TV set and home screen for the HY310 projector: full-screen picture, channel up/down, volume up/down,
nothing else. No menus, no text. The only configuration is an M3U playlist. Playback is
[Media3](https://github.com/androidx/media) (ExoPlayer) 1.11.1.

For end users there is a separate [user guide](USER-GUIDE.md). This page is the technical one.

## Install

```sh
adb install -r -g minitv.apk
adb shell pm disable-user --user 0 com.google.android.katniss
adb shell pm disable-user --user 0 com.softwinner.awsource
adb shell pm disable-user --user 0 com.softwinner.awlivetv
python3 tools/make-playlist.py playlist.m3u
adb push playlist.m3u /sdcard/playlist.m3u
adb shell am start -n org.minitv/.MainActivity
```

The APK is 52 MB: it carries the speech model of the voice button. Disabling `com.google.android.katniss` (Google's
voice app) is what makes the voice button reach this app instead of Google, see "Voice". Undo with
`adb shell pm enable com.google.android.katniss`.

`-g` grants the storage permission, so no dialog ever appears. Each non-comment line of the playlist is a channel
(HLS and plain MPEG-TS over HTTP). A stream that fails or ends is tried again by itself (see "No picture, no network"). The last channel is
remembered.

`tools/make-playlist.py` writes a playlist with the live streams of the German-language public broadcasters
(43 channels) from the public MediathekViewWeb API, the way
[this gist comment](https://gist.github.com/Axel-Erfurt/b40584d152e1c2f13259590a135e05f4) describes. Python 3, no
other packages.

## Make it the home screen

```sh
adb shell cmd role add-role-holder android.app.role.HOME org.minitv
```

The unit then boots into the picture and the Home key returns to it.

## Keys

| Function | Keys |
|---|---|
| Channel list | OK (short press) |
| Channel up | Channel up, Up, Page up |
| Channel down | Channel down, Down, Page down |
| Video call of the family room | Green color key (only when the setting `jitsi_room` is set; see "Video calls") |
| Channel down / up with a mouse or touch screen | Click or tap the left half of the screen (down) or the right half (up). A press that moves more than 48 px is not a click. Nothing happens while a message is shown, in a video call or while the channel list is open. |
| Volume up / down | Right / Left (system music volume, no on-screen display). The Volume keys themselves are handled by the system before the app sees them, so the system's own volume bar appears. |

Every other key is swallowed.

## Remote: the Menu button

The remote's Menu button sends the Settings key, which the vendor window manager (`TvWindowManager`) handles itself
and opens its Settings screen: it never reaches apps, and the Menu key is swallowed the same way. So the button is
remapped to the red color key (`KEYCODE_PROG_RED`), which the app treats like Menu: a short press does nothing, a hold
of 2.5 s opens the list of other home apps. The remap is a copy of the remote's key layout
`/system/usr/keylayout/Vendor_1d5a_Product_c081.kl` with `key 0x8b    PROG_RED` in the Magisk module
(`system/usr/keylayout/` of `usb_audio_autoswitch`); it needs a reboot. The vendor Settings screen
`com.htc.spectraos/.activity.MainSettingActivity` is disabled (`su -c pm disable ...`) so that the key can never open it.

## Getting back to another launcher

| How | What happens |
|---|---|
| Hold Back, Menu or OK for 2.5 s | a list of the other home apps opens (Up/Down, OK starts one, Back closes); the default home app stays this one |
| Press Home 3 times within 3 s | opens another launcher (each time the next one: SpectraOs, Projectivy, ...); the default stays |
| Remove the playlist (`adb shell rm /sdcard/playlist.m3u`) | the app hands over to another launcher whenever it starts |
| From the PC | `adb shell cmd role add-role-holder android.app.role.HOME com.spocky.projengmenu` (or `com.htc.spectraos` for the vendor launcher) |
| Remove the app | `adb uninstall org.minitv` |

## Build

No Android SDK needed. Needs a JDK 17+, `apktool` with a framework installed, `python3` with `cryptography`, `curl`,
`unzip`, `zip`.

```sh
sh build.sh
```

It downloads the pinned libraries (`libs.txt`, hashes in `libs.sha256`) into `build/libs`, compiles with `javac`,
dexes with D8, builds manifest and resources with apktool, adds the dex files and signs (v1 signature; the app
targets API 28 so Android 11 accepts that). These steps are shared with minicall in `../common/` (`fetch.sh`,
`apk.sh`, `sign.py`). Both apps are signed with the one key in `../keys/signing-key.pem` (not in git): keep it, an
update only installs over a version signed with the same key, and minicall takes calls only from apps signed with
it (`org.minicall.permission.CALL`, declared in both manifests).

## Voice

Hold the voice button of the remote, say a channel name, release: the TV switches. Everything runs on the projector;
no audio or text leaves it, and nothing is recorded.

How it works, found out on the black and white unit:

- The remote (a Bluetooth HID remote, "HBG remote", model HV411) sends one momentary `KEYCODE_VOICE_ASSIST`. The
  system handles that key before any app sees it and starts `android.speech.action.WEB_SEARCH`, which Google's
  voice app (`com.google.android.katniss`) answers by default. So an app cannot read the key, but it can answer the
  intent. `VoiceActivity` does, and Katniss is disabled so that nothing else does.
- The remote streams its microphone (SBC audio in HID reports) for as long as the button is held. The vendor audio
  driver (`BLEAudioDrive_HV411`) decodes it and the audio policy offers it as the built-in microphone input. With
  a USB headset plugged in, Android prefers the headset's microphone, so the app asks for the built-in input
  explicitly (`setPreferredDevice`). The device `hbg_audio` that the policy lists for the remote is not visible
  to apps.
- `VoiceActivity` records 16 kHz mono (`VOICE_RECOGNITION`) until the stream ends (level below 20 for 0.3 s) or
  8 s. It runs in its own process (`:voice`); there the speech model stays loaded between presses and Android may
  drop it under memory pressure instead of dropping the TV.
- Recognition is [Vosk](https://github.com/alphacep/vosk-api) 0.3.75 with the small German model 0.15 (both Apache
  2.0; 32-bit ARM libraries only; the model is in the APK as an asset and is unpacked once on first use). The
  recognizer only listens for the words of the channel names and their aliases, which makes it fast enough.
- `ChannelMatcher` turns the sentence into a channel: filler words ("das", "bitte", "zeig") are ignored, runs of
  spelled letters are joined ("em de er" or "m d r" = MDR, the model does not know the words `mdr`, `rbb`, `dw`),
  digits are spoken ("3Sat" = "drei sat"), and the shortest name that contains every recognized word wins.
- Alternative names ("Hessen" for HR, "Baden-Württemberg" for SWR BW, "Das Erste" for ARD) are an attribute of
  the channel's `#EXTINF` line: `#EXTINF:-1 aliases="Hessen;Hessischer Rundfunk",HR`. `tools/make-playlist.py`
  writes them for the public broadcasters (its `ALIASES` table); edit the playlist to add your own. Words the
  model does not know (`kinderkanal`, `dreisat`, `südwestrundfunk`) are ignored, so give an alias a form that
  contains known words too.
- The model is loaded in the background by `VoiceService` right after the first picture (about 9 s), so even the
  first press after boot is quick.

Measured on the unit (10 presses, all matched correctly): 2.5 to 4.8 s from pressing to the switch, of which 1.5
to 3 s is the person speaking; recognition itself takes about 1 s. Without the preload the first press needs
about 6 s longer.

`sh test.sh` runs the matching tests on the PC (plain Java). Debugging: while the file `/sdcard/voice-debug` exists,
the last ten utterances are kept as `voice-0.wav` to `voice-9.wav` in the app's private storage (read with root);
delete the file and the recordings when done.

## Start-up speed

- A stream that fails is tuned again after 3 s, but also immediately when a network comes up
  (`ConnectivityManager` callback, `retryPending` in `MainActivity.java`). At boot the first attempts fail until WLAN
  has connected (about 25 s after kernel start on the black and white unit), so this removes the wait for the timer.
- `DefaultLoadControl` starts playback after 1 s of buffered media (default 2.5 s) and keeps at most 30 s, which also
  makes channel changes faster. A weak stream may stutter a little more.
- Measuring: the app logs when it tunes and when the first frame is drawn, in milliseconds since boot:
  `adb logcat -d -s minitv`. The time since boot of the first picture is the `first frame` line of that boot.

## Loudness

Broadcast audio is mastered quietly: ffmpeg `ebur128` on 25 s of live audio gave ARD -24.4 LUFS and ZDF -25.8 LUFS,
while YouTube plays around -14 LUFS. The app therefore adds +10 dB with the platform `LoudnessEnhancer` effect on
the player's audio session (permission `MODIFY_AUDIO_SETTINGS`; the effect has a built-in compressor, so peaks do
not clip hard). If the effect cannot be created the app plays without it. `BOOST_MB` in `MainActivity.java` sets it.

## On-screen elements

- Header (off by default, see Settings): at a channel change the name (and number, 0 is HDMI) appears at once at the top left on a 70% black bar, before
  anything is loaded. It goes away, without animation, when the first picture is up, and stays as long as the stream
  does not start (together with the red or yellow screen).
- Footer (off by default): after the first picture the programme text and the date and time show on a 70% black bar at the bottom for
  6 s (`footer_seconds`), then bar and text slide down and fade out (400 ms). The header and footer bars are only there while there is
  text in them.
- Messages: the dimmed background and the text fade in together (300 ms) and fade out (400 ms) when the message ends
  (a key, a click or tap on the screen, `show_seconds`, an empty `cmd/message`, a message without a screen part); a new text that replaces a shown
  one just takes its place. The message is over when the fade out starts: a key pressed during it works normally
  (`MESSAGE_FADE_IN_MS`, `MESSAGE_FADE_OUT_MS` in `MainActivity.java`).
- The channel list (and the list of other home apps) slides in from the left and fades in, and slides out to the left
  when closed (300 ms). During the slide out its keys are already free.

Camera choice: the projector's own camera (`/dev/video0`, USB 0bda:5803) is the last resort. A root loop of the Magisk
module (`tools/camera-prefer.sh`, started by the module's `service.sh`) unbinds the built-in camera from `uvcvideo`
while another USB video camera is attached and binds it again when that one is gone, so Android (and the WebView) only
ever sees one camera and the call cannot pick the wrong one; the camera ids of Android are not stable (an id can be the
built-in or the webcam), so choosing in the page does not work. The webcam must be a standard USB video class (UVC)
camera: a Sony PlayStation Eye (1415:2000, vendor-specific interface, no gspca driver in this kernel) is not seen as a
camera at all. The built-in camera is mounted so that its picture comes out rotated by 90 degrees.

Microphone choice: with a USB headset and a USB webcam that has a microphone, Android selects only the newest USB sound
card (the other one is dropped, so the sound can end up on the built-in speaker). A second root loop of the same module
(`tools/audioroute/`, see [USB-AUDIO.md](../USB-AUDIO.md)) keeps the sound on the headset and makes the webcam's
microphone the input (for every app, the call's `VOICE_COMMUNICATION` capture included), whatever the plug order. Without
the webcam the headset microphone is used, without the headset Android's normal behavior applies.

Picture only: the room page is stripped to the video. A style sheet hides the known logos and name tags, and a script
hides every element of the page that has no video inside, every 0.7 s, so that logos (some servers show their own),
name tags, avatars and indicators never show; a room without any video is an empty gray screen.

Codecs: the WebView (Chromium 83) shows the other party's camera only when it is VP8 (newer browsers send scalable VP9),
so the call page asks the whole room for VP8 (`config.videoQuality.*` in the fragment); shared screens worked before.

## Settings (also over MQTT)

Settings are kept in the app's private preferences, are published retained as one JSON on `<topic>/config`, and can be
set one by one (`<topic>/set/<name>`, payload `on`/`off`, a number or text) or together (`<topic>/set/config`, a JSON
object with any of them: all values are checked first, and nothing changes if one is wrong; the answer is on
`result`). Home Assistant gets a switch, number or select for each (except the schedule).

| Setting | Values | Default | What |
|---|---|---|---|
| `footer` | on/off | off | the programme text and date and time on a bar at the bottom after a channel change |
| `footer_seconds` | 1 to 60 | 6 | how long the footer stays |
| `header` | on/off | off | the channel name at the top after a channel change |
| `header_mode` | `picture` / `seconds` | picture | `picture`: until the first picture is up, and as long as the stream does not start; `seconds`: for `header_seconds` |
| `header_seconds` | 1 to 60 | 3 | the time for `header_mode` seconds |
| `header_number` | on/off | off | the channel number in front of the name |
| `clock` | on/off | off | the time (HH:mm) at the top right, permanently |
| `guide` | on/off | the playlist header (`guide="off"`) | the channel list and the programme texts |
| `schedule` | JSON array | empty | scheduled messages, see below |
| `jitsi_room` | room name (letters, digits, `-` `_` `.`, 64 at most) or empty | empty | the family's video call room, joined with the green key; empty: the key does nothing |
| `jitsi_max_minutes` | 0 to 1440 | 120 | a video call ends by itself after this long; 0: never |
| `speech` | on/off | on | spoken text at all (see "Speech") |
| `device_name` | text, 0 to 40 characters | the `name=` of `mqtt.conf` | what the TV is called in Home Assistant and as the display name in video calls (empty: the name of `mqtt.conf`) |
| `speech_voice` | an Edge voice name (`^[a-z]{2}-[A-Z]{2}-[A-Za-z]+Neural$`) | `de-DE-KatjaNeural` | the voice; a German-only voice is recommended (see "Speech": multilingual voices guess the language) |
| `speech_rate` | `-99%` to `+99%` | `-5%` | how fast it speaks |
| `speech_volume` | 0 to 100 | 100 | volume of the speech, on top of the system volume it follows like the TV (see "Speech") |
| `speech_boost_db` | 0 to 15 | 10 | loudness lift of the speech (`LoudnessEnhancer`, like the TV's +10 dB) |
| `speech_repeat` | 1 to 10 | 1 | how often a text is spoken, where a command or schedule entry does not say (`repeat`) |
| `speech_pause` | 0 to 60 | 5 | seconds of silence between the repeats, where a command or entry does not say (`pause`) |
| `speech_duck_percent` | 0 to 100 | 8 | the TV volume while speech plays, in percent of its gain |
| `speech_cache_mb` | 10 to 200 | 200 | size limit of the speech cache |
| `speak_channel` | on/off | off | the station name is spoken after a channel change (see "Speech") |
| `no_picture_seconds` | 5 to 120 | 20 | the red screen when a selected channel has had no picture this long (yellow, no internet: twice that); see "No picture, no network" |
| `message_mode` | `text` / `speech` / `both` | text | how pushed and scheduled messages are presented where they do not say (`mode`), see "Messages" |
| `message_seconds` | 0 to 3600 | 0 | seconds the text of a message stays after it has been spoken (after it appeared, when it is not spoken), where it does not say (`show_seconds`); 0: until a key is pressed |

Connection and security settings (`mqtt.conf`: broker, password, `url`, `shell`, `jitsi`, `jitsi-server`, `jitsi-allowed`, `speech`) cannot be changed over MQTT, on purpose.

Scheduled messages: `[{"id":"bettzeit","text":"Bettzeit","from":"21:00","to":"05:00","every":30,"enabled":true,"mode":"both","show_seconds":0,"repeat":2,"pause":5}]`.
Inside the window `from` to `to` (it may cross midnight; the same time twice means all day) the text comes up like a
pushed message at `from` and then every `every` minutes (1 to 1440) until `to`. Keys: `id` (to tell entries apart),
`text` (1 to 500 characters), `from`, `to` (HH:mm), `every`, `enabled` (true/false, default true), and the presentation
keys of a message (see "Messages"): `mode`, `show_seconds`, `repeat`, `pause`, and the deprecated `speak`. At most 20
entries. They are checked at every full minute while the TV app is in front, in the time zone of the device. Get:
`config` (key `schedule`); set: `set/schedule` (the array) or `set/config`.

## Messages

A message (`cmd/message`, or a schedule entry) can be shown on the screen, spoken, or both. Its keys, all optional but
`text`; what a message does not say comes from the settings:

| Key | Values | Default | What |
|---|---|---|---|
| `text` | text | | what is shown and said |
| `mode` | `text`, `speech`, `both` | `message_mode` | on screen only, voice only, or both |
| `show_seconds` | 0 to 3600 | `message_seconds` | seconds the text stays after it has been spoken (after the last repeat; after it appeared, when it is not spoken; after the failure is known, when it is shown because it could not be spoken); 0: it stays until a key is pressed |
| `repeat` | 1 to 10 | `speech_repeat` | how often the voice part is spoken |
| `pause` | 0 to 60 | `speech_pause` | seconds of silence between the repeats |
| `speak` | true / false | | deprecated form of `mode`: true is `both`, false is `text`; `mode` wins when both are given |

A key of the message itself wins over the setting. Wrong values (`show_seconds` 4000, `mode` "loud", `repeat` 0, a
string where a number belongs) are refused with an answer on `result` and an error event.

| `mode` | Seen | Heard | Ends |
|---|---|---|---|
| `text` | the text over the dimmed picture | nothing | a key, `show_seconds`, the next message, an empty `cmd/message` |
| `speech` | nothing (the text if it cannot be spoken) | the text, `repeat` times | a key, the last repeat, the next message, an empty `cmd/message`, `cmd/say-stop`, a video call |
| `both` | the text | the text, `repeat` times | screen and voice: a key, the next message, an empty `cmd/message`; the screen alone: `show_seconds` after the voice has ended (after the last repeat, or after `cmd/say-stop` or a call stopped it); the voice alone: the last repeat, `cmd/say-stop`, a video call |

Acknowledgement by key: the first key pressed on the remote while a message is active (its text on the screen, or
its voice playing, being fetched or pausing between repeats; also a series of `cmd/say`) ends the whole message at
once: the text goes, the speech stops, the TV comes back up. That key does nothing else (not when it repeats, not when
it is released). With `mode` speech there is nothing to see, but the key still ends it: this is how someone confirms
having heard it.

A message is never lost: when its voice part cannot be spoken (setting `speech` off, `speech=off` in `mqtt.conf`, a
video call, a text that is not cached and cannot be fetched, the service refuses it), a `speech` message is shown on
the screen instead and a `both` message just stays there, and an error event of type `speech` says why. A new
message, from any source, replaces the active one completely (its screen, its voice, its timers).

## Channel numbers

The number of a channel is its position among the visible channels of the playlist, 0 is HDMI. `make-playlist.py`
puts ARD first (1), ZDF second (2) and SWR BW third (3), then the others in their order (`FIRST` in the script). The
number is shown in the header, and is what the MQTT topic `set/channel` and the Home Assistant channel list use
(by name there). Hidden channels have no number.

## Channel 0: HDMI


On a device without the TV input framework (no TV input manager service, e.g. a smart display) the HDMI channel is not there at all: it is left out of the playlist when it is read, so it is not zapped to, listed or speakable, and channel 0 is the first stream.
The first channel of the generated playlist is `HDMI` (address `hdmi:`; say "HDMI", "Eingang" or "Computer"). It
shows the projector's HDMI input inside the app, with the system's TV input framework (`TvView`, the first HDMI
input `HW1`; the same way as `china-android-tv-deshittify/hdmi-input`). No signal: the input service draws its blue "no signal"
screen. There is no input chooser. The vendor's source selector and live TV (`com.softwinner.awsource`,
`awlivetv`) are disabled, because the remote's input key would open them and leave the app.

## Hidden channels

`hidden="yes"` in a channel's `#EXTINF` line removes it from zapping, the list and voice. `make-playlist.py` hides
the regional variants (table `HIDDEN`): one channel per broadcaster stays, except SWR (both). 26 of 43 + HDMI show.

## No picture, no network

- A channel is tried again and again in the background until it plays: every failed attempt (a player error, the
  stream ended, no first frame within 7 s of the attempt, or buffering for 15 s) is followed by the next one after
  0.5 s, 1.5 s, 3 s and then every 5 s, for as long as the channel is selected; a network that comes back tries at
  once. Before the 3rd, 5th, 7th ... attempt the player is released and made new (a player that keeps failing may be
  stuck; seen after long runs under memory pressure). A parse error flips between HLS and the detected format.
  Everything pending belongs to the channel selection and goes with a channel change.
- Only when a selected channel has had no picture for `no_picture_seconds` (default 20 s; the clock starts when the
  channel is selected, or when a playing stream loses its picture) the screen turns plain red, no text; without
  internet plain yellow after twice that. The attempts go on, and the colour goes as soon as a picture comes. Channel
  +/- still work. The channel name (header) stays during the attempts, and the picture area stays black.
- Every failed attempt is a `stream` error event with the attempt number ("attempt 3"; the same error once a minute
  at most), also kept as `last_stream_error` in `telemetry`; while the screen is red or yellow, `state` says which
  stream error explains it (`stream_error`). The last channel is remembered by name.
- The Settings and input keys are handled by the system before the app sees them; the input key's vendor screens
  (`awsource`, `awlivetv`) are disabled, the Settings key still opens the vendor settings (Home returns).

## Programme guide (optional)

A short press of OK opens the list of all channels (left 40% of the screen, `ChannelList.java`): Up/Down (or Channel
+/-) move, OK tunes the selected channel and closes the list, Back or 15 s without a key close it. The selected
channel also shows what is on (time span and title). After every channel change (keys, list, voice) the time, title
and subtitle appear at the bottom for 6 s. The data comes from the public
[Zapp backend](https://github.com/mediathekview/zapp-backend) of the MediathekView project
(`https://api.zapp.mediathekview.de/v1/shows/<id>`, one small JSON request, `Guide.java`).

- Never slows switching: the channel is tuned first; the banner is requested only after the first picture is up;
  requests run on one low-priority background thread; the list asks 0.3 s after the last key, so scrolling quickly
  fires nothing. Timeouts are 8 s.
- Caching: an answer is kept until its programme ends (never longer than 15 min), so switching back to a channel
  asks nothing and never shows a programme that is over. After a failed request (or an answer that is already over)
  the guide stays quiet for 30 s instead of asking on every switch. Nothing is stored on disk.
- Optional, on by default: `#EXTM3U guide="off"` in the first line of the playlist turns the whole feature off: OK does
  nothing, no request is ever made. `python3 tools/make-playlist.py --no-guide` writes such a playlist (also without
  `zapp-id`). With the guide on, a channel without `zapp-id="..."` in its `#EXTINF` line is in the list but has no
  texts; the generated playlist has `zapp-id` for 27 of its 43 channels (table `ZAPP_IDS`; the ids are at
  `https://api.zapp.mediathekview.de/v1/channelInfoList`).
- The only data that leaves the projector is the id of the channel being listed or tuned.
- Not covered: ORF, DW, ARTE FR, WDR Lokalzeit; they show no guide text. Only now, no "next" (the backend returns one
  programme). If the server is down or there is no network, nothing is shown and nothing else changes. The backend takes the data from the ARD
  programm-api and the ZDF API, see [EPG-SOURCES.md](EPG-SOURCES.md) for these and for the other sources that were
  checked (full XMLTV guides, ARTE, ORF) if the guide is to be extended.

## Remote control over MQTT (optional)

`MqttService` (HiveMQ MQTT client 1.4.1, MQTT 3.1.1 over TLS, last will) connects to a broker and lets home
automation control and watch the TV. It exists only when `/sdcard/mqtt.conf` exists (see `mqtt.conf.example`; the app
moves the file into its private storage at the first start and deletes it from the card). Works with HiveMQ Cloud,
EMQX Cloud and any TLS broker; the server certificate and host name are always verified (system trust store, or
`cafile=` for a private CA). `tls=off` is accepted only for a broker in the local network. Credentials come from the
file: use a broker user whose ACL is limited to the `topic=`.

Topics below `<topic>`:

| Topic | Direction | Payload |
|---|---|---|
| `status` | published, retained | `{"state":"online","since":...,"previous_stop":"..."}`; or `{"state":"offline","reason":"...","time":"..."}` (the time, UTC ISO 8601, is only in the message of a clean stop; the last will of a lost connection has none: the `time` of the retained `telemetry`, published every minute, then says when the TV was last seen) |
| `state` | published, retained | `{"channel","picture","screen","message","message_mode","stream_error","call","room","speaking","volume","uptime_s","last_key_s_ago"}`; `message` is true while the text of a message is on the screen, `message_mode` the mode of the active message while any part of it runs (else empty); `speaking` is true while a voice series plays, including its pauses; `call` is true while a video call is open, `room` its room (empty otherwise); `screen` is `ok`, `hdmi`, `no-picture-yet`, `red-no-picture` or `yellow-no-internet`; every change and every 60 s |
| `set/channel` | command | channel name (any case) or its number in the list (0 is HDMI) |
| `set/volume` | command | 0 to 100 (system music volume) |
| `cmd/play` | command | any address (`https://...m3u8`, ...); plays until the next channel change; `url=off` disables |
| `cmd/jitsi` | command | a room name (`Familie-Mueller`) or the full https address of a room on any server (`jitsi-allowed`): opens the video call fullscreen; empty or `hangup` ends it; anything else is refused (answer on `result`, an error event of type `jitsi`); `jitsi=off` disables |
| `cmd/say` | command | a text to speak (1000 characters at most), or `{"text":"...","voice":"de-DE-KatjaNeural","repeat":3,"pause":5}` (`voice`, `repeat` 1 to 10 and `pause` 0 to 60 s optional; defaults from the settings); empty stops; refused (answer on `result`, an error event of type `speech`) when `speech=off`, the setting `speech` is off, during a video call, or for a bad voice; a render that fails is an error event of type `speech` |
| `cmd/say-prefetch` | command | a JSON array of up to 50 texts (1000 characters each): rendered and cached with the voice and rate of the settings, not played; the answer on `result` says how many were rendered, cached already or failed |
| `cmd/say-stop` | command | anything: stops the speech |
| `cmd/screen` | command | `off`: display dark (Android's lowest brightness) while minitv keeps playing and listening (Echo Show, with the root service echoscreen from echo-show-2gen); `on`: normal brightness again. By voice: "Bildschirm aus" switches it off, "Ausschalten" goes to the home screen and switches it off too; any other voice command switches it on again |
| `cmd/message` | command | a text, or `{"text":"...","mode":"both","show_seconds":30,"repeat":2,"pause":5}` (keys `text`, `mode`, `show_seconds`, `repeat`, `pause`, deprecated `speak`; see "Messages"; a payload that does not start with `{` or is no valid JSON is a plain text with the settings). On screen the picture is dimmed and the text centered, as large as fits (160 sp for a few words down to 24 sp for a long text, never breaking a word). Any key on the remote ends it (that key does nothing else); the next message replaces it; an empty text ends it |
| `playlist` | published, retained | the playlist file as text (M3U), after every connect and change |
| `channels` | published, retained | the visible channels: `[{"number","name","url","zapp_id"}]`; the number is what `set/channel` takes |
| `set/playlist` | command | a whole new M3U text: every address line must be an address (`scheme://...`, or `hdmi:`), it needs at least one channel and at most 1 MB; the old file is kept as `playlist.m3u.bak`, the new one is loaded at once; the answer is on `result`; needs `url` on (it adds addresses) |
| `cmd/reload-playlist` | command | anything |
| `cmd/shell` | command | a shell command (`shell=on` needed); the answer is on `result` |
| `telemetry` | published, retained | `{"time","boot","uptime_s","online_since","channel","url","watching_since","watching_s","last_key","previous_shutdown":{"time","approximate","reason"},"last_error","last_stream_error","speech_cache":{"files","bytes"}}`; times are ISO 8601 in UTC; on every change and every minute, so the boot, shutdown and online timestamps stay on the broker while the TV is off. `previous_shutdown.time` is exact after a clean stop and the last heartbeat (up to a minute early, `approximate: true`) after a power loss or crash; `last_stream_error` is the newest `stream` error (every failed attempt, with `"attempt"`), kept apart from `last_error`, which the red screen's own event replaces |
| `errors` | published | one event per error: `{"time","type","channel","url","code","message"}`; types: `stream` (ExoPlayer error code, for example an unreachable address, or the stream ended), `no-picture` (red screen), `no-internet` (yellow screen), `hdmi`, `guide`, `command` (a command that failed), `speech` (a text that could not be rendered or played, a refused `cmd/say`), `jitsi` (a refused room, no WebView, the page did not load, the renderer crashed or was killed, a denied permission, not joined within 60 s, the time limit), `crash` (reported after the restart); the same error is reported once a minute at most; events from while the broker is unreachable are sent when it is back (50 at most); the newest is also `last_error` of `telemetry` |
| `result` | published | `{"command","exit","output"}` (first 16 KB; 30 s limit) or `{"command","ok":false,"message"}` |

Commands that arrive retained are ignored (a retained command would run again at every connect). `message` in
`state` is true while a message is shown, so automation sees when it was dismissed. All texts are UTF-8 (umlauts, ß and so on work in messages, channel names, addresses and shell commands). Home Assistant gets a Message text
entity and a Message shown sensor, plus timestamp sensors (Last boot, Online since, Watching since, Last shutdown) and Stream address and Last error.

Online or offline: the connection has a last will that the broker publishes on `status` as `offline` ("connection
lost") when the TV loses power, crashes, is killed or loses the network. A clean stop publishes the reason itself
first: `reboot (...)` or `power off (...)` (the system's shutdown broadcast; `adb reboot` does not send it and shows up
as "connection lost"), or `app stopped`. After the next start `previous_stop` of `status` says how the last run ended,
including `unclean (power loss, crash or killed)` when nothing was published. Tested against an EMQX Cloud serverless
broker: connect, last will, reboot reason, all commands.

Home Assistant: with `discovery=on` (the default) the TV publishes retained MQTT discovery messages on every
connect (and after a playlist reload) below `homeassistant/` (`discovery-prefix=`), so Home Assistant adds the device
by itself (name from `name=`, default "TV"). Entities: Channel (select, filled from the playlist), Volume (number),
Picture (binary sensor), Screen, Uptime and Last stop (diagnostic sensors; Last stop shows how the last run ended, or why
the TV is offline now), Reload playlist (button), and, only when enabled, Video call (text, shows the room), In call (binary sensor), Video call room (green key) and Video call time limit (config), Say (text), Message presentation (select), Message seconds on screen, Speech, Speech volume, Speech boost, TV volume while speaking, Speech cache size, Speech repeats, Pause between repeats, Speech voice and Speech rate (config), Play address (text), Shell command (text) and
Shell result (sensor with the output as attribute). Availability follows `status`, so the entities turn unavailable
when the TV is offline. A feature switched off removes its entities again (empty retained message). The messages were
checked against the broker (valid JSON, all required fields); they were not tried in a Home Assistant instance.

Shell commands run as the app's own user (`u0_a52`, no root): they can read what the app can, and `su -c` works only
if Magisk grants the app root. Anyone who can publish to `<topic>/cmd/shell` can run commands, so keep the broker
user's password secret, use a topic ACL, and leave `shell` off unless needed. Playing addresses can show anything
on the screen; `url=off` removes that.

## Speech


If an Ivona text-to-speech engine is installed on the device (a package starting with `com.ivona.`), the spoken text is rendered with it, on the device, instead of the online service.
The TV can speak: a text sent over MQTT (`cmd/say`, voice only), and pushed or scheduled messages with `mode`
`speech` or `both` (see "Messages"). On-device neural voices are too slow on this projector (measured real-time factor 2 to
6), so the audio comes from the "read aloud" service of the Microsoft Edge browser, fetched by the app itself
(`EdgeTts.java`, a plain-Java port of the python package [edge-tts](https://github.com/rany2/edge-tts): a WebSocket
to `speech.platform.bing.com` with Edge's public client token and the time-based `Sec-MS-GEC` token, MP3 24 kHz mono
48 kbit/s). No account or key is needed.

> **Caveat:** this endpoint is unofficial and undocumented. It is not licensed for use in a product. Microsoft may
> change it, stop it or block clients at any time (the token scheme has changed before). When it breaks, texts that
> are not cached yet are not spoken (an error event of type `speech` is published, nothing shows on the screen);
> cached texts keep working. Voice and endpoint are replaceable: `Speech.java` only needs MP3 bytes for a text, so
> another service (or a PC, see below) can take the place of `EdgeTts.java`.

- Cache: every rendered text is kept in the app's private storage (`files/tts/`, file name = SHA-256 of
  `v2|voice|rate|text` + `.mp3`; `v2` is the rendering version, raised when the way of rendering changes, and the files of
  an older version are deleted at the first start), written to a temporary file and renamed, so a broken render never leaves a file. The
  same text is never fetched twice and plays without a network. The least recently
  used go first (a play marks a file as used): at most `speech_cache_mb` (default and maximum 200 MB) and 2000 files.
  The cache can never fill the disk: when a new file would leave less than 300 MB free on the data partition, the
  render is played once from a temporary file and not kept (an error event of type `speech`, code `low-space`, once
  an hour at most). Statistics: `speech_cache` in `telemetry`.
- Scheduled messages that will be spoken are rendered ahead whenever the schedule or a setting changes (and at
  start), so night reminders work when the network is down. `cmd/say-prefetch` does the same for any texts.
- Playback: `MediaPlayer` (media usage, speech content) on the normal output (the USB headset when plugged in), at
  `speech_volume`, lifted by `speech_boost_db` with a `LoudnessEnhancer` on its audio session (played without it if
  the effect cannot be created). A text can be spoken several times (`repeat`) with `pause` seconds of silence between; pauses of up to 2 s
  (`DUCK_HOLD_MAX_PAUSE_S`) keep the TV lowered until the last repeat has ended; after a longer pause the TV fades back
  up (500 ms) for the pause and down again (150 ms, starting 250 ms early) so that it is low when the next repeat
  starts. One series at a time: a new one, `cmd/say-stop`, a dismissed message or a
  video call ends it at once. While it plays the TV
  (player and HDMI input) goes down to `speech_duck_percent` (8%) of its gain, fading down in 150 ms and back up in
  500 ms after the speech; the volume keys and the USB volume handling keep working. A dismissed or replaced
  message stops its speech. No speech during a video call (and a call stops it).
- Timing: the service sends audio only about 1.4 times faster than it plays, and the app plays a text when it is
  complete: a short sentence starts about 1.3 to 5 s after `cmd/say` (the very first after a start took 9 s), a
  500-character text after about 30 s (a long uncached message simply waits for the whole render; there is no
  streaming); a cached text in about 20 ms. Timeouts: 10 s to connect, 20 s without data, and 20 s plus 0.1 s per character in all.
  Texts over 2000 characters are rendered in parts (cut after a sentence or between words) that are joined.
- Language: everything is meant to be German. The multilingual voices (`...MultilingualNeural`) guess the language of
  each text and read short texts and spelled names in French or English ("Tagesschau" came out as "tipp ja" in a
  German recognizer, "A Er De" as French); this endpoint accepts only `speak`, `voice` and `prosody`, it closes the
  connection for `lang`, `say-as`, `phoneme` or even `break` ("1007 SSML is invalid"), and `xml:lang` on the voice
  changes nothing. So the default voice is the German-only `de-DE-KatjaNeural`, which reads sentences, spelled names
  and even plain abbreviations ("ARD", "HDMI", "ZDF neo") the German way (checked with the German Vosk model on the PC).
  Any other voice can be set; `xml:lang` is the voice's own locale.
- Loudness: the Edge voices come out at -20.5 to -21.8 LUFS (ffmpeg `ebur128`, true peak about -5 dBFS), 48 kbit/s
  MP3, 6000 bytes per second of speech. The TV programme ends up around -14 to -16 LUFS (broadcast -24 to -26 LUFS
  plus its +10 dB), so the default `speech_boost_db` of 10 puts the speech at about -11.5 LUFS, about 3 dB above the
  programme at the same volume (PC estimate with a limiter: +9 dB gave -12.6 LUFS, +7 dB -14.4). Both then go through
  the same volume (below), so this relation holds at every volume step. Measured on the unit (`dumpsys
  media.audio_flinger`): the Loudness Enhancer is enabled on the speech session; at volume 86% the speech track is at
  -4.2 dB like the TV, which goes down to -26 dB (8%) while the speech plays.
- Volume: the speech follows the remote's volume and mute like the TV. On a USB output the app applies the same gain
  as to the TV (`Volume.java`: the system volume index as -30 to 0 dB, 0 when the stream or master is muted), because
  this firmware leaves the music stream at 0 dB there; on other outputs the system volume does its normal work.
  Measured on the USB headset: speech track -4.2 dB at volume 86%, -21 dB at 30%, silent (-inf) when muted.
- Station names (`speak_channel`): after a channel change by a key, the channel list, a voice command or `set/channel`,
  its spoken form is said once the channel has stayed 600 ms: `spoken="..."` of its `#EXTINF` line
  (`#EXTINF:-1 spoken="Es We Er Be We",SWR BW`; `tools/make-playlist.py` writes it for all generated channels, table
  `SPOKEN`), else the name with abbreviations spelled with German letter names and digits as German words
  (`SpokenName.java`: "MDR Sachsen" is "Em De Er Sachsen", "3Sat" "Drei Sat", "KiKA" "Kika"; tested by `sh test.sh`),
  because the multilingual voices read "ARD" with English letter names. Waiting the 600 ms means zapping queues nothing;
  each change stops the name before at once, and a name still being rendered when the channel changes again is
  dropped. Not at start, not for the same channel, not for `cmd/play`, not while a message (screen or voice) or a
  video call is active, and nothing when speech is not possible. An announcement is not a message: no key is ever
  swallowed for it (Channel + just zaps on). The TV is lowered for it like for other speech. The names of the visible
  channels (50 at most) are rendered ahead when the setting is switched on, after a playlist reload and at start, so
  they play from the cache at once and offline.
- `speech=off` in `mqtt.conf` refuses `cmd/say` and `cmd/say-prefetch` and removes the Home Assistant entities; the
  setting `speech` off silences everything.
- Pre-rendering elsewhere: a PC or Home Assistant can render with `edge-tts` (or any TTS that writes MP3) and the
  file can be copied into the cache under its name (SHA-256 hex of `v2|voice|rate|text`, lowercase, `.mp3`, e.g.
  `printf '%s' 'v2|de-DE-KatjaNeural|-5%|Bettzeit' | sha256sum`) with root:
  `/data/data/org.minitv/files/tts/`, owned by the app's user. The TV then never asks the service for that text.

## Video calls (Jitsi)


The call page is Jitsi's own, shown in a WebView as Jitsi draws it: toolbar, tile view, several parties and screen
sharing work as in a browser. What is changed: the address fragment (no pre-join page, no app offer, the resolution of
the device, VP8 for the old WebView), polyfills for the old WebView, and two look rules injected into the page: Jitsi's
logo (top left) is hidden and the background image that a server may put behind the large video is replaced by a dark
grey gradient. Nothing else is hidden or rewritten, and a watchdog in the page switches both rules off for good when
videos play but none of them can be seen for 8 s, so a change of Jitsi's page cannot cost the picture. The display name
is the `device_name` setting (else `name=` of `mqtt.conf`). The microphone is used when the device lists one for apps (a
headset or a built-in microphone); a touch in a call goes to the call page.

The call screen shows no background image from the Jitsi server (rooms can be branded with one): a dark grey gradient instead, also where there is no picture.
A relative starts a [Jitsi Meet](https://jitsi.org/) call in a browser or the Jitsi app, then sends the room to the TV
(`cmd/jitsi`, or the Home Assistant text entity "Video call"). The TV stops the channel and shows the call fullscreen
inside the app; the person in front of it does nothing. Back hangs up, Right/Left (and the Volume keys) change the
volume, every other key does nothing. Afterwards the channel (or HDMI) is tuned again. A channel set over MQTT during
the call is tuned after it. A pushed message still comes up above the call.

- A room name is opened on `jitsi-server` (`mqtt.conf`, default `meet.jit.si`); a full address (`https://meet.ffmuc.net/room`)
  on the server it names, so that the server stays flexible: `jitsi-allowed=*` (the default) lets any host name through
  (no IP addresses, no `localhost`), `jitsi-allowed=meet.ffmuc.net,jitsi.hamburg.ccc.de` limits it to a list. `http`,
  user info, other ports and room names with other characters than letters, digits, `-` `_` `.` are always refused. Query and fragment of a given address are dropped. Camera and microphone are granted to
  the host of the room only. The intent that opens a call carries a random per-process token, because MainActivity is exported
  (it is the home screen) and other apps must not be able to start a call. `jitsi=off` turns the feature off.
- The call ends by itself 20 s after the others have left, 5 min after joining when nobody else came, when the page
  leaves the room, after `jitsi_max_minutes` (default 120), when it is not joined within 60 s, and on an empty or
  `hangup` payload. The WebView is removed and destroyed at every end (and when the app stops), so audio, video,
  camera and the renderer process go away.
- Green key: with the setting `jitsi_room` set, a short press joins that room (to call back). The HBG remote has no
  color keys; a remote with a green key, or a remap like the red key's, is needed.
- How it is shown: a WebView (system WebView `com.android.webview`, Chromium 83) with JavaScript, DOM storage, media
  without a gesture and a desktop Chrome user agent (on Android, Jitsi offers its app instead of the call).
  Configuration in the address fragment: no pre-join page, no deep linking, no toolbar, no notifications, no self
  view, no filmstrip, 360p, the name from `mqtt.conf` `name=`. Without the pre-join page Jitsi joins muted whatever
  the fragment says, so after joining the app switches the camera on if there is one (and the app has the
  permission) and the microphone if a USB or wired headset brings one; the remote's microphone is not usable for this.
- Chromium 83 lacks functions the current Jitsi web client uses (`String.replaceAll` among them; without it the
  client never connects). The app fetches the room page itself (`shouldInterceptRequest`, and the service worker
  client for reloads) and puts small stand-ins (`POLYFILLS` in `VideoCall.java`) in front of Jitsi's scripts. A newer
  Jitsi version may need more of them; a newer WebView (for example through a Magisk module) would remove the need.
- Volume: the remote voices play as media, so the system volume applies; on USB outputs the app sets the volume of
  the page's audio elements with the same gain curve as the player.
- `meet.jit.si` lets a room start only when a moderator has logged in (Google, GitHub, ...): the TV waits in the lobby
  until the relative has joined, and gives up after 60 s. Community servers without that rule (for example
  `jitsi.hamburg.ccc.de`, `meet.ffmuc.net`) start the room for whoever comes first.
- Permissions: `adb shell pm grant org.minitv android.permission.CAMERA` and `... RECORD_AUDIO` (or install with `-g`).
- Measured on the unit with a USB webcam and a USB headset, a 1:1 call at 1080p screen size: app process about 320 MB
  PSS plus 220 MB for the WebView renderer (free memory went from about 400 MB to 260 MB available), CPU about 140%
  renderer plus 60% app. After hanging up the renderer process is gone, the camera is released, the audio mode is
  back to normal; the app process stays about 80 MB larger than before the first call (the WebView library stays loaded).

## Known limits

- AC-3 and other formats the audio output cannot play: the picture continues without sound.
- Voice: German only; a channel name the model does not know has to be said as letters or given an alias with known
  words; a misheard word can pick a wrong channel (the shortest name containing the recognized words wins).
- On this firmware the system leaves the music stream at 0 dB on USB audio outputs. While a USB audio device is
  connected the app therefore turns the system volume index into gain of its own player (-30 dB at the lowest step,
  0 dB at the top, linear in dB) and follows `android.media.VOLUME_CHANGED_ACTION`, the mute broadcasts (mute is gain 0 there; the system's mute does nothing on USB by itself) and device changes. The HDMI input gets the same gain (`TvView.setStreamVolume`). Other apps
  still get no volume control on USB, see [USB-AUDIO.md](../USB-AUDIO.md).
- The playlist path is `/sdcard/playlist.m3u`; newer targets than API 28 would need another location.

## Voice with a wake word

A wake word service on the device can start `VoiceActivity` with the extra `pcm`, the path of a 16 kHz mono PCM16 file, instead of having it record. Used on the Echo Show, where this is the only way to hear what was said right after the word. Without the extra, nothing changes. The file is deleted when recognition is done, which tells the service to unmute. With the extra, "Beenden" and "Ausschalten" go to the launcher, a failure gives a low tone, and the file `/sdcard/voice-apps` (one `phrase|package` per line) adds commands that open an app. "Lauter" and "Leiser" move the media volume by one of seven levels. On a quit word, the file `wake.quit` next to the capture asks the service to stop those apps. The file `/sdcard/voice-foreground` makes `VoiceService` a foreground service, which keeps the speech model loaded.
