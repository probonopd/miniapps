# boot-root install log - Echo Show 5 2nd gen (cronos)

Companion to `README.md`. All writes described here are on the **device**, not
in the backup. The backup `backup-20261031-122840/` is read-only and was not
modified by the install.

## 1. Summary

| | |
|---|---|
| Installed | `boot-root.zip` → `boot` partition (`/dev/block/mmcblk0p9`) |
| When | 2026-10-31 |
| Method | TWRP `twrp install` after `adb push` (guide's fastboot path equivalent) |
| Result | “Boot image flashed successfully” / “Installation complete” |
| Verification | read-back of the flashed region matches `boot.img` exactly |
| Rebooted yet | **No** - device is still in TWRP |

## 2. The archive

| | |
|---|---|
| File | `boot-root.zip` |
| Size | 8,008,566 bytes |
| MD5 | `cd8263d36ca7e6f6af943f2558da8239` |
| Contents | `META-INF/com/google/android/update-binary` (1,324 B) |
| | `META-INF/com/google/android/updater-script` (8 B, `# Dummy`) |
| | `boot.img` (8,671,232 B, MD5 `77028e64e9fc90abaa70e3f72362d81a`) |

The update-binary only runs in recovery (it exits if `zygote` is running),
sets SELinux permissive, extracts `boot.img` and writes it with:

```sh
dd if=$INSTALLER/boot.img of=$BOOT_BLOCK bs=4096
```

`BOOT_BLOCK` is resolved from
`/dev/block/platform/*/by-name` (here
`/dev/block/platform/bootdevice/by-name/boot` → `mmcblk0p9`).

## 3. What was done

```sh
adb push boot-root.zip /tmp/boot-root.zip          # md5 verified on device
adb shell 'twrp install /tmp/boot-root.zip'
```

Output:

```
Installing zip file '/tmp/boot-root.zip'
Unmounting System...
Boot Image Patcher
Extracting files
Finding boot partition
Boot partition: /dev/block/platform/bootdevice/by-name/boot
Flashing boot image
Boot image flashed successfully
Installation complete
```

Verification (first 8,671,232 bytes of the partition):

```
77028e64e9fc90abaa70e3f72362d81a  /dev/block/mmcblk0p9 (read back)
77028e64e9fc90abaa70e3f72362d81a  boot.img
```

`dd` writes only the image length; the rest of the 16 MiB boot partition keeps
its previous bytes and is not used by the bootloader.

## 4. What the new boot image is

| | |
|---|---|
| Format | Android boot image, page size 2048, second stage none |
| Kernel | 6,786,596 bytes |
| Ramdisk | 1,880,632 bytes, MTK `ROOTFS` container (magic `0x58881688`) around a gzip cpio, files dated 2025-12-20 |
| Cmdline | `bootopt=64S3,32N2,32N2 buildvariant=user` |
| Boot image build | `Amazon/cronos/cronos:6.0/NS6571/6208N:user/amz-p,release-keys` |

Key `default.prop` values versus the stock boot that was replaced:

| Property | Stock (`images/p09-boot.img`) | boot-root |
|---|---|---|
| `ro.secure` | 1 | **0** |
| `ro.adb.secure` | 1 | **0** |
| `ro.debuggable` | 0 | **1** |
| `persist.sys.usb.config` | `none` | **`mtp,adb`** |
| boot image build | `NS6548/3290` | `NS6571/6208N` |

Other changes in the ramdisk:

- `/init.fosflags.sh` is replaced. The stock Amazon script enables adb only
  when the bootloader FOS flag `ADB_ON` (0x1) is set; the replacement is 3
  lines and unconditionally does:
  ```sh
  setprop persist.sys.usb.config mtp,adb
  setprop amazon.fos_flags.noadbauth 1
  ```
- `sbin/adbd` differs from stock (patched/other build), and `init.usb.rc`
  defines adbd as `start adbd` on the `sys.usb.config=...adb` triggers.
- `init.cronos.rc` (unchanged from stock) contains, on `boot`:
  ```sh
  # Enable adb over network
  setprop service.adb.tcp.port 5555
  ```

## 5. Will adb work after reboot?

**Confirmed on 2026-10-31: yes - root adb over USB, no authorization prompt**
(see section 10 for the first-boot evidence).

Reasoning (from the ramdisk and device state):

- `ro.secure=0` → adbd does not drop privileges; `adb shell` is `root`.
- `ro.adb.secure=0` → no RSA key authorization needed.
- `persist.sys.usb.config=mtp,adb` in `default.prop`; no overriding
  `persist.sys.usb.config` file exists in `/data/property`, so init applies it
  at boot and starts adbd.
- `amazon.fos_flags.noadbauth=1` is the Fire OS signal to skip adb auth.
- The XDA/guide procedure runs `adb shell` right after rebooting into Fire OS
  and expects it to work, which is what this payload is for.

Things that could still prevent it:

1. **Firmware mismatch.** Installed system is **Fire OS 6.5.4.8 (NS6548,
   Android 7.1.2, SDK 25)**; the payload is from **NS6571** (the 6.5.7.x
   line the guide targets). If kernel/ramdisk and system disagree badly, the
   device may bootloop → no adb at all. This is the main risk.
2. **FOS flags.** The guide's Windows flow also runs `fastboot oem flags 61`
   (ADB on + adb-auth-disable + console). On this unit
   `/proc/idme/fos_flags` reads **0**, i.e. that step was not done. The patched
   `init.fosflags.sh` and `ro.adb.secure=0` are meant to make it unnecessary.
3. **Framework setting.** `/data/system/users/0/settings_global.xml` has
   `adb_enabled=0`. If Fire OS's USB manager re-derives the USB function set
   from that setting after boot, adb could be switched off. The forced
   `persist.sys.usb.config` and `ro.adb.secure=0` should override this, but it
   is the part that cannot be proven without booting.
4. adb comes up late in boot; use `adb wait-for-device`, give it a minute.

### Verifying after reboot

```sh
adb wait-for-device
adb devices -l                 # serial in state "device"
adb shell id                   # expect uid=0(root)
adb shell getprop ro.build.version.name
adb shell getprop ro.debuggable        # 1
adb shell getprop ro.adb.secure        # 0
```

## 6. If it does not boot / adb does not appear

amonet remaps the buttons; recovery is still possible:

| Mode | How |
|---|---|
| TWRP / recovery | power off, hold **Volume Up**, apply power |
| Hacked fastboot | power off, hold **Volume Down**, apply power |
| Stock fastboot | power off, hold **all three buttons**, apply power |
| USBDL / unbrick | hold **Mute + Volume Down**, apply power, USB connected |

Never flash `lk`, `preloader`, `tee1`, `tee2` - permanent brick risk.

From TWRP, restore the stock boot (read-only use of the backup):

```sh
adb shell 'sync'
adb shell -T 'dd of=/dev/block/mmcblk0p9 bs=1048576' \
  < backup-20261031-122840/images/p09-boot.img
adb shell 'sync'
```

Stock boot checksum: `eedb993aa48107a2516321ad09a10a52` (MD5).

## 7. Rollback / re-install

- Rollback: command in section 6 (stock boot from the backup).
- Re-install root payload: boot TWRP and run
  `adb push boot-root.zip /tmp/ && adb shell 'twrp install /tmp/boot-root.zip'`.

## 8. What changed on the device

| Partition | Change |
|---|---|
| `boot` (`mmcblk0p9`) | new root boot image (NS6571) |
| everything else | untouched |

`/data`, `/system`, both boot areas, GPT and RPMB were not written. The zip on
the device is only in `/tmp` (tmpfs) and disappears on reboot.

## 9. Device identity correction

The device is an **Echo Show 5 2nd gen (2021)** - codename **cronos** - as
listed by the XDA thread (`cronos` = 2nd gen 2021). The model string
`Echo_Show_5` is shared with the 1st gen; the codename is the reliable
identifier. The frozen backup README (`backup-20261031-122840/README.md`)
contains the older, incorrect "not Echo Show 2nd gen" note; it is left as-is
because the backup is never modified.

## 10. First boot and setup skip (2026-10-31)

The device booted into Fire OS normally. The payload works.

### adb access

- The device re-enumerated as USB `1949:01e8` (`Lab126, Inc. Alexa`), which the
  host had no udev rule for - adb reported “no permissions”. Fixed by adding
  `/etc/udev/rules.d/51-amazon-echo-adb.rules`:

  ```
  SUBSYSTEM=="usb", ATTR{idVendor}=="1949", ATTR{idProduct}=="01e8", MODE="0666"
  ```

  then `udevadm control --reload-rules` and `udevadm trigger` (node became
  `crw-rw-rw-`).
- `adb devices` → `<serial>  device  product:cronos model:AEOCN`.
- `adb shell id` → `uid=0(root) gid=0(root) context=u:r:su:s0`.
- `sys.boot_completed=1`, `ro.debuggable=1`, `ro.adb.secure=0`,
  `ro.build.version.name=Fire OS 6.5.4.8 (NS6548/3290)`.
- `settings get global adb_enabled` → **1** (it was 0 in the backup), i.e. the
  payload switched USB debugging on by itself, as intended.
- WLAN/TCP adb was not needed and was not verified on this unit.

### Setup wizard

At first boot the device was unprovisioned and the OOBE wizard owned HOME:

- `device_provisioned=0`, `user_setup_complete=0`
- focused: `com.amazon.kindle.otter.oobe/.modules.languagePickerModule.Knight.KnightLanguagePickerActivity`
- HOME resolution: OOBE `OobeHomeActivity` (priority 100) over the real
  launcher `com.amazon.paladin/.app.MMLauncherActivity` (priority 50)

Skipping it:

```sh
adb shell settings put global device_provisioned 1
adb shell settings put secure user_setup_complete 1
adb shell pm disable-user --user 0 com.amazon.kindle.otter.oobe   # -> disabled-user
adb shell am force-stop com.amazon.kindle.otter.oobe
adb shell am start -n com.amazon.paladin/.app.MMLauncherActivity
```

Result:

- `mCurrentFocus` / `mResumedActivity` → `com.amazon.paladin/.app.MMLauncherActivity`
- HOME key stays on Paladin (OOBE stays disabled)
- screenshot 960×480 shows the Echo Show clock screen
  (“FR., 12. NOV.” - RTC not yet network-synced)

Revert (factory setup again):

```sh
adb shell pm enable --user 0 com.amazon.kindle.otter.oobe
adb shell settings put global device_provisioned 0
adb shell settings put secure user_setup_complete 0
adb reboot
```

## 11. Backup state

The backup was not modified by this install. Verified afterwards:

```
sha256sum -c SHA256SUMS     # 879/879 OK
```

Backup root: `backup-20261031-122840/` (read-only).
