# miniapps

Small, focused Android apps for locked-down, cheap and unusual devices: an Amazon Echo Show,
Android TV boxes and projectors, and ordinary phones.

| App | What it does |
|---|---|
| [**minitv**](minitv/) | A full-screen TV: IPTV channels, program guide, offline voice control, remote control over MQTT |
| [**minicall**](minicall/) | Video calls between devices, to web browsers and to SIP apps, answered by themselves for known callers |
| [**minisshd**](minisshd/) | A persistent SSH server for devices that would otherwise need a USB cable for a shell |
| [**minicaptive**](minicaptive/) | Stays online in hotel and hospital WLANs: passes captive portals (WISPr, forms, JavaScript) by itself |
| [**miniha**](miniha/) | A Home Assistant compatible hub in one static binary: MQTT discovery, automations, dashboards, HA REST and WebSocket APIs; runs on a Pi Zero 2 or a rooted Echo Show |

## Hardware notes

| Device | What is there |
|---|---|
| [**Echo Show 5 (2nd gen, 2021)**](hardware/echo-show-5-gen2/) | Jailbreak, backup, root, setup skip, settings fix, minitv and the Amazon network block, with reusable scripts for other "cronos" units |

## Download

Every change on `main` is built and published under
[Releases](https://github.com/probonopd/miniapps/releases): one APK per app and processor, `armeabi-v7a` (32-bit
ARM, e.g. the Echo Show and most TV boxes), `arm64-v8a` (64-bit ARM, current phones) and `x86_64` (Intel);
minicaptive has no native code and runs on all. miniha is not an APK but a static binary per OS and processor
(Linux, FreeBSD, OpenBSD; the Linux ones also run on Android from a root shell).

To keep them up to date, use [Obtainium](https://github.com/ImranR98/Obtainium): add
`https://github.com/probonopd/miniapps` once per app and set "Filter APKs by regular expression" to the app's
name, for example `^minicall-`. Obtainium picks the APK for the device's processor by itself.

## What they have in common

- **No Android SDK or NDK.** Java is compiled with `javac` and D8; native parts are cross-compiled into static
  programs (with [zig](https://ziglang.org/), dropbear with an ARM gcc). Every download a build makes is pinned by
  its SHA-256.
- **Wide version range.** They run on Android 7 (the Echo Show 2nd generation) as well as on Android 15, and use the
  hardware where it helps (video coding, echo cancellation).
- **No accounts, no paid servers.** Remote control and call setup go over an MQTT broker of your choice (a free one
  is enough); calls go directly between the devices.
- **Permissive licenses throughout:** the apps under MIT, the libraries they use under BSD, MIT or Apache.

## Working together

minitv's MQTT command `cmd/call` starts and ends minicall's calls. minicall takes such requests only from apps
signed with the same key, which is why all apps here are signed with one key.

## Building

Each app has a `build.sh` and its own README with the details. `ARCHS="armv7 arm64 x86_64"` chooses the
processors for the native parts, `APK_SPLIT=1` makes one APK per processor (as the releases have). You need a JDK, apktool, python3 with
`cryptography`, curl and zip; minicall also cmake, ninja and zig, minisshd an `arm-linux-gnueabihf-gcc`.

```
sh minitv/build.sh
minicall/helper/build.sh armv7 && minicall/helper/build.sh arm64 && sh minicall/build.sh
minisshd/dropbear/build.sh && sh minisshd/build.sh
sh minicaptive/build.sh
cd miniha && make          # Go 1.24; "make all" cross-compiles every target into dist/
```

## Signing

The releases are signed with one private key that only the maintainer has (in CI it is a GitHub Actions secret).
It stays private on purpose: Android installs an update only over an app signed with the same key, and minicall
takes call commands only from apps signed like it (that is how minitv starts calls). A public key would let any
APK pass as an update and let any app start calls with camera and microphone.

You are welcome to build and sign the apps yourself: `common/apk.sh` creates `keys/signing-key.pem` on the first
build if there is none. Your builds then do not install over the released apps, nor the other way around;
uninstall one before installing the other. Keep a copy of your key, or you cannot update your own builds either.

| Folder | |
|---|---|
| `common/` | Build steps the apps share: pinned downloads, compiling, packaging and signing |
| `keys/` | The signing key (not in git). Keep a copy: Android only installs an update signed with the same key |
