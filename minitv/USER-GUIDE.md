# TV - user guide

Turns the projector into a simple TV set. It shows live channels full screen. The remote does four things:
channel up, channel down, volume up, volume down. There are no menus and no text on screen.

You need: the projector on your WLAN with internet, and a PC on the same network with `adb` installed.

## 1. Install

On the projector, turn on developer options and network debugging (Settings, About, tap Build number 7 times;
then Developer options, USB debugging). Then, on the PC:

```sh
adb connect <projector-ip>:5555
adb install -r -g minitv.apk
adb shell pm disable-user --user 0 com.google.android.katniss
adb shell pm disable-user --user 0 com.softwinner.awsource
adb shell pm disable-user --user 0 com.softwinner.awlivetv
```

The second command lets the remote's voice button reach the TV instead of Google's voice app (section 4). The app is
about 52 MB because it contains the speech recognition.

## 2. Get channels

The channel list is a playlist file in M3U format. You can write your own: one stream address per line.

```
#EXTM3U
#EXTINF:-1,Channel one
https://example.com/channel1/master.m3u8
#EXTINF:-1,Channel two
https://example.com/channel2/master.m3u8
```

Or create a list of the German public broadcasters (43 channels) with the included script (Python 3):

```sh
python3 tools/make-playlist.py playlist.m3u
```

Copy the list to the projector and start the TV:

```sh
adb push playlist.m3u /sdcard/playlist.m3u
adb shell am start -n org.minitv/.MainActivity
```

The list is read when the TV starts. After changing it:

```sh
adb push playlist.m3u /sdcard/playlist.m3u
adb shell am force-stop org.minitv
```

then press Home on the remote. Some streams only work in their home country.

## 3. Use it

| Remote key | What it does |
|---|---|
| OK | list of all channels (section 5) |
| Channel + (or Up, Page up) | next channel |
| Channel - (or Down, Page down) | previous channel |
| Volume + / - (or Right / Left) | louder / quieter |

On a USB speaker or headset the TV applies the volume itself, because this firmware does not. All other keys do nothing. The last channel is remembered. A channel that fails or ends is tried again every
3 seconds. After a channel change the screen is black until the picture arrives.

## 4. Voice: say the channel

Press and hold the voice button on the remote, say the channel, let go. About 3 seconds later the TV switches.

- Say the name as in the list: "ZDF", "ARD Alpha", "Arte", "Phoenix", "3Sat" (say "drei sat"), "MDR Sachsen".
- The generated list also knows other names: "Das Erste", "Hessen", "Baden-Württemberg", "Deutsche Welle",
  "Kinderkanal", "Rheinland-Pfalz", "Berlin" and so on. To add your own, edit the playlist: write
  `aliases="Name one;Name two"` after `#EXTINF:-1` on the channel's line, for example
  `#EXTINF:-1 aliases="Hessen;Hessischer Rundfunk",HR`. Copy the list to the projector again and restart the TV.
- Letters work too: say "M D R" or "R B B Brandenburg" letter by letter.
- Hold the button only while speaking. Speak normally, close to the remote.

Everything is recognized on the projector itself. Nothing is sent to Google or anywhere else, and nothing is
recorded. The first voice command after switching on can take a few seconds longer while the recognition loads
(about 10 seconds after the picture appears).

The first channel, `HDMI`, shows what is plugged into the HDMI socket (a computer, for example); say "HDMI",
"Eingang" or "Computer". The generated list hides the regional variants of the same broadcaster (one stays, SWR
keeps both). To bring one back, remove `hidden="yes"` from its line. A channel that does not come up is tried again and
again by itself; only after 20 seconds without a picture (setting `no_picture_seconds`) it shows a plain red screen,
and a plain yellow screen after twice that means the internet is down. The colour goes as soon as the picture comes. Switch channels as usual.

## 5. Channel list and programme guide

A short press of OK opens the list of all channels. Move with Up/Down (or Channel +/-), press OK to switch to the
selected channel, or Back to close the list (it also closes by itself after 15 seconds). The selected channel shows
what is on now. After every channel change the time, title and subtitle show for 6 seconds.

The programme texts are optional. They are fetched from the public Zapp server of the MediathekView project for the
channels whose line in the playlist has `zapp-id="..."` (the generated list has it for 27 of 43 channels: the ARD
family, ZDF, 3sat, KiKA, phoenix, ARTE and the regional programs). Only the id of that channel is sent. Without
`zapp-id`, or when the server or the internet is not available, the texts are simply not shown; the list and
switching work the same. 
To turn the whole feature off (OK then does nothing and nothing is ever fetched), make the first line of the playlist
`#EXTM3U guide="off"`, or create the playlist with `python3 tools/make-playlist.py --no-guide playlist.m3u`.

## Remote control from home automation (optional)

The TV can be switched, set louder or quieter and watched over MQTT (online or offline, channel, picture, how the
last shutdown happened). Copy `mqtt.conf.example` to `/sdcard/mqtt.conf`, fill in your broker (HiveMQ Cloud, EMQX
Cloud, your own with TLS) and restart the TV. Without that file nothing of this exists. Messages can be pushed to the screen too: the picture is dimmed and the text shown in the middle until any key is pressed. Details and topics: README.

Video calls: a relative opens a Jitsi room (for example on `jitsi.hamburg.ccc.de`; set `jitsi-server=` in
`mqtt.conf`, default `meet.jit.si`, where the relative has to log in first) and sends the room name to the TV over
MQTT (`cmd/jitsi`). The call opens full screen on the TV by itself. Back hangs up, Left/Right change the volume,
nothing else does anything; then the TV continues. A USB webcam and a USB headset with microphone are used when
plugged in (grant them once: `adb shell pm grant org.minitv android.permission.CAMERA` and `... RECORD_AUDIO`). With
the setting `jitsi_room` set, the green key of a remote that has one calls that room. Details: README.

Spoken text: the TV can say a text over MQTT (`cmd/say`, or the Home Assistant text "Say"), and show a pushed or
scheduled message on the screen, say it, or both (setting `message_mode`, or `"mode"` in the message: `text`,
`speech`, `both`; `show_seconds` for how many seconds the text stays after it has been spoken). Any key on the remote ends a message, also one that is
only spoken: that is how you confirm you heard it. If the voice cannot be used (no internet, speech off, a video call)
the text is shown instead. With the setting `speak_channel` on, the TV says the station name after each channel
change. It spells abbreviations the German way ("A Er De"); to change how a channel is said, write
`spoken="..."` in its line of the playlist, for example `#EXTINF:-1 spoken="Es We Er Be We",SWR BW`. The voice comes from an unofficial Microsoft Edge online service, so a new text needs the internet once;
every text is kept on the projector afterwards and then works without internet. Scheduled messages that are spoken
are fetched ahead. The TV sound is lowered while it speaks; there is no speech during a video call. The speech follows
the remote's volume and mute, a little louder than the programme; `speech_volume` and `speech_boost_db` adjust it. A
text can be repeated (`repeat`, `pause`, or the settings `speech_repeat`, `speech_pause`). Turn it off with
the setting `speech` off. Details: README, "Speech".

## 6. Make it the home screen (optional)

The projector then starts straight into the TV picture, and the Home key always returns to it.

```sh
adb shell cmd role add-role-holder android.app.role.HOME org.minitv
```

## 7. Leaving the TV

The TV swallows nearly every key, so these are the ways out:

| What you do | What happens |
|---|---|
| Hold Back, Menu or OK for 3 seconds | a list of the other home apps opens; choose one with Up/Down and OK to start it (the TV stays the default); Back closes the list |
| Press Home 3 times within 3 seconds | another launcher opens (the TV stays the default) |
| Delete the playlist: `adb shell rm /sdcard/playlist.m3u` | the TV hands over to another launcher |
| Set another launcher from the PC | `adb shell cmd role add-role-holder android.app.role.HOME com.spocky.projengmenu` |

Use `com.htc.spectraos` instead of `com.spocky.projengmenu` for the original launcher.

## 8. Problems

| Problem | Cause and fix |
|---|---|
| Opens another launcher instead of the TV | the playlist is missing or empty: copy it to `/sdcard/playlist.m3u` |
| Black screen | the stream is down, geo-blocked, or the address is wrong; try another channel or check the address |
| Picture but no sound | the sound format (for example AC-3) cannot be played here; try another channel |
| Very quiet or silent on a USB speaker or headset | the TV scales USB volume itself, from -30 dB at the lowest step to full volume at the top; turn the volume up (the first steps are very quiet) |
| Quieter than other apps | TV broadcasts are mastered quieter than, for example, YouTube; the TV already adds 10 dB, turn the volume up for more |
| The voice button opens Google's voice search or asks which app to use | the voice app was not disabled: run `adb shell pm disable-user --user 0 com.google.android.katniss` |
| The voice button does nothing or picks the wrong channel | the first command after switching on can take longer; say the name as in the list, or add an alias (section 4); check the app has the microphone permission (`adb install -r -g`) |
| OK shows nothing | the channel has no `zapp-id` in the playlist, or there is no internet; ids: `https://api.zapp.mediathekview.de/v1/channelInfoList` |
| Asks for storage permission | the app was installed without `-g`; allow it, or reinstall with `adb install -r -g minitv.apk` |

## 9. Remove

```sh
adb uninstall org.minitv
```

If it was the home screen, pick a launcher first (section 7).
