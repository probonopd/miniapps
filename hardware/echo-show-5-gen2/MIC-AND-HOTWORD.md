# Microphones and hotword detection (cronos)

Everything needed to set our own hotword or use our own speech model later.
State as of 2026-10-31, Fire OS 6.5.4.8 (NS6548/3290, incremental 0005805759108).

Evidence labels used below:

- **[measured]** tested on this unit.
- **[static]** read from files on this unit, not run.
- **[reported]** from another project, on another build or model. Check before relying on it.
- **[open]** not known yet.

---

## 0. How to use it, and where things stand

Say "Alexa", wait for the beep, say a channel name. The TV goes quiet and the
channel switches about 4-6 s after the word. The sound returns as soon as the command is done.
A low double tone means nothing was understood, and the channel stays. "Beenden" or
"Ausschalten" goes back to the launcher and stops the radio. "Lauter" and "Leiser" change the
volume. "Radio" opens the radio app.

Speak close enough and clearly: that decides more than any setting. Leave a short pause
after the beep; a single name works better than a list.

Settled by test, do not retry without a new reason:

| Question | Answer | Section |
|---|---|---|
| Stock Alexa detector usable? | No trace in logs or broadcasts | 5b |
| Our Vosk listener as hotword? | Too heavy, 88% of a core, fires on noise | 5b |
| Amazon's engine in our binary? | Yes, 25% of a core, 3 of 3 words | 5b |
| Hex (anoma.ly)? | Desktop dictation, not a candidate | 5b |
| Recording source? | 6: echo-cancelled and beamformed | 5c |
| Source 9 while muted? | Not louder, no gain | 5b |
| More mic gain (MICPGA 60)? | Noise rises with speech, no gain | 5b |
| Fixed boost? | Yes, +15 dB helps | 5b |
| Normalizing? | No, worse | 5b |

Still open: a router capture during a WLAN reboot, hit rate over many tries, another speech
model (`vosk-model-small-de-zamia` is a candidate, the current one lacks some words), and
"Computer" as the word.

---

## 1. Summary

- Two MEMS mics [reported], `Num Mics: 2` in `AFE.cfg` [static]. No hardware
  wake word: detection runs on the CPU.
- One ALSA PCM, `hw:0,22`, 4 channels, S24_3LE, 16 kHz, held open permanently by
  `audioserver` [measured]. Channels 2 and 3 are the playback loopback [measured].
- Inside `audioserver`, Amazon's `libasp.so` does echo cancellation and
  beamforming. `amazon.speech.sim` reads the result as mono 16 kHz PCM through
  normal `AudioRecord` and feeds `libpryon.so` [measured/static].
- Four stock words: Alexa, Amazon, Computer, Echo, in 19 locales [static]. No
  Ziggy. German has all four. Switching among them is possible; a new word is not.
- **Hands-free voice works:** "Alexa, das Erste" switches the channel, with Amazon's
  engine in our own daemon and minitv's Vosk for the channel (section 5b).
- A custom word would need another engine (section 9). Amazon's stays for now: it
  uses a quarter of the CPU of our Vosk listener.
- Raw capture works, with two separate live mic signals [measured, section 5].
- A third `AudioRecord` client works next to Amazon's two recordings, end to end
  with Vosk [measured, section 5a]. The mute button zeroes the mic PCM [measured].

---

## 2. Hardware

| Part | Detail | Evidence |
|---|---|---|
| Mics | 2 MEMS, MK1/MK2 on the top button PCB; spacing unpublished | [reported], teardown |
| ADC | TI TLV320AIC3101 stereo codec, I2C `0-0018` | [measured] |
| Packing | FPGA on SPI, driver `amzn_mt_spi_pcm`, packs 2 mics + 2 loopback into one stream | [reported] |
| Amp | Maxim MAX98396, I2C `2-003d` | [measured] |
| SoC | MT8163. No voice wake-up DSP known; `/dev/snd/adsp` is the OSS node | [reported], [static] |
| Mute | `gpio-privacy` platform device | [measured] |
| Calibration | per-unit values in `/proc/idme/miccal.0` and `.1`, `.2` and `.3` = 0 | [measured] |

A teardown names TLV320ADC3101 and TAS5805M instead; this unit reports AIC3101
and MAX98396. `audio_device_tas5805m.xml` exists, so other revisions are likely.

### Mute

- Input devices `gpio-privacy-state` (`/dev/input/event0`) and
  `gpio-privacy-button` (`event1`) [measured].
- `/sys/devices/platform/gpio-privacy/state` read 0 with mics on [measured].
- [reported] state: 1 = cut, GPIO 405. Button GPIO 404. `enable` (GPIO 384):
  writing 1 pulses for 1000 ms and cuts the mics. Software cannot release the
  latch, only the button can, and capture stays silent until the path reopens.
- Reading `enable` gives "Permission denied" [measured]; it is write-only.
- Muted with an outside sound playing: `state` = 1, ch0 and ch1 100% exact zero,
  8 s [measured]. Unmuted the same sound gave -48 dBFS. So the cut happens before
  the PCM. `audioserver` and Amazon's two recordings stay open while muted [measured].
- Unmuted: capture on a newly opened PCM returned to -46 dBFS, correlation 0.965
  [measured]. Whether a stream left open across unmute recovers by itself
  is [open]; TECHO5 reports that it stays silent until reopened.

---

## 3. ALSA

Card 0 `mt-snd-card`. PCM 22 is `TLV320AIC3101 Capture`, PCM 23 is `MAX98396_Playback`.

Stock stream, from `/proc/asound/card0/pcm22c/sub0/hw_params` [measured]:

| Parameter | Value |
|---|---|
| format | S24_3LE, interleaved |
| channels | 4 |
| rate | 16000 |
| period / buffer | 257 / 2570 frames |
| owner | `audioserver` (`pcmC0D22c`, `pcmC0D23p`, `controlC0`) |

Channels:

| Ch | Content | Evidence |
|---|---|---|
| 0 | mic, about -48 dBFS with sound in the room | [measured] |
| 1 | mic, correlation with ch0 0.93-0.95 with sound, lag 0 | [measured] |
| 2 | playback loopback L, exact zero when idle | [measured] |
| 3 | playback loopback R, identical to ch2 | [measured] |

Mixer [measured]:

- `ADC_A Left Ip Select ADC_A DIF1_L switch` and the Right equivalent: On.
  `ADC_A MICPGA Volume Ctrl` 40, `ADC_A Digital Volume Control` 88.
  `Speaker Volume A` 8, `Digital Volume A` 127.
- The MT8163 internal `Audio_ADC_*` and `Audio_MicSource*` controls are off and
  not in the path, consistent with the FPGA route.
- `/vendor/etc/audio_device.xml` holds MediaTek's generic paths, mostly unused.

[reported]:

- The DT property `amzn,mic-downmix` makes the driver average ch0 and ch1. With
  it removed (`tools/linux/patch-dtb.py` in TECHO5, a boot.img DTB patch), the mics
  differ. Here ch0 and ch1 never match sample for sample (0.0% equal) with
  correlation 0.93-0.95 [measured], the same figure TECHO5 gives for separate mics.
  So this unit does not output a plain copy, without any DTB patch.
- Mixer settings must be applied after the PCM opens. Here the order made no
  difference [measured].
- The driver rejects 256x10 and accepts 320x8.
- `pcmC0D1c` must be held open, unconfigured, before opening `pcmC0D23p`.
  Done in our tests.

---

## 4. Software path

```
mics + loopback -> pcmC0D22c (4 ch, S24_3LE, 16 kHz)
  -> audio.primary.mt8163.so in audioserver
     -> libasp.so (service "audiosignalprocessor"), AFE.cfg
        AEC, beamforming, beam selection, AGC
  -> AudioFlinger AudioIn threads, 16 kHz mono PCM16, 320-frame HAL buffers
     -> source 6 (VOICE_RECOGNITION)
     -> source 2999 (Amazon private)
  -> amazon.speech.sim (AudioRecord, pid owner of both tracks)
     -> libwakewordserver_jni.so -> libpryon.so
```

Two input threads run all the time, both owned by `amazon.speech.sim`, one with
source 6 and one with source 2999 [measured]. See section 5c for what each one is.
No sound-trigger HAL exists, although `media.sound_trigger_hw` is registered
(no `sound_trigger.*` module in `/system/lib/hw` or `/vendor/lib/hw`) [static].

### Consequences

- **Privacy:** the mic path is always running, even with the network blocked.
  Audio stays local while the firewall holds. The hardware mute is the real off
  switch.
- **Cost:** with wake word active and idle, `amazon.speech.sim` 11% and
  `audioserver` 9% of total CPU in one `top` sample [measured]. Total system user
  28%, system 8%.
- **For us:** an app can use the same processed stream through `AudioRecord`
  with `VOICE_RECOGNITION`, alongside Amazon's two recordings [measured, 5a].

### libasp.so and AFE.cfg

`/system/lib/libasp.so`, `/vendor/etc/audio-algorithms/AFE.cfg` (54 kB JSON with
comments) [static]:

- `Num Mics` 2, mic and ASR path 16 kHz, speaker 48 kHz, 2 references per input.
- Processing chain for ASR: Downsampler, HPF 80 Hz, AEC, SerialARA BeamMerger, Mic
  Delay, Adaptive BeamFormer, SNR BeamSelector. FBF always on; AEC or ABF chosen by
  reference level.
- Separate VoIP chain with VAEC.
- Exports: `asp_init`, `asp_parameterized_init`, `asp_create_pipeline`,
  `asp_process`, `asp_process_ext`, `asp_process_3p`, `asp_set_device`,
  `asp_command`, `asp_set_event_callback`, `asp_set_ext_ref_sync_status`,
  `asp_destroy_pipeline`.
- Links `libmedia`, `libbinder`, `libspeexresampler`.

Pryon-AFE-Runtime wraps this for an Echo Dot 2 with 9 channels and 128-frame quanta
[reported]; on cronos the channel count differs.

---

## 5. Raw capture test results

Tool: `tools/echocap.c`, a freestanding ARMv7 binary, because stock `tinycap` fails
with "cannot set hw params" for `-b 24` (it cannot request S24_3LE on this build).

| Test | ch0/1 rms | ch2/3 rms | Result |
|---|---|---|---|
| Quiet room, 6 s | -72.9 dBFS | silent, 100% zero | hum at about 50 Hz only |
| 1 kHz + sweep, -26 dBFS loopback | -72.9 / -72.7 | -26.4, top 1 kHz | no tone on mics |
| Sweep, -14 dBFS loopback, `Speaker Volume A` 30 requested | -73.3 / -72.8 | -14.0 | no tone on mics |

With sound in the room (source outside the device), 2026-10-31:

| Test | ch0 rms | ch1 rms | Correlation | Peak |
|---|---|---|---|---|
| 10 s, mixer set after PCM open | -48.1 dBFS | -48.3 dBFS | 0.950 | -31.6 dBFS, spectrum top 104 Hz |
| 6 s, mixer set before PCM open | -49.1 dBFS | -49.4 dBFS | 0.934 | -32.2 dBFS, spectrum top 106 Hz |

Per-second level moved between -45 and -53 dBFS with the sound.

Conclusions:

- Capture, both mics and the loopback work, about 25 dB above the quiet floor.
- The mixer order does not matter.
- The sweeps never reached the mics because the speaker makes no sound with raw
  `tinyplay` while the HAL is stopped [inferred: the loopback had the signal and the
  mics did not, and the mics do work]. Raw playback needs the amp bring-up that the HAL
  does. Not yet solved; AEC tests with raw playback depend on it.

With a second adb device attached, set `ANDROID_SERIAL=<serial>`.

`audioserver` must be stopped for this; it costs a few seconds of sound and wake
word. `start audioserver` restored everything in each run.

---

## 5a. Third AudioRecord test with minitv

`am start -a android.speech.action.WEB_SEARCH -n org.minitv/.VoiceActivity`, with
Amazon's apps running and an outside sound in the room, 2026-10-31 [measured]:

- `dumpsys media.audio_flinger` shows 3 input threads while recording: Amazon's source 6
  and 2999, plus minitv's source 6. Back to 2 afterwards.
- `voice: recorded 8000 ms`, the 8 s maximum. minitv stops after 0.3 s below RMS 20 of
  32768 (about -64 dBFS), so the stream carried real audio. An exact level was not
  logged.
- `voice: heard "sachsen"`, `channel 9 MDR Sachsen`, tuned, first frame after 1 s.
  This was a false match on room sound: the grammar forces a channel word.
- Vosk: model load 23.5 s (background preload, first run), result 4.9 s after the
  recording, which may include waiting for the load. With the model loaded and a short
  capture it is about 1 s (section 5b).

Findings for the hotword design:

- A hotword must not rely on a forced grammar: Vosk with channel names matches noise.
- An always-listening Vosk client costs 88% of a core: section 5b.

minitv bug on Android 7: `copyAsset` got an empty `assets.list("model")`, treated the
folder as a file, and failed with "Is a directory". `scripts/install-minitv.sh` now
unpacks the model into `files/model` and creates `model.ready`. minitv itself is not
fixed. The manual step is what was tested; the script itself is checked only for syntax.

---

## 5b. Hands-free voice, as installed

`scripts/install-echowake.sh` installs this; files in `echowake/` and `tools/`.

```
AudioRecord VOICE_RECOGNITION (Amazon's AEC and beamforming)
  -> MicPipe (root Java, app_process, su domain)
  -> pryonwake (libpryon, /system/local/models/keyword/de-DE/ALEXA)
       on the word: beep, amp muted, 0.2 s before + up to 7 s after saved to wake.pcm,
       ending after 0.7 s of silence
  -> echowake.sh -> am start VoiceActivity --es pcm /sdcard/wake.pcm
  -> Vosk with the channel grammar -> channel switch, amp restored
```

Measured:

| | Vosk listener (prototype, removed) | Pryon |
|---|---|---|
| CPU, one core | 88% | 25% |
| Memory | 197 MB | 27 MB + 86 MB mic helper |
| "Alexa" 3 times, "Computer" once | - | 3 of 3, Computer ignored |

- The beep comes from `ToneGenerator` in MicPipe, on request through a named pipe, then
  the amp is muted until minitv deletes the capture, at most 7 s (`QUIET_S`). Say the word, wait for the beep, say the channel.
- Word reported about 0.9 s after it started (14240-14720 samples), after the word has ended. Only 0.2 s
  is kept from before it: with more, Vosk hears "Alexa" as the letter X, which spoils
  channel matching. So the channel name has to come after the beep.
- With the capture ending on silence and the model loaded, recognition takes about 1 s. First load of the model takes 15-50 s.
- Series of 4 with minitv playing, 2026-10-31, as spoken after the beep: ZDF, Das Erste,
  ZDF, Arte. All 4 correct, Alexa detected 4 of 4, 3.9-5.1 s from the word to the channel,
  recognition 0.8-1.2 s. `wlan-logs/voice-series.log` has the log. Small sample: [open] over
  more tries, other channels and rooms. One "Alexa" was missed earlier during heavy adb load.
- Say the word, wait for the beep, say the channel. Without a pause the name is lost.

- Off-by-one, fixed 2026-10-31: `VoiceActivity` runs in its own process and never called
  `Playlist.init`, so it kept the HDMI channel that the player drops on a device without
  TV inputs. Every voice channel landed one too high, while logs showed the right name.
  Check the screen, not only the log.
- After the fix the owner confirmed correct switching on screen.
- A word heard extra ("o berlin das erste") matches nothing, and the channel stays.
  `ChannelMatcher` needs all heard words in one title.

Accuracy measures in place (2026-10-31, effect not yet measured over a series):

- Grammar: whole channel phrases plus `spoken` forms and words of 4 letters or more.
  A Vosk grammar loops, so this cannot forbid "berlin das erste"; it removes single letters.
- Words under 0.7 confidence are dropped, so a doubtful result does nothing.
- `ChannelMatcher` falls back to the title with most words said, later words winning.
- `pryonwake` raises the capture by about 15 dB, Pryon itself gets the original.
- A low double tone reports a failed hands-free command, once the speaker is back.
- Commands that open an app are listed in `echowake/voice-apps`, one `phrase|package` per line, on
  the device as `/sdcard/voice-apps`. A phrase must use words the model knows: `radiodroid` is not one,
  so that phrase is written "radio droide".
- "Lauter" and "Leiser" move the media volume by one of 7 even levels (0-30 on this device).
- "Beenden" and "Ausschalten" both go to the launcher and stop every app in that list, through
  the marker `wake.quit`: going home alone would leave a radio playing in the background.
  Phrases match whole words only. Hands-free only: other devices have no such words, and a launcher in front just stays.

Gain test, 2026-10-31: the last 10 commands replayed through Vosk on the host, without
boost, with the fixed 15 dB, and normalized to -3 dBFS peak.

| Result | Without boost | Fixed 15 dB | Normalized |
|---|---|---|---|
| Correct, usable | 2 (`zdf`, `drei sat`) | 4 (adds `das erste`, weak `phönix`) | 3 (loses `das erste`) |
| Wrong words | 1 | 0 | 2 (`i sat`, a mix for noise) |

- Fixed gain stays. Normalizing does not help: it lifts noise too.
- Clear speech peaks near -8 dBFS after the boost, the failures near -23 dBFS, 15 dB less,
  so level or distance decides. Five recordings gave nothing, even with the free vocabulary.
- Not in the model's vocabulary: bw, dreisat, dw, eff, kinderkanal, mdr, rbb, rp,
  südwestrundfunk, zet. Those phrases lose these words; letter spellings and `zdf` remain.
- Small sample, and which recording belongs to which spoken word is inferred from the order.

Mic tests with the speaker muted, 2026-10-31, "ZDF, Das Erste, Arte" at normal distance:

| Setting | Speech | Noise floor | Signal over noise |
|---|---|---|---|
| Source 6, MICPGA 40 (current) | -53 to -57 | -74 to -76 | 18-22 dB |
| Source 9, MICPGA 40 | -55 to -56 | -77 | 21-22 dB |
| Source 6, MICPGA 60 | -48 to -49 | -66 | 17-18 dB |

- Source 9 is not louder and has no echo cancellation for the wake word. Stay on 6.
- MICPGA 60 lifts speech 8 dB and noise 10 dB: no gain in quality, recognition no better.
  It stays at 40. Level alone does not decide: 20 dB over the noise is what this path gives.
- A word heard from `phönix` at 0.50 is dropped by the 0.7 cutoff. Not changed: "ard" at
  0.44 and "süd" at 0.50 were rightly dropped in the same series.

What failed on the way, so it is not retried:

- Mic hearing the TV: with minitv's own sound on, speech came out garbled. Hence the amp mute.
- Reaching back 1-2 s for audio: "Alexa" came out as the letter X and spoiled the match.

- Stock detector: 40,000 log lines around a spoken "Alexa" showed nothing, and no
  broadcast. With no account it may not run at all [open].
- Starting `VoiceActivity` on detection: 2.4 s to start, so the speech was already over.
- `adbd` SELinux domain: `AudioRecord` fails there; `su` works (`runcon` in the script).
- Plain `app_process`: `AudioRecord` crashes on a null package name without
  `ActivityThread.systemMain()`.
- The engine frees the keyword after its callback: copy it. `STOP` is reported too.
- No `mkfifo` on the device: `mknod <path> p`.
- Hex (anoma.ly): desktop dictation, no hotword, no ARMv7 or Android. Not a candidate.

To change the word: `scripts/install-echowake.sh <manifest>`, for example
`/system/local/models/keyword/de-DE/COMPUTER/pryon.manifest`, then reboot. Not tested.

---

## 5c. Which recording source is processed

Test 2026-10-31 [measured]: minitv playing, nobody speaking, `tools/micpipe` with a source
argument, 3 rounds of 5 s each, one source at a time:

| Source | Level, dBFS | Reading |
|---|---|---|
| 6 VOICE_RECOGNITION (ours) | -69, -64, -59 | TV mostly removed |
| 1999 HOTWORD | -66, -69, -63 | TV mostly removed |
| 1 MIC | -48, -32, -35 | TV present |
| 9 UNPROCESSED | -30, -54, -36 | TV present |
| 2999 | no audio | held by `amazon.speech.sim` |

TV content varied between rounds, so these are about 25 dB in rough terms, not an exact
figure. It shows TV sound reduced by processing, and speech surviving it is shown by the
voice tests, not by this table.

echo-dot-assist `docs/re-checkers.md` [reported, static, same build family]: the HAL's
`getAspPipelineType` picks the `libasp` pipeline by source. 6 and 1999 use the ASR pipeline
(AEC and beamforming, mono 16 kHz), 7 Voice, 2999 SwwRef for the playback decoder, which stops
the wake word firing on Alexa's own speech, and 1 an undocumented MIC pipeline. Our measurement
agrees. It also names 1999 and 2999 as Amazon's sources, while `dumpsys` shows 6 and 2999 here.

Stay on source 6. Hotword and channel name share this stream.

Findings from a SIP test call, 2026-11-10, reported by the video call session [reported, not
re-measured here]:

- Source 6 at 16 kHz carries speech (about -53 dBFS); the device's own playback is cancelled
  almost completely: a 440 Hz tone played at -15 dBFS came back below -84 dBFS.
- Source 7 (VOICE_COMMUNICATION) and source 6 at 48 kHz gave only silence. Use 16 kHz.
- Playback on STREAM_VOICE_CALL with MODE_IN_COMMUNICATION is silent, although AudioFlinger
  routes it to the speaker; STREAM_MUSIC in MODE_NORMAL is audible.
- Static musl binaries under an app UID find no network interfaces: SELinux denies
  SIOCGIFNAME (if_indextoname) to untrusted_app.
- During a call the hotword steps aside by itself: while `/data/media/0/minicall/in-call` exists and
  the call app runs, `echowake.sh` waits; when the file appears, it stops its mic helper and
  `pryonwake` (which does not exit at the end of its input), restores the amp and exits, and
  init restarts it into the wait. A stale file of a dead app does not block it.

Concurrency [measured]: one extra recording fits next to Amazon's two; a fourth gets reads
of 0. `am force-stop amazon.speech.sim` does not free them: it is persistent. So a test
recording needs `stop echowake` first.

---

## 5d. CPU and power

60 s per process with minitv playing, 2026-10-31, share of all 4 cores, 77% busy in total,
cores at 1.3 GHz, CPU 76 C: `com.amazon.knight.hds` 18.5, minitv 12.9, audioserver 12.9,
`amazon.speech.sim` 9, surfaceflinger 6, pryonwake 5.2, mediacodec 3.4, cameraserver 1.2.
No app restarts in that minute.

`com.amazon.knight.hds` is the Human Detection Service: it kept the camera open and ran
`mobilenetv3_v4_40k_320_320_rc8.tflite` on the frames. The APK also has `ACTION_PRESENCE_DETECTED`,
motion, face, gesture and Guard code. Amazon documents camera presence detection for this model,
for Adaptive Content and occupancy Routines, on-device with a presence signal sent to the
cloud [reported]. Sources: Amazon help pages, https://augustkarlstedt.github.io/blog/2018/10/11/learning-about-android-and-fireos/2/
Disabled with `pm disable-user`: process gone, camera closed, in `privacy-hardening.sh`
and `verify-device.sh`.

It stays installed, disabled by default, so it can be used later:

- Turn on: `pm enable com.amazon.knight.hds` (camera on again, about 0.9 W more).
- Events: broadcast action `com.amazon.knight.hds.ACTION_PRESENCE_DETECTED`, services `.HDService`
  and `.HDClientService`, native `libcv_presence_detector_jni.so`, models under `assets/presence/`.
- Permissions: `GET_HUMAN_PRESENCE_EVENTS` (signature) and `CONTROL_HDS` (signature|privileged),
  so a listener must be a privileged app or use root. Not tried.

After disabling, 60 s again: 74% busy, `org.minitv` 36.9, audioserver 12.8, `amazon.speech.sim` 8.9,
pryonwake 5.1, surfaceflinger 3.8, mediacodec 2.5, CPU 80 C. minitv's rise is in its player
threads and garbage collection, so it depends on the stream. The power saved is mostly not CPU.

Power at the wall, by the owner's meter: 4.8 W before, 3.9 W after, about 19% less.

No software power sensor exists: only `power_supply/ac` with online, `hwmon` is temperature only.

Magisk was considered for hiding apps and not used: its boot image and SELinux changes put the
early firewall at risk, and moving an APK folder out of `/system` gives the same result here.

---

## 6. Pryon wake word engine

`/system/lib/libpryon.so` (3.1 MB, 152 exports), a Kaldi-derived engine [static].

Models: `/system/local/models`, 72 MB, 272 files [measured].

```
keyword/<locale>/<WORD>/pryon.manifest    entry point, JSON
                       /pryon.config      front end and decoder settings
                       /kw.cfg.json       detector config, thresholds
                       /op.cfg.json       state machine: sleep/awake
                       /*.hclg.pfst       foreground and background graphs
                       /<WORD>.psvm, .scales   second-stage SVM classifier
                       /dnn_quantized.mlp.gz or kaldi_dnn.mlp   acoustic model
                       /phones.txt, words.shrunk.txt, pdf.counts, *.cmvn
                       /playback/         second model, for self-trigger rejection
AED/                                      acoustic event detection
```

Locales (19): ar-SA de-DE en-AU en-CA en-GB en-IN en-US es-ES es-MX es-US fr-CA
fr-FR hi-IN it-IT ja-JP mul-021 mul-155 mul-CA pt-BR.
Words: ALEXA, AMAZON, ECHO in all 19; COMPUTER in 13 [static].
de-DE sizes: ALEXA 11 MB, AMAZON 6 MB, COMPUTER 1.9 MB, ECHO 1.6 MB.
echo-dot-assist says other words must be downloaded; on this unit they are present.

Each model also holds `STOP`, so "stop" works without the cloud in awake state:
`op.cfg.json` moves sleep to awake on the word, back on STOP or after 175 s.
`kw.cfg.json` lowers thresholds during playback (properties in section 7).

Front end [static], de-DE ALEXA: LFBE, 25 ms frames, 10 ms shift, pre-emphasis
0.96875, 20 mel bins 100-6000 Hz, Povey window, 20 left + 10 right context.

Current config, `shared_prefs/amazon.speech.wakewordservice.wakewordinfoprovider.xml`
[measured]: active `alexa`, locale `de-DE`, combinations `computer`, `amazon`,
`alexa`, `echo`. The app's own state: `listener_status` `MODEL_DOWNLOAD_QUEUE_EMPTY`.

Other library features [static]: `PryonEnrollmentApi_*` (speaker enrollment, not a
new word), acoustic events, natural turn taking, federated learning callbacks,
`PryonApi_TurnOffSecureMode`. The firewall stops any upload of the latter.

`libwakewordserver_jni.so` exposes on `amazon.speech.wakewordservice.WakeWordService`:
`createNativeService`, `switchPryonModel`, `setPryonProperty`,
`setPryonAcousticProperty`, `setDetectorEnabled`, `getDetectorEnabled`,
`injectWaveFile`, `setAEDDirectory`, `setFingerprintDatabase`, `getInputFrameCount`,
`enablePlaybackDetector`. `injectWaveFile` could test detection without sound.

### Custom words on Pryon

Not possible with known tools. The graph, DNN and SVM come from Amazon's training
pipeline, and no third-party training exists [reported]. Only "Computer" or "Echo"
instead of "Alexa", or a different language model.

---

## 7. Running Pryon ourselves

From echo-dot-assist `src/hassmic/wake_pryon.c`, and now [measured] on this build with
`tools/pryonwake.c`. The `end` field at +0x40 returned garbage here; `begin` at +0x08 works.
No NDK needed: `tools/build-pryonwake.sh` links against the device's libraries and
replaces crtbegin with a few lines calling `__libc_init`.

1. `PryonApi_SetLoggingCallback`, `PryonApi_SetEnumeratedResultCallback`
2. `PryonModelSet_New("id", "<dir>/pryon.manifest", "")`
3. `PryonDecoder_NewPryonMultichannelAudioFormat_Default(&fmt)`
4. `PryonDecoder_NewSpotterAudioDecoder("dec", "id", "pryon", fmt, "{}")`
5. loop `PryonDecoder_PushAudioEventSamples("dec", index, int16 samples, n)` at real
   time, 16 kHz mono
6. `PryonDecoder_SessionEnd`, `PryonDecoder_Delete`, `PryonModelSet_Delete`

Result struct: keyword +0x1c, type +0x24 (0 near miss, 2 accept), begin +0x08,
end +0x40. Properties: `AudioPlayerState`, `audio_playback`, `AudioPlaybackState`,
`MediaPlayerState`, `TtsPlayerState`, `EarconPlayerState`, `AlarmState`.

Alternatives: Pryon-AFE-Runtime (`pryon` adapter, simpler `PryonDecoder_NewPcmInt16`,
Echo Dot 2) and echo-dot-2-playground (JNI via DexClassLoader, with
`CAPTURE_AUDIO_HOTWORD`, needs `/system/priv-app`, about 17% CPU on Dot 2).

Requirements: root, `system` or root UID for capture, Amazon apps stopped so that
the model and audio are free. Removal of `amazon.speech.sim` is safe only once we
own the home screen (Paladin uses its permissions).

---

## 8. Open questions and test plan

| # | Question | How |
|---|---|---|
| 1 | Do the mics show sound on raw PCM? | Done: yes |
| 2 | Are ch0 and ch1 separate? | Done: not identical, 0.93-0.95 |
| 3 | Does mute zero the PCM? | Done: exact digital zero, `state` = 1 |
| 3a | What does raw playback need to reach the amp? | Not needed for our path. HAL trace or TECHO5 source |
| 4 | Does a third `AudioRecord` get audio? | Done: yes, section 5a |
| 5 | Is the processed stream better than raw for Vosk? | Done with the speaker muted: about equal, section 5b |
| 6 | Which of source 6 or 2999 feeds the stock Pryon? | No longer needed for our path |
| 7 | Does `libpryon` run for us at NS6548? | Done: yes |
| 7a | Hit rate of "Alexa <channel>" over many tries? | 20 tries, quiet and with TV, with `/sdcard/voice-debug` on |
| 7b | Better speech model? | Replay 10 such recordings on the host, no device change |
| 7c | Does "Computer" work as well? | `install-echowake.sh <manifest>`, reboot, repeat the series |
| 8 | CPU of our engine next to Amazon's? | `top` over 10 minutes, not one sample |

---

## 9. Plan for our own hotword and speech model

Amazon's word is in place (section 5b). This remains the plan for a custom word.
Recommended, simplest first:

1. **Capture:** `AudioRecord`, `VOICE_RECOGNITION`, 16 kHz mono, same as minitv
   `VoiceActivity.java:103-110`. This keeps Amazon's AEC and beamforming, and works
   alongside Amazon's recordings (section 5a).
2. **Hotword:** microWakeWord on TFLite Micro, as TECHO5 does on this very hardware.
   Custom words are trained off the device (Linux, Python 3.10-3.12, about 16 GB RAM,
   60 GB disk). Fallbacks: openWakeWord (Apache 2.0, heavier), sherpa-onnx keyword
   spotting (Apache 2.0, 32-bit ARM), Porcupine (proprietary, key required).
3. **Now in use:** Pryon with "Alexa", section 5b.
4. **Recognition:** after the hotword, reuse minitv's Vosk (0.3.75,
   `vosk-model-small-de-0.15`, 92 MB unpacked, armeabi-v7a, grammar of channel names).
   minitv has no hotword code of its own; a service hands it a recording (section 5b).
5. **Stop Amazon's pipeline** (`pm disable` plus removal from `/system`, as hassmic
   does) once ours works, to free about 11% CPU. Keep `libasp` in the HAL: it is the
   front end. Raw PCM needs our own AEC: TECHO5 uses webrtc-audio-processing against
   ch2/ch3 in a helper process.

Beamforming gain with 2 mics is small; AEC against the loopback matters more,
especially while minitv plays.

---

## 10. Tools

| File | Purpose |
|---|---|
| `tools/echocap.c`, `tools/build-echocap.sh` | raw capture, no libc, needs clang and `lld-19` |
| `tools/compat/` | minimal headers for `asound.h` without libc |
| `tools/pryonwake.c`, `tools/build-pryonwake.sh` | Amazon wake word engine as a filter, stdin PCM to stdout lines |
| `tools/micpipe/MicPipe.java`, `tools/build-micpipe.sh` | processed mic stream to stdout, as root |
| `echowake/` | the service: script, rc file, config |
| `scripts/install-echowake.sh` | build and install all of it |
| `tools/analyze-capture.py` | per-channel level, spectrum peak, correlation, lag |
| `scripts/mic-capture.sh` | the full capture procedure |

---

## 11. Sources

- [TECHO5](https://github.com/HuskerMinion/techo5), `docs/hardware.md`, `docs/training-wake-word.md`
- [echo-dot-assist](https://github.com/Gamer92000/echo-dot-assist), `devices/cronos`, `src/hassmic/wake_pryon.c`
- [Pryon-AFE-Runtime](https://github.com/T-vK/Pryon-AFE-Runtime)
- [echo-dot-2-playground](https://github.com/albertoZurini/echo-dot-2-playground)
- [Echo Show 5 2nd gen teardown](https://www.briandorey.com/post/echo-show-5-2nd-gen-smart-display-teardown)
- [TLV320AIC3101 datasheet](https://www.ti.com/document-viewer/TLV320AIC3101/datasheet)
- [Alexa wake word research paper](https://arxiv.org/pdf/2008.00508)
- [PryonLite model notes, XMOS](https://www.xmos.com/documentation/XM-014600-PC/html/doc/wakeword/WW_setup.html)
- [openWakeWord vs microWakeWord](https://community.home-assistant.io/t/openwakeword-vs-microwakeword/1000318)
- [sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx)
