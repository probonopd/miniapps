# Echo Show 5 (2nd gen, 2021) - "cronos"

Jailbreak, backup, root, setup-skip, settings fix, minitv and the Amazon
network block for this device, plus reusable scripts for other cronos units.

**Full details: [DOCUMENTATION.md](DOCUMENTATION.md)**
**Mics and hotword: [MIC-AND-HOTWORD.md](MIC-AND-HOTWORD.md)**
**Backups** (`scripts/backup-emmc.sh`) are personal to one unit and stay out of git.

## State (2026-10-31)

| | |
|---|---|
| Device | Echo Show 5 2nd gen (2021), `cronos` |
| OS | Fire OS 6.5.4.8 (NS6548/3290), TWRP 3.7.0_9-0 (amonet) |
| Root | boot-root image installed, adb shell is `uid=0(root)` |
| Setup | OOBE disabled, provisioned, Echo launcher (Paladin) is HOME |
| Settings | crash fixed (bundle restored from the APK assets) and working |
| minitv | `org.minitv` installed, playlist at `/sdcard/playlist.m3u`, launched |
| Network | WLAN on, reboot tested; strict egress allowlist loaded at 5 s, before netd and zygote, with OUTPUT policy DROP: only user-installed app UIDs, DNS/DHCP/NTP for root and system, ICMP and adb replies to private addresses pass. Telemetry/smarthome daemons stopped, OTA/Appstore disabled, Amazon hostnames blackholed |
| Privacy | location off, Bluetooth off, scanning/error reporting off, telemetry packages disabled; camera and mDNS/Avahi kept for video conferencing and discovery |
| Voice | "Alexa", beep, channel name switches channels: Amazon wake word in `pryonwake`, then minitv Vosk, offline; "Beenden" and "Ausschalten" go home - see MIC-AND-HOTWORD.md |
| Power | 3.9 W with minitv playing after disabling the camera presence service `knight.hds` (was 4.8 W), no power sensor on the device |
| adb over WLAN | open by decision: unauthenticated root for the whole LAN, firewall lets replies out to private addresses only; tested working - see DOCUMENTATION §11 |

## Layout

```
README.md                this file
DOCUMENTATION.md         everything: device, backup, root, fixes, firewall, repeat procedure
INSTALL-LOG.md           boot-root install record and first boot
amazonblock/             device-side firewall files + notes
MIC-AND-HOTWORD.md       mic hardware, audio path, Pryon, plan for our own hotword
tools/                   echocap raw capture tool and capture analysis
scripts/                 reusable host scripts (backup, root, setup, settings, firewall, verify)
backup-20261031-122840/  full eMMC backup, hashes, tools - READ-ONLY
amonet-cronos-v2.0.1.zip amonet exploit package
boot-root.zip            rooted boot payload (installed)
amonet/                  amonet scripts and images
.claude/skills/          project skills: boot firewall, traffic accounting, wake word
```

## Rules

1. The backup directory is **read-only**. Verify with
   `backup-20261031-122840/tools/verify.sh` (879/879 OK).
2. Never flash/modify `lk`, `preloader`, `tee1`, `tee2` - permanent brick risk.
3. adb root means anyone with the cable (and, with 5555 open, the LAN) has root.

## Quick start for another unit

```sh
./scripts/udev-rule.sh
# boot TWRP: power off, hold Volume Up, apply power
./scripts/backup-emmc.sh --second-dump
./scripts/install-boot-root.sh boot-root.zip
# reboot into Fire OS
./scripts/skip-setup.sh
./scripts/fix-settings-app.sh
./scripts/install-amazon-block.sh
./scripts/install-amazon-hosts.sh
./scripts/install-echowake.sh
./scripts/verify-device.sh
```

## Recovery

| Mode | How |
|---|---|
| TWRP | power off, hold **Volume Up**, apply power |
| Hacked fastboot | hold **Volume Down** at power |
| Stock fastboot | hold **all three** at power |
| USBDL / unbrick | hold **Mute + Volume Down** at power, USB connected |

## References

- XDA: [UNLOCK][ROOT][TWRP][UNBRICK] Echo Show 5 2nd Gen 2021 (cronos)
  - https://xdaforums.com/t/4772596/
- Derek Seaman, “Home Assistant: The Complete Echo Show Jailbreak Guide”
  - https://www.derekseaman.com/2025/11/home-assistant-hacking-your-echo-show-5-and-8.html
- Gamer92000/echo-dot-assist `lockdown.sh` (ideas for atomic iptables loading,
  boot-time races, adb-over-WLAN exposure)
  - https://github.com/Gamer92000/echo-dot-assist
