# Echo Show 5 (2nd gen, 2021) - cronos - complete documentation

Everything done to this unit, why, how to verify it, and how to repeat it on
another unit of the same type. The backup directory is **read-only** and is
never modified by these procedures.

---

## 1. Device

| | |
|---|---|
| Model | Amazon Echo Show 5, 2nd gen (2021) |
| Codename / product | `cronos` / `omni_cronos` |
| Model string | `Echo_Show_5` (shared with the 1st gen; the codename identifies it) |
| SoC | MediaTek MT8163 |
| OS | Fire OS 6.5.4.8 (`ro.build.id` NS6548 / 3290), Android 7.1.2, SDK 25 |
| Recovery | TWRP `3.7.0_9-0` (amonet) |
| Bootloader | unlocked, `verifiedbootstate=yellow`, `rpmb_state=1` |
| eMMC | SK Hynix `H8G4a2`, 15,269,888 sectors = 7,818,182,656 bytes, GPT |

> The frozen backup README says "not Echo Show 2nd gen"; XDA lists **cronos as
> the 2nd gen (2021)**, so that note is superseded. The backup itself is left
> untouched.

---

## 2. Safety rules

1. **The backup is read-only.** Never write, move, delete or chmod inside
   `backup-20261031-122840/`. Verify with its `tools/verify.sh`.
2. **Never flash `lk`, `preloader`, `tee1`, `tee2`.** The XDA thread warns a
   mistake there is a permanent brick (no BROM USBDL unless the preloader
   USBDL mode is exposed).
3. Boot-partition changes are recoverable: the stock boot is in the backup
   (`images/p09-boot.img`) and TWRP is reachable with **Volume Up** at power.
4. Root adb means the device trusts the USB cable completely
   (`ro.secure=0`, `ro.adb.secure=0`). See §11.
5. WLAN is currently **off**; when enabled, the firewall in §9 keeps
   Amazon/preinstalled apps offline while user apps keep internet.

---

## 3. Recovery modes (amonet button remap)

| Mode | How |
|---|---|
| TWRP / recovery | power off, hold **Volume Up**, apply power |
| Hacked fastboot | power off, hold **Volume Down**, apply power |
| Stock fastboot | power off, hold **all three buttons**, apply power |
| USBDL / unbrick | hold **Mute + Volume Down**, apply power, USB connected |

---

## 4. Host setup

- `adb` 1.0.41 (platform-tools 34.0.5) on Debian.
- Fire OS exposes adb as USB **`1949:01e8`**, which needs a udev rule or adb
  reports “no permissions”:
  `scripts/udev-rule.sh` installs
  `/etc/udev/rules.d/51-amazon-echo-adb.rules`
  (`SUBSYSTEM=="usb", ATTR{idVendor}=="1949", ATTR{idProduct}=="01e8", MODE="0666"`).
- Removable media: `udevil`/`devmon`. `devmon` mounts with `-o noexec,...` but
  `udevil.conf` did not allow `noexec`, so every mount failed with
  *“Option 'noexec' ist keine erlaubte Option”*. `scripts/udevil-automount.sh`
  adds `noexec` to `allowed_options` (backup kept), restarts `devmon`, and
  mounts existing media. Verified by unmounting the USB drive and re-triggering a
  udev `add` event - it automounted again.
- `sudo` on this host works with `sudo -A -E` (`SUDO_ASKPASS` configured); the
  scripts use that.

---

## 5. Backup (2026-10-31)

Location: `backup-20261031-122840/` (read-only, 24 GB).

| Area | File |
|---|---|
| whole eMMC (7,818,182,656 B) | `raw/mmcblk0-full.img` |
| second dump (identical) | `raw/mmcblk0-full-dump2.img` |
| eMMC boot areas (4 MiB each) | `raw/mmcblk0boot0.img`, `raw/mmcblk0boot1.img` |
| per-partition images (GPT) | `images/p00-…`, `p01-…` … `p16-userdata.img`, `p99-gpt-backup.img` |
| `/data` file tree | `data-files/root/` (2.1 GB, 835 files) |
| metadata | `meta/` (getprop, cmdline, GPT, MD5s, dmesg, mmc sysfs) |
| hashes | `SHA256SUMS` (879 files) |

Verification performed:

- dump MD5 and SHA-256 match `md5sum /dev/block/mmcblk0` on the device; values in the
  backup, not here: they identify this unit.
- second dump `cmp` exit 0, same hashes (`meta/dump2-comparison.txt`).
- every partition image matches the device MD5 of that partition.
- `e2fsck -fn` on `p16-userdata.img`: clean (2532 inodes, 569700/1010171 blocks).
- boot0/boot1 match the device MD5s.

Restore (see the backup README for all details):

```sh
# whole eMMC, from TWRP
adb shell 'sync; umount /data /cache /sdcard'
adb shell -T 'dd of=/dev/block/mmcblk0 bs=1048576' < raw/mmcblk0-full.img

# one partition
adb shell -T 'dd of=/dev/block/mmcblk0p9 bs=1048576' < images/p09-boot.img
```

**RPMB** (4 MiB, `/dev/block/mmcblk0rpmb`) is not in the backup: it is a
JEDEC replay-protected area, every access is authenticated with a per-eMMC key
held by the SoC secure world, a plain read returns `I/O error`, and a copy would
be cryptographically useless on another eMMC. It holds rollback counters and
DRM/secure-storage state, not user data. Details and evidence:
`backup-20261031-122840/README.md` §9 and `meta/mmc-sysfs.txt`.

---

## 6. Root boot image

`boot-root.zip` (md5 `cd8263d36ca7e6f6af943f2558da8239`) contains
`boot.img` (8,671,232 B, md5 `77028e64e9fc90abaa70e3f72362d81a`).

Installed through TWRP (`scripts/install-boot-root.sh`):

```sh
adb push boot-root.zip /tmp/boot-root.zip
adb shell 'twrp install /tmp/boot-root.zip'     # dd's boot.img to mmcblk0p9
```

Read-back of the first 8,671,232 bytes of `mmcblk0p9` matched `boot.img`.

What it changes (`default.prop` of the new ramdisk):

| Property | Stock boot | boot-root |
|---|---|---|
| `ro.secure` | 1 | 0 |
| `ro.adb.secure` | 1 | 0 |
| `ro.debuggable` | 0 | 1 |
| `persist.sys.usb.config` | `none` | `mtp,adb` |
| build | `NS6548/3290` | `NS6571/6208N` |

It replaces `/init.fosflags.sh` with a 3-line version that forces
`persist.sys.usb.config mtp,adb` and `amazon.fos_flags.noadbauth 1`, and adbd is
started by init on the `sys.usb.config=...adb` triggers.

After reboot (verified): `sys.boot_completed=1`, `adb shell id` →
`uid=0(root)`, `adb_enabled` became 1, Fire OS 6.5.4.8 still boots.

Notes:

- The payload is from NS6571 while the installed system is NS6548; the device
  booted normally, but this is the main compatibility risk for another unit.
- The stock boot it replaced is `images/p09-boot.img`
  (md5 `eedb993aa48107a2516321ad09a10a52`). Rollback:
  `adb shell -T 'dd of=/dev/block/mmcblk0p9 bs=1048576' < images/p09-boot.img`.

---

## 7. Skipping the OOBE

At first boot the wizard (`com.amazon.kindle.otter.oobe`, language picker) owned
HOME with priority 100 over the launcher `com.amazon.paladin`
(priority 50). `scripts/skip-setup.sh`:

```sh
adb shell settings put global device_provisioned 1
adb shell settings put secure user_setup_complete 1
adb shell pm disable-user --user 0 com.amazon.kindle.otter.oobe
adb shell am force-stop com.amazon.kindle.otter.oobe
adb shell am start -n com.amazon.paladin/.app.MMLauncherActivity
```

Result: `mCurrentFocus` = `com.amazon.paladin/.app.MMLauncherActivity`, HOME
stays there. Revert: `pm enable --user 0 com.amazon.kindle.otter.oobe` and set
the two settings back to 0.

---

## 8. Android Settings app fix

Symptom: `com.android.settings/.Settings` crashed immediately, every sub-screen
too:

```
java.io.FileNotFoundException: null/preference-settings.json
  ConfigFileReader.getPreferenceSettingsFromFile
  SettingsActivity.loadCategoriesFromResource
```

Cause: `ConfigurationBundleManager.getPathToConfigFile()` builds
`<bundle_location>/preference-settings.json`; `bundle_location` (shared prefs
`com.amazon.settings.CONFIG_BUNDLE_STORE`) was unset, so the prefix was the
string `null`. `/sdcard/tarBundle`, the alternate on-device bundle location,
was empty - the device never extracted its settings bundle.

Fix (`scripts/fix-settings-app.sh`): the bundle ships inside the Settings APK's
own assets. `assets/DE.tgz` contains `preference-settings.json`, `strings.json`,
`uris.json`, `languages.json`, `endpoints.json`; `assets/de-DE.tgz` contains
`language_config.json`. They were extracted into

```
/data/data/com.android.settings/files/bundle_a
```

(owner `system:system`, SELinux `system_app_data_file`), and
`bundle_location=/data/data/com.android.settings/files/bundle_a` was written to
the prefs file. Settings then opened normally, showing Bluetooth, Netzwerk
(“WLAN ist ausgeschaltet”), Startseite und Uhr, Bildschirm, …

---

## 9. Strict egress allowlist (Amazon/preinstalled blocked)

Goal: user-installed apps keep internet; Amazon and preinstalled apps/daemons do
not; no update checks or updates; no gap at boot.

### 9.1 Model

`AMZBLOCK` is the first rule in `OUTPUT` (IPv4 and IPv6) and ends in **REJECT**.
The `OUTPUT` policy is **DROP**, plus a loopback rule, so the device stays
offline even if the chain is missing. Only these pass:

| Allowed | Why |
|---|---|
| loopback | local IPC |
| UIDs in `/system/etc/amazonblock.allow` | user-installed apps only (generated from `pm list packages -3`) |
| root and system UIDs, DNS 53 tcp/udp | netd resolver |
| root and system UIDs, DHCP 67/68 (v4), 546/547 (v6) | getting an address |
| root and system UIDs, NTP 123 udp | clock |
| ICMP / ICMPv6 | path MTU, neighbor discovery |
| source port 5555 to private addresses only | adb over WLAN; v4 RFC 1918, v6 `fc00::/7` and `fe80::/10` |

Everything else is rejected, including HTTP/HTTPS from **root UID 0** and
**system UID 1000**, Amazon service users (`trackerd` 5120, `smarthome` 5108,
`keystore` 1017 for `dha_service`, `blemesh` 5027, `inlocservice`) and every
Amazon app UID. The captive-portal rule was removed: portal mode is 0 and it
was the only other route to 80/443.

Consequences to know:

- **adb over WLAN is open on purpose**: anyone on the LAN gets an
  unauthenticated root shell and can switch the firewall off. See §11.
- Captive portals cannot be logged into. Use a network without one.
- Android's connectivity check runs as UID 1000, so the UI may show "no
  internet". User apps still work.
- **New user apps must be added to the allow list** (re-run
  `scripts/install-amazon-block.sh`, or append the UID to
  `/system/etc/amazonblock.allow` and run `sh /system/bin/amazonblock.sh`).
- mDNS (udp 5353) is not allowed for `avahi-daemon` or `mdnsd`, so answering on
  the LAN does not work although both daemons run.
- Root and system UIDs can still send to any address on ports 53 and 123, and
  ICMP is open to every UID. HTTP/HTTPS from blocked UIDs is not possible.
- Lookups for Amazon names outside the 57 blocked hosts may reach the LAN
  resolver, depending on which UID netd uses for the query. Not measured.

### 9.2 Boot-time loading

- `amazonblock.rc` runs `amazonblock.sh early` with **`exec` in `on
  post-fs-data`**. `exec` blocks init, so the firewall is in place before
  `class_start main` starts netd, zygote and the Amazon daemons. Measured:
  **4.96 s** after power-on, compared with about 36 s before.
- The script runs again without arguments whenever `init.svc.netd=running` and at
  `sys.boot_completed=1`. It reloads the chain, repairs the hook if needed
  and stops the daemons.
- Rules are loaded atomically with `iptables-restore -w --noflush`; the expected
  count comes from the same functions that generate the rules
  (`amazonblock.sh want`, currently 17 IPv4 / 16 IPv6). Five attempts.
- **Failing closed:** if the chain cannot be loaded, the DROP policy stays. If
  the policy cannot be set either, the script sets
  `amazonblock.state=failed` and `amazonblock.rc` keeps netd stopped. USB adb
  still works; a normal reboot clears the property.
- Progress goes to the kernel log: `adb shell 'dmesg | grep amazonblock:'`.
- SELinux: `seclabel u:r:adbd:s0`; `init -> adbd` transition is allowed and
  `adbd` is permissive on this boot image, so iptables works. `init` itself has
  no netfilter permission.

Verified across two reboots with WLAN off:

- rule counts, `-P OUTPUT DROP` and `-A OUTPUT -j AMZBLOCK` first;
  `verify-device.sh` ALL CHECKS PASSED.
- flushing `OUTPUT` and restarting netd brought the hook back.
- `setprop amazonblock.state failed` stopped netd and kept it stopped.

**Not yet verified:** real traffic. Before relying on it, capture on the
router or AP while the device reboots with WLAN on, and test adb over WLAN.

### 9.3 Telemetry / smarthome daemons stopped

Stopped at every boot (and immediately by the scripts):

`aal-metric`, `trackerd`, `dha_service`, `inlocservice`,
`halo-hub-broker`, `halo-logr`, `meshmgrservice`, `shblemeshd`,
`shlocalskillsd`, `smarthomewifid`, `amazonfiled`, `wifi_log_levels`,
`integrity_logger`, `whad_cc` (Whole Home Audio Daemon, open ports 55442-55444).

Kept on purpose: `aal` (MediaTek display service: stopped, the backlight could not come back after a 0), `cameraserver` (video conferencing), `avahi-daemon` and
`mdnsd` (local discovery), `securetime` (clock), `rpmb_svc`, `perfmonitord`,
`dbus`.

### 9.4 Updates

Disabled packages: `com.amazon.device.software.ota`,
`.ota.override`, `com.amazon.android.marketplace`, `com.amazon.venezia`.

### 9.5 Hosts block

`/system/etc/hosts` blackholes 57 Amazon/Alexa/OTA names with `0.0.0.0` and
`::` (original saved as `hosts.bak-20261031`). It is a second layer, not the
primary one.

### 9.6 Undo

`scripts/uninstall-amazon-block.sh [--enable-updates]` removes the files,
flushes the chains and optionally re-enables the update packages.

---

## 10. Privacy hardening

Applied by `scripts/privacy-hardening.sh` (settings persist; daemons are
stopped again by `amazonblock.sh` at every boot).

Settings:

- location off: `settings put secure location_mode 0`,
  `location_providers_allowed -`; `com.android.location.fused` disabled;
  `inlocservice` stopped.
- WLAN/BLE background scanning off (`wifi_scan_always_enabled=0`,
  `ble_scan_always_enabled=0`), assisted GPS off, mock locations off.
- captive-portal probing off (`captive_portal_mode=0`), app-error reporting off
  (`send_action_app_error=0`).
- advertising ID zeroed, `limit_ad_tracking=1` (Fire OS may ignore it).
- Bluetooth off, via `service call bluetooth_manager 8` (`svc` has no bluetooth
  command here and `bluetooth_on=0` alone left the radio on). Persists across
  reboot.

Telemetry/diagnostics packages disabled: `com.amazon.client.metrics`,
`com.amazon.device.metrics`, `com.amazon.device.logmanager`,
`com.amazon.device.crashmanager`, `com.amazon.logan`, `com.amazon.dp.logger`,
`com.amazon.wirelessmetrics.service`, `com.amazon.whad`, `com.amazon.avod`,
`com.amazon.device.messaging`, `com.amazon.device.sync`,
`com.amazon.device.sync.sdk.internal`, `com.amazon.alexa.beaconbroadcaster`,
`com.amazon.wifilocker`, `com.amazon.wifi.sync`,
`com.amazon.whisperjoin.middleware`, `com.amazon.device.smarthome.ota`,
`com.amazon.device.rdmapplication`, `com.fireos.usagestats.proxy`,
`com.fireos.arcus.proxy`, `com.amazon.knight.hds`.

`com.amazon.knight.hds` is the Human Detection Service: it kept the camera open all the
time and ran a neural network on the frames. Disabling it took the wall power from 4.8 W to
3.9 W. It stays installed and can be used later; see MIC-AND-HOTWORD.md section 5d. The
camera itself stays available for video calls.

Deliberately **not** disabled:

- **camera** (`com.android.camera2` / `cameraserver`) - user-installed video
  conferencing must work. `--disable-camera` turns it off if ever wanted.
- **avahi-daemon / mdnsd** - local discovery features must keep working.
- Bluetooth stack (radio is switched off; the service is kept).

---

## 11. ADB access and security

- The boot image makes adbd run as **root** with no key authorization
  (`ro.secure=0`, `ro.adb.secure=0`, `amazon.fos_flags.noadbauth=1`).
- Fire OS `init.cronos.rc` sets `service.adb.tcp.port 5555` at boot, so adbd
  also listens on TCP 5555 whenever networking is up - an unauthenticated root
  shell for anyone who can reach the device.
- **Decision for this unit: 5555 is open** (chosen by the owner; tested 2026-10-31 from a LAN host: root shell over WLAN works). The
  firewall lets adbd's replies out to private addresses only, so it cannot
  answer public addresses, but the whole LAN has
  root. That also means any LAN device can disable the firewall, so this unit
  is only as safe as its network. An isolated guest network or VLAN limits that.
- The scripts need one adb device: `adb disconnect` the WLAN connection first,
  or set `ANDROID_SERIAL`.
- To close it anyway:

  ```sh
  adb shell 'setprop service.adb.tcp.port 0; setprop ctl.restart adbd'
  ```

  USB adb is unaffected. (A reboot resets the property to 5555 because
  `init.cronos.rc` sets it again; `amazonblock.sh` does not touch it.)

---

## 12. minitv (TV launcher)

- Package `org.minitv`, installed with `adb install -r -g` from
  a USB drive.
- Playlist copied to `/sdcard/playlist.m3u`; without it the app deliberately
  hands over to another launcher at start (documented in its README).
- Voice control: say "Alexa", wait for the beep, say a channel. Everything about it is in
  [MIC-AND-HOTWORD.md](MIC-AND-HOTWORD.md), starting with section 0.
- Changes in minitv for this:
  - fixes: the model unpacking on Android 7, `Playlist.init` in the voice process (it
    counted the HDMI channel, so devices without TV inputs got the next channel);
  - fix for all devices: a retry timer after `onStop` crashed `tune()` with the player gone;
  - for all devices: grammar of whole phrases, words under 0.7 confidence dropped, best
    partial match in `ChannelMatcher`;
  - only used on the Echo: the `pcm` extra on `VoiceActivity`, deleting the capture when
    done (which unmutes), failure tone, "Beenden" and "Ausschalten", `/sdcard/voice-foreground`, which keeps the model loaded.
- The remote's voice button path was not retested on the HY310 after these changes; the
  matcher's tests pass.
- Started with `am start -n org.minitv/.MainActivity`; it is a full-screen
  “picture only” TV front end (black screen while there is no network).
- It declares MAIN/LAUNCHER/LEANBACK_LAUNCHER and HOME; it is **not** set as
  the default home (the Echo launcher Paladin remains HOME).

---

## 13. Scripts

See `scripts/README.md`. Summary:

| Script | Where |
|---|---|
| `udev-rule.sh`, `udevil-automount.sh` | host |
| `backup-emmc.sh [--second-dump]` | TWRP |
| `install-boot-root.sh` | TWRP |
| `skip-setup.sh`, `fix-settings-app.sh`, `install-amazon-block.sh`, `install-amazon-hosts.sh`, `privacy-hardening.sh`, `install-minitv.sh`, `uninstall-amazon-block.sh`, `verify-device.sh` | Fire OS (root adb) |
| `install-echowake.sh`, `mic-capture.sh`, `watch-wlan.sh`, `rejected-by-uid.sh` | Fire OS (root adb) |
| `generate-allow.py`, `generate-uids.py` | host helpers |

---

## 13a. Watching WLAN traffic

`scripts/watch-wlan.sh [minutes]` runs on the device over USB adb. It stops
minitv, then reads `nf_conntrack` every second and switches WLAN off on the first
accepted flow to a public address other than DNS/DHCP/NTP or ICMP. It sees
accepted flows only: rejected attempts appear as a counter, and DNS names are
not visible (no tcpdump or LOG target on the device).

Reboot with WLAN on, 2026-10-31: firewall at 4.97 s, WLAN connected at about
51 s, 10 minutes watched: no trip, no TCP to public addresses. Allowed: DNS to
the router and a public resolver (root), NTP to two public servers (system),
ICMP. The reload at `boot_completed` resets counters, so they start at about 47 s,
before WLAN had an address. No capture off the device was made.

Rejected packets by UID, about 9 minutes, temporary per-UID chain before `REJECT`
(311 counted, 6 unattributed): `com.amazon.avod` 144, `com.amazon.whad` 90,
UID 1000 (system, shared) 27, `com.amazon.tcomm` 20, `amazon.speech.sim` 16,
`com.amazon.aria` 8. Destinations were not recorded.

After disabling `com.amazon.whad` and `com.amazon.avod` and stopping `whad_cc`:
rejected packets over 150 s, a few minutes after boot, were 28: UID 1000 23,
`com.amazon.tcomm` 4, 1 unattributed (`scripts/rejected-by-uid.sh`). Ports 55442-55444
are closed, `avod` no longer runs. The `com.amazon.whad` process still runs despite
being disabled (persistent app), now without daemon or ports. Paladin, Settings and
minitv were checked after the reboot.

First WLAN run, 2026-10-31: connected, DNS only to the router, NTP to
two public servers, no TCP to public addresses, 327 packets rejected (226 v4,
101 v6) in the first minutes. Logs are in `wlan-logs/`.

---

## 14. Repeating this on another cronos unit

1. Unlock with amonet-cronos (XDA thread), ending in TWRP.
2. Host: `scripts/udev-rule.sh`.
3. TWRP: `scripts/backup-emmc.sh --second-dump` (keep the backup read-only).
4. TWRP: `scripts/install-boot-root.sh boot-root.zip`.
5. Reboot to Fire OS; confirm `adb shell id` is root.
6. `scripts/skip-setup.sh`.
7. `scripts/fix-settings-app.sh` (adjust `--pfm/--locale` if not DE/de-DE).
8. Optional: `scripts/install-minitv.sh` (installs the APK with `-g`, pushes
   `playlist.m3u`, starts it).
9. `scripts/install-amazon-block.sh` (+ `install-amazon-hosts.sh`).
10. `scripts/privacy-hardening.sh` (camera and mDNS/Avahi stay enabled).
11. `scripts/verify-device.sh`; reboot once and re-run it to prove persistence.

Expected device differences: serial, eMMC serial/date, partition timestamps,
`/data` contents. Everything else (partition layout, boot-root payload,
bundle assets) is the same for cronos units on the same Fire OS line.

---

## 15. Gotchas learned

- TWRP's `dd` is toybox: **`bs=1M` is rejected**, use `bs=1048576`.
- `adb exec-out` for binary stdout; `adb shell` allocates a pty and can
  translate CR/LF. For binary stdin use `adb shell -T`.
- `/dev/block/by-name/lk` is `/dev/null` under amonet; the real bootloader is
  `lk_real` (p3). TWRP maps `recovery` and `swdl` both to p11 while the GPT has
  `recovery`=p10, `swdl`=p11 - use GPT numbers.
- The settings app needs a bundle extracted from its own APK assets; the
  “alternate” location `/sdcard/tarBundle` is normally empty on this unit.
- `iptables` at boot: use `-w` / `iptables-restore -w --noflush`; single
  appends lose races and vanish silently.
- SELinux: `init`, `adbd` and `su` - only `adbd`/`su` are permissive; init
  cannot run iptables. A service with `seclabel u:r:adbd:s0` can.
- `pm list packages -U` does not exist on Android 7; read
  `/data/system/packages.list`.
- The device reports model `Echo_Show_5`; cronos is the 2nd gen (2021).
- The RTC is not network-synced while offline; the clock screen shows a wrong
  date/time until NTP is reachable (Amazon NTP is hostname-blocked by design).
- Strict allowlist consequences: new user apps are blocked until their UID is
  added to `/system/etc/amazonblock.allow`; the UI may warn about "no internet"
  because the connectivity check runs as UID 1000.
- A firewall started at `sys.boot_completed` leaves about 36 s of open network
  at boot. Use `exec` in `on post-fs-data`; `start` would race class main.
- The first NTP sync jumped the clock from 2021 to 2026, which ended a watcher
  timed with `date`. Time long-running device scripts with `/proc/uptime`.
- NTP goes to `2.android.pool.ntp.org`; one server seen appeared to be in AWS address space (volunteer pool hosts can be). Allowed by
  design: root and system may reach any address on udp 123.
- `tail -1` on this device prints 10 lines from a pipe. Use `tail -n 1`.
- `pm disable-user` is only written to `package-restrictions.xml` a few seconds
  later. An immediate reboot silently loses it; wait 20 s.
- Packages listed as disabled can still show running processes
  (`com.amazon.whisperjoin.middleware`, `com.amazon.alexa.beaconbroadcaster`
  were seen after disabling). Do not rely on `pm disable-user` alone.
- Camera and Avahi/mDNS are deliberately kept so video conferencing and local
  discovery work; use `privacy-hardening.sh --disable-camera` only if needed.

---

## 16. Lessons from dumbtv

[dumbtv](https://github.com/humblemuzzu/dumbtv) debloats Fire TV over adb
without root. Read from its README, `docs/HOW-IT-WORKS.md` and package lists;
its scripts were not reviewed or run.

- It lists `com.amazon.device.metrics`, `.crashmanager`, `.rdmapplication`,
  `.software.ota` and `.ota.override` as protected on Fire TV. They are disabled
  here because we are root. Worth knowing if a future Fire OS refuses this.
- It also treats disabled packages whose processes survive, as seen here.
- Two telemetry packages from its list were present and enabled on this unit and
  are now disabled: `com.amazon.dp.logger`, `com.amazon.wirelessmetrics.service`.
- It notes that Fire OS rewrites some settings at boot, so settings need
  checking after a reboot. `verify-device.sh` does this and they held here.
- Its Fire OS 6 DNS filter is a local VPN because it cannot write `/system`;
  its host list (about 400 names, HaGeZi Amazon tracker list) is larger than
  our 57. Worth considering to reduce DNS leaks in §9.1, optional.
- Not applicable: appops and Data Saver restrictions (by its own docs they do
  not stop uploads on unmetered networks), and its launcher workarounds (we can
  set HOME directly).

---

## 16a. Web research on the rejected apps (2026-10-31)

- `com.amazon.whad`: a 2018 talk, "Breaking Smart Speakers", describes the Whole
  Home Audio Daemon: tcp 55442 (audio cache), tcp 55443 (control), udp 55444 (time
  sync), respawned after a crash. Confirmed here: tcp 55442 and 55443 listen on
  0.0.0.0. Replies from this UID are rejected, so they should not answer on the
  LAN; not tested.
- `com.amazon.tcomm`: only seen in community disable lists (Fire TV Stick 7.6.2.4).
  Purpose not documented.
- `amazon.speech.sim`: an APK mirror lists it as the Alexa app; in a Fire HD 10
  disable list. Effect on an Echo not documented.
- `com.amazon.avod`: disable command documented for Fire tablets, no Echo report.
- `com.amazon.aria`: no source found. Its manifest labels it AriaRuntime 1.7.73.0 and
  includes `com.amazon.apl.android` and a voice service permission, so it appears
  to be the runtime for Alexa on-screen content (APL).
- XDA cronos thread: `pm disable-user` on the newer Paladin ("Ambient Home") did
  not stick for one user. Not seen here, but re-check disabled packages.
- TECHO5 replaces Fire OS with Alpine Linux on Echo Show 5 1st and 2nd gen, which
  would remove everything Amazon but also Android apps such as minitv.

---

## 17. Timeline (2026-10-31, CET)

| Time | Event |
|---|---|
| 12:28 | backup started in TWRP |
| 12:34–12:50 | dump 1 (7,818,182,656 B), verified |
| 12:53–13:09 | dump 2, `cmp` identical |
| 12:54–12:56 | boot areas, partition split, `/data` extraction, `e2fsck` clean |
| 13:31 | `boot-root.zip` installed to `mmcblk0p9` |
| 13:38–13:41 | Fire OS booted, root adb, OOBE skipped, launcher up |
| 14:03–14:06 | Settings crash diagnosed and fixed (bundle from APK assets) |
| 14:1x | minitv installed with `-g`, playlist pushed, launched |
| 14:1x | first per-UID firewall applied (146 UIDs), updates disabled |
| 14:2x | reboot test: 3 rules lost to xtables race |
| 14:3x | atomic `iptables-restore -w` + boot_completed trigger installed |
| 14:3x | reboot test: 146/146 IPv4 and IPv6 rules, updates disabled, WLAN off |
| 14:4x | strict allowlist (user apps + DNS/DHCP/NTP/captive/ICMP) installed, telemetry/smarthome daemons stopped |
| 14:4x | privacy hardening applied; camera, avahi and mdnsd kept enabled on request |
| 15:xx | firewall moved to `exec` in `post-fs-data` (4.96 s), OUTPUT policy DROP, fail closed via netd, captive-portal rule removed, DNS/DHCP/NTP limited to root and system, adb over WLAN opened to private addresses, Bluetooth really off |
| 15:xx | `com.amazon.dp.logger`, `.wirelessmetrics.service`, `.whad`, `.avod` disabled, `whad_cc` stopped at every boot; WLAN on, reboot tested |
| 19:xx | minitv model unpacking fixed; hands-free "Alexa, <channel>" working with Amazon's engine in `pryonwake` |
| 21:xx | source, gain and normalization tests finished; results in MIC-AND-HOTWORD.md section 0 |
| 22:xx | `knight.hds` disabled (4.8 W to 3.9 W), sound returns when a command is done, "Beenden" and "Ausschalten" |
