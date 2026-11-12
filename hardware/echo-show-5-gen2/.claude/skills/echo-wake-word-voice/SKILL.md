---
name: echo-wake-word-voice
description: Use when working on the microphones, wake word or voice control of a rooted Echo Show (cronos, Fire OS 6, MT8163), or of a similar Echo running libpryon. Covers raw 4-channel mic capture when tinycap cannot do S24_3LE, why logcat and broadcasts show nothing for the stock detector, running Amazon's libpryon in our own binary with no NDK, getting the processed mic stream as root through app_process, the SELinux domain that may record, handing captured audio to a recognizer, muting the amp through tinymix, and why speech recognition fails with the device's own speaker on or with the wake word left in the audio.
---

# Wake word and voice on a rooted Echo Show

Full facts and measurements: `MIC-AND-HOTWORD.md`. Install: `scripts/install-echowake.sh`, then reboot. Remove nothing Amazon from `/system` while Paladin is home: it uses `amazon.speech.sim` permissions.

## Architecture that works

`AudioRecord VOICE_RECOGNITION` -> `MicPipe` (root Java) -> `pryonwake` (libpryon) -> `echowake.sh` -> `VoiceActivity` with `--es pcm` -> Vosk channel grammar.

On the word: beep, 0.25 s later amp to 0, capture until 0.7 s of silence (7 s at most), recognition, then minitv deletes the capture, which unmutes the amp (7 s at most). Word to channel 4-5 s. The person must wait for the beep.

## Raw capture

- `hw:0,22` is 4 channels S24_3LE 16 kHz: ch0/1 mics (correlation 0.93-0.96, never identical), ch2/3 playback loopback.
- `audioserver` holds it: `stop audioserver`, capture, `start audioserver`, then restart minitv.
- `tools/echocap.c`: no libc, talks ALSA ioctls. tinycap `-b 24` fails on this build.
- The speaker makes no sound from raw `tinyplay` with the HAL stopped, so a tone test cannot prove the mics. Use a real sound in the room.
- Mixer order against PCM open did not matter here, whatever other projects report.
- Mute: `/sys/devices/platform/gpio-privacy/state`, 1 = muted, both mics exact zero while Amazon's recordings stay open.

## Pryon outside the stock app

- Stock detection logs nothing and sends no broadcast. Do not spend time on logcat hooks.
- `tools/pryonwake.c` declares what it uses. Sequence from echo-dot-assist `wake_pryon.c`: logging and result callbacks, `PryonModelSet_New`, default format, `PryonDecoder_NewSpotterAudioDecoder`, then push samples at real time.
- No NDK needed: link with lld against the device's own `libc.so`, `libpryon.so` etc., `-pie --dynamic-linker=/system/bin/linker --hash-style=both`, and call `__libc_init` from a small `_start`.
- The keyword pointer dies after the callback: copy it. `STOP` is reported too, once awake. The `end` field gave garbage; `begin` works.
- Log levels 4 and 5 are hundreds of lines per model load.
- Words: ALEXA, AMAZON, COMPUTER, ECHO under `/system/local/models/keyword/<locale>/`. Custom words: impossible.

## Getting audio as root

- `app_process` without `ActivityThread.systemMain()` segfaults in `AudioRecord` (null package name reaches `String16`).
- `AudioRecord` fails to initialize in the `adbd` domain and works in `u:r:su:s0`: `runcon` in the service script.
- A third recording next to Amazon's two works.
- A fourth does not: reads return 0. `stop echowake` before any test recording. Sources 6 and 1999 are processed (TV about 25 dB down), 1 and 9 are not, 2999 is held by Amazon. `MicPipe - <source>` selects one.
- After a mute, reopen the recording when blocks stay exact zero.
- A live Java helper can beep at once (`ToneGenerator`), asked through a pipe made with `mknod <path> p`; there is no `mkfifo`. A new process would take a second.

## Do not retry

Stay on source 6, MICPGA 40, fixed gain about 6x. Source 9 is not louder; MICPGA 60 raises noise more than speech; peak normalizing makes Vosk invent words. Signal over noise is about 20 dB at normal distance. Replay `/sdcard/voice-debug` recordings on the host to compare anything: no device change, no one has to repeat.

## Recognition pitfalls, each found the hard way

- Starting the activity on detection: 2.4 s later, the speech is over. Capture in the process that already has the stream.
- The word is reported about 0.9 s after it starts.
- minitv's own speaker garbles speech despite AEC: mute the amp at `Digital Volume A`, not the media volume, and restore it even on failure.
- More than 0.2 s of reach-back keeps "Alexa" in the audio; Vosk turns it into the letter X and channel matching breaks.
- A Vosk grammar is a loop over its entries: phrases cannot forbid "berlin das erste". Handle that in the matcher.
- Grammar-only Vosk matches noise, so it cannot be a hotword: 88% of a core, and it fired on room sound.
- The `:voice` process is killed when idle: foreground service, enabled by `/sdcard/voice-foreground`.
- Shared storage hides root-owned files from the app: `chown 1023:1023` under `/data/media/0`.
- Logcat rotates in minutes here: stream `logcat -s minitv echowake` to a host file before a test series.
- A log can be right while the screen is wrong: `:voice` is a separate process, so static state set in the main process is missing there. `Playlist.init` was, and every channel landed one too high. Tune by intent and take a screenshot.
- Ask what the person said, then compare with the log. "Nothing happened" may be a switch to the channel already on.
- A second adb device breaks `adb get-serialno`: set `ANDROID_SERIAL`.
- Do not run `verify-device.sh` during a voice test: its load can cost a detection.
