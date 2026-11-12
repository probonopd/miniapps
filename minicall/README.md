# minicall

Video calls for the Echo Show 2nd generation, phones and other Android devices (7.0 and newer, 32 and 64 bit)
without paid servers and without anything to touch for the person in front of the device:

- **Device to device**: two minicall devices (an Echo Show and a phone, say) call each other over an MQTT broker,
  no account (`contact=Name=mqtt:<the other device's topic>`).
- **Web**: calls to and from a web page (`web/call.html`) in any current browser, over the same broker, no account
  and no app on the other side.
- **SIP**: calls to and from ordinary SIP apps (Linphone and others), with a free SIP account or, in the local
  network, without one.

Callers on the contact list are answered after a short ring; nobody else is. The call fills the screen in landscape
(no black bars: between devices each sends the other's screen shape), phones show the own picture in a corner
(cut exactly as the far end shows it), and one button hangs up. Over MQTT, the TV app minitv can start and end
calls (`cmd/call`).

Everything is permissively licensed: baresip and libre (BSD-3), Opus (BSD-3), libvpx (BSD-3), OpenSSL 3.5
(Apache-2.0), WebRTC audio processing (BSD-3) with abseil (Apache-2.0), the HiveMQ MQTT client (Apache-2.0),
RxJava (Apache-2.0), and this code (MIT).

## Setting up

1. Build (below) or take `minicall.apk`, then `adb install -r -g minicall.apk` (`-g` grants camera, microphone and
   storage).
2. Write the settings: `minicall.conf.example` explains every line. Copy them to `/sdcard/minicall.conf` and start
   the app; it moves the file into its private storage.
3. On the Echo Show: run `scripts/install-amazon-block.sh` of the echo-show-2gen repository, so that the firewall
   lets the new app out, and install its `echowake.sh`, which gives the microphone to a call (see below).

### Web calls for relatives

Put `web/call.html` on any web server with https (GitHub Pages will do; the page holds no secrets) and send each
person a link with their own name:

```
https://example.org/call.html#host=CLUSTER.s1.eu.hivemq.cloud&user=family&pass=PASSWORD&topic=minicall/wohnzimmer&name=Oma
```

Browsers never send the part after `#` to the server. The page shows whether the device is online, has one button
to call, and rings when the device calls (`contact=Oma=web:Oma` on the device). The broker user in the link
should only be allowed `minicall/wohnzimmer/#`.

### Calling the device from a PC

The device answers only its contacts, so first give the PC a contact line in `minicall.conf` (and copy it to the
device again):

```
contact=PC=sip:pc@192.0.2.10        # SIP: user and host the PC calls from (the port does not matter)
contact=PC=web:PC                      # or the web page with name=PC
```

Any of these three ways then works:

1. **Browser** (no installation): open the web page link with `name=PC` (see above) and press Call. Needs the
   MQTT settings on the device. A camera and microphone on the PC are used if there are any; `test=1` in the link
   sends a test picture instead.
2. **A SIP app**, for example Linphone (Windows, macOS, Linux):
   - in the local network without an account: call `sip:minicall@ADDRESS-OF-THE-DEVICE`. The contact line must
     name the PC's address (`sip:pc@192.0.2.10` with the user name set in the app as `pc`).
   - over the internet: both sides with a SIP account (for example free ones at sip.linphone.org); the contact
     line names the PC's account (`sip:name@sip.linphone.org`), and the PC calls the device's account.
   - In the app: video on, the codec **H.264** enabled (the device sends and receives only H.264; Linphone may
     offer to download the H.264 codec on first use), ICE with a STUN server (`stun.l.google.com:19302`).
     Not yet tried with Linphone itself; tested with baresip.
3. **baresip on Linux** (what the tests use, no camera needed): build the host helper (`helper/build.sh host`)
   and run `tests/pc-peer.sh /tmp/peer 60 sip:minicall@ADDRESS-OF-THE-DEVICE`. It sends a test picture and a
   440 Hz tone and records what it gets in `/tmp/peer/out` (`display.h264`, `speaker.raw`). Without an address it
   waits 60 s for the device to call it (the contact line `sip:pc@<this PC's address>`).

The device can also call the PC: touch its button on the device, or `<minitv topic>/cmd/call` with `PC`.

### From minitv (MQTT)

`<minitv topic>/cmd/call` with a contact's name calls it; empty or `hangup` ends the call. minitv sends the
broadcasts `org.minicall.action.CALL` (extra `to`) and `org.minicall.action.HANGUP`; minicall's receiver takes them
only from apps signed with the same key (signature permission `org.minicall.permission.CALL`), and even then only
contacts can be called. For tests from `adb shell`: works where the shell is root (Echo Show), not on a phone.

## How it works

```
minicall.apk (Java)
 ├─ CallService: settings, call states, ringing, auto-answer, MQTT signaling (Signaling), call mode on phones
 ├─ CallActivity: far end (MediaCodec decodes onto a SurfaceView, filling the screen), self view, hang-up button
 ├─ AudioBridge: AudioRecord (Echo: source 6 at 16 kHz; phones: voice call source, speakerphone), AudioTrack
 ├─ VideoBridge + CameraEncoder: camera -> GPU (turned upright, cut to the far end's shape) -> MediaCodec
 │    H.264 encoder (constant bit rate); access units -> MediaCodec decoder -> screen
 └─ lib/<abi>/libbaresip.so: the baresip helper, a static musl program (zig, no NDK), armeabi-v7a and arm64-v8a
      ├─ ctrl_stdio: commands and events as JSON lines over its stdin/stdout; WebRTC offer/answer for MQTT calls
      ├─ sockaudio:  PCM to and from the app over abstract Unix sockets
      ├─ sockvideo:  H.264 access units to and from the app, RTP packetizing in libre
      └─ webrtc_aec: WebRTC audio processing: echo canceller (AEC3), high-pass, noise suppression, AGC2
```

- The helper runs as the app's child under the app's uid, so the firewall allow-list covers it, and its sockets
  only accept that uid.
- Video is coded by the device's hardware (MediaCodec H.264, constrained baseline), the camera picture goes to the
  encoder through the GPU without passing Java: software VP8 took most of a core for 640x360. The size follows the
  screen (`video-size=auto`): the Echo Show sends 864x480 (its screen is 960x480), the moto g34 1280x576; between
  devices the sender cuts to the receiver's screen shape. The bit rate is constant: an encoder that sends more than
  agreed fills the sender's queue and puts the picture seconds behind the sound.
- Audio: WebRTC's audio processing in the helper (`audio-processing=webrtc`, noise suppression level
  `webrtc_ns_level high`); the Echo Show's quiet stream gets +15 dB fixed before the adaptive gain. Phones run in
  call mode with the loudspeaker as the communication device and the platform echo canceller, without the
  platform noise suppressor (it lowered the voice). `audio-processing=simple` uses the app's own gain control and
  noise gate instead.
- NAT: ICE with STUN. Without TURN a call between two strict NATs (both on carrier-grade NAT, for example)
  can fail; IPv6 usually avoids that.
- The Echo Show's wake word service (echowake) holds the one free capture slot. During a call the app keeps
  `/sdcard/minicall/in-call`; echowake stops while that file exists and the app runs, and starts again after.

### Patches to upstream code

`helper/patches/`, applied by `helper/fetch.sh`:

| Patch | Why |
|---|---|
| `re-netlink-ifname.patch` | Interface names come from netlink: `if_indextoname()` needs `SIOCGIFNAME`, which Android's SELinux policy denies to apps, and then no address was found at all. |
| `re-printaddr-scope.patch` | Same denial when binding link-local IPv6: print the numeric scope. |
| `re-ice-controlled-selection.patch` | ICE in the controlled role: use the pair the peer nominates, else the one of highest priority (not the first one validated), and pair peer-reflexive candidates (RFC 8445 7.3.1.4), which browsers that hide their host address behind mDNS depend on. |
| `baresip-ice-no-peer-support.patch` | A peer without ICE: the streams were never reported connected, so no media started. |
| `baresip-webrtc-video-decoder.patch` | WebRTC: the receiving video stream never got a decoder. |
| `baresip-dtls-answer-passive.patch` | The answerer is DTLS passive: an active answerer starts DTLS while its answer is still on its way through MQTT, the offerer drops that hello and the call can hang. |
| `baresip-webrtc-aec-conference.patch` | The webrtc_aec module with what conference clients use (AEC3, high-pass, noise suppression, AGC2 instead of AGC1), settings `webrtc_ns_level` and `webrtc_fixed_gain_db`, and mono processing of Opus' two channels. |
| `re-netlink-ifname.patch` (also) | Android 11 and newer deny apps the netlink link list (RTM_GETLINK); the addresses alone are used then. |
| `abseil-cpp-zig-armv8-crypto.patch` | zig rejects abseil's `-march=armv8-a+crypto`. |

### Babyphone

A phone's start screen has a blue "Babyphone" button for each device contact. It shows that device's camera
and plays its microphone, one way: the phone opens neither camera nor microphone (it sends silence). The
watched device answers by itself, without a ring, without the call screen and without playing anything, so
what its screen shows stays as it is. Only device contacts listed in its `babyphone=` may do this; with the
setting empty (the default) every babyphone request is refused. While it runs, the Echo's wake word pauses,
as in any call.

### When a call does not come about

- **Busy**: a device in a call refuses a second one with "busy"; the caller shows "busy" and plays the busy tone
  for 3 s (also for SIP "486 Busy"), the web page says "... is busy".
- **No answer**: a device answers by itself within seconds, so an offer to a device (`mqtt:` contact) that gets
  no answer within 15 s ends the call (the device is off or offline); web pages and SIP phones, where a person
  answers, get 60 s.
- **No connection**: once answered, on both sides, the call must be established within 15 s; otherwise it is
  ended instead of hanging at "connecting" (and keeping the Echo's wake word off).
- After an update the app starts again by itself and clears an in-call flag left behind.

## Building

Needs a JDK 17+, apktool with a framework, python3 with `cryptography`, curl, zip, cmake, ninja, and zig in
`~/opt/zig-venv` (`pip install ziglang`). No Android SDK or NDK.

```
helper/fetch.sh           # sources, pinned in helper/sources.sha256, patches applied
helper/build.sh armv7     # the helper for the Echo Show (32 bit), also builds WebRTC audio (helper/build-apm.sh)
helper/build.sh arm64     # the helper for 64-bit phones
sh build.sh               # minicall.apk with both, signed with ../keys/signing-key.pem (shared with minitv)
```

## Tests

On this machine, without a device (`helper/build.sh host` first):

- `tests/call-test.sh` - two helpers call each other over SIP: H.264 both ways, audio both ways, events.
  `PARAMS_A=';medianat=ice;stunserver="stun:stun.l.google.com:19302"'` (and `PARAMS_B`) adds ICE.
- `tests/webrtc-test.py WORKDIR SECONDS` - serves http://localhost:8099/, a page that calls the helper over WebRTC.
- `tests/webrtc-p2p.py WORKDIR [SECONDS]` - two helpers, one offering, one answering, as two devices over MQTT.
- `tests/pc-peer.sh WORKDIR SECONDS [sip:address]` - a SIP video phone on this machine for calls with a device.
- `tests/minibroker.py` - an MQTT broker for the local network (TCP 1883, WebSocket 8083), so that test calls do
  not go through a public broker. The page then: `call.html#host=localhost&port=8083&tls=off&topic=...&test=1`.

Measured on the Echo Show 2nd generation (MT8163, Android 7.1.2):

- SIP with the PC: the Echo's camera arrives as 640x480; PC to the Echo decoded at 30 fps; audio both ways;
  auto-answer after the ring.
- Frame rate from the Echo: the camera delivers 20 fps in a dim room although its preview range is set to
  30-30 (`setRecordingHint` changes nothing); the hardware encoder takes every frame.
- Web page (Chromium) with the Echo: both directions of video and audio, either side calling, either side hanging
  up, a caller who is not a contact refused.
- Microphone: only source 6 at 16 kHz carries sound (speech about -53 dBFS before the gain); the device's own
  playback is cancelled (a tone at -15 dBFS came back below -84 dBFS). The voice call stream is silent on this
  device, the media stream is used.
- In the local network a SIP app without ICE reaches the Echo, but its media goes to the Echo's public address,
  which only works where the router loops it back.
- With the moto g34 5G (Android 15): the Echo decodes the phone's 1344x672 at 30 fps; the Echo's camera sends about
  20 fps (dim room). Picture and sound in sync after the constant bit rate. The Echo's helper takes about 9% CPU
  during a call with WebRTC audio processing.
- Moto on mobile data, Echo on the home network: call setup over the broker, media directly to the Echo's public
  IPv4 address (found by STUN); 30 fps decoded, no lost frames.
- Network changes: the MQTT client logs in again on every automatic reconnect (it used to reconnect without the
  login and was refused), and the helper is started again with the new network's DNS servers after a change
  while no call runs. On Android 15 the helper is killed with SIGSYS (exit 159, a system call the app sandbox
  forbids) when the network goes away; it is restarted within 2 s, but a call in progress ends. Which call it is
  is not known yet (needs a network switch with adb over USB).
- Without a relay (TURN) a call can fail when both sides are behind strict NAT without IPv6, or where UDP is
  blocked; there is no TURN server without an account from a reputable company.
