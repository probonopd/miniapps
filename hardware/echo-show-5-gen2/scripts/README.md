# Scripts

All scripts are host-side Bash unless noted. `ANDROID_SERIAL=<serial>` selects a
device if several are attached; otherwise the single adb device is used.

| Script | When | What it does |
|---|---|---|
| `udev-rule.sh` | host, once | installs `/etc/udev/rules.d/51-amazon-echo-adb.rules` for USB `1949:01e8` (Fire OS adb) |
| `udevil-automount.sh` | host | fixes `noexec` in `/etc/udev/udevil.conf`, restarts `devmon`, mounts removable media |
| `backup-emmc.sh` | TWRP | full eMMC backup with verification, boot areas, partition split, `/data` tree, hashes; `--second-dump` adds a comparison dump |
| `install-boot-root.sh` | TWRP | installs `boot-root.zip` into the boot partition and verifies the read-back |
| `skip-setup.sh` | Fire OS | marks provisioned, disables the OOBE wizard, starts the Echo launcher |
| `fix-settings-app.sh` | Fire OS | restores the Settings config bundle from the APK's assets (fixes the `null/preference-settings.json` crash) |
| `install-amazon-block.sh` | Fire OS | regenerates the allow list from user-installed apps and installs the strict allowlist + telemetry shutdown |
| `install-amazon-hosts.sh` | Fire OS | blackholes Amazon/Alexa/OTA hostnames in `/system/etc/hosts` (idempotent) |
| `privacy-hardening.sh` | Fire OS | location off, scanning/reporting off, telemetry packages disabled, daemons stopped; camera + mDNS stay on (`--disable-camera` optional) |
| `install-minitv.sh` | Fire OS | installs the APK with `-g`, pushes the playlist, starts the app |
| `uninstall-amazon-block.sh` | Fire OS | removes firewall files/chains, optionally re-enables update packages |
| `verify-device.sh` | Fire OS | checks root adb, firewall counts, DROP policy, early load, disabled updates, setup flags, Settings bundle, minitv |
| `watch-wlan.sh [minutes]` | Fire OS | watches accepted flows over USB adb, switches WLAN off on the first flow to a public address other than DNS/DHCP/NTP |
| `rejected-by-uid.sh [seconds]` | Fire OS | counts rejected packets per app with a temporary chain, then removes it |
| `install-echowake.sh [manifest]` | Fire OS | builds and installs hands-free voice: Amazon wake word, then minitv voice command; uses minitv's `build/libs` from the miniapps tree (run `minitv/build.sh` once; `MINITV_LIBS` overrides) |
| `mic-capture.sh [seconds] [out.raw]` | Fire OS | stops audioserver briefly, records the raw 4-channel mic PCM, prints per-channel statistics |
| `generate-allow.py`, `generate-uids.py` | host helpers |
| `split-partitions.py` | host | splits a full eMMC image via its GPT (CRC-checked) |
| `lib.sh` | - | shared helpers; sourced by the others |

## Typical sequence for a new unit

```sh
./scripts/udev-rule.sh                     # once per host
# boot TWRP (power off, hold Volume Up, apply power)
./scripts/backup-emmc.sh --second-dump
./scripts/install-boot-root.sh boot-root.zip
# reboot into Fire OS
./scripts/skip-setup.sh
./scripts/fix-settings-app.sh
./scripts/install-amazon-block.sh
./scripts/install-amazon-hosts.sh
./scripts/privacy-hardening.sh
./scripts/verify-device.sh
```

`backup-emmc.sh` and `install-boot-root.sh` require TWRP; the others require the
booted Fire OS with root adb.
