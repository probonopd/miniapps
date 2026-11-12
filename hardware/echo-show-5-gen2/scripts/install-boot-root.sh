#!/bin/bash
# Install boot-root.zip (the rooted boot image) through TWRP.
#
#   ./install-boot-root.sh [path/to/boot-root.zip]
#
# Writes only the boot partition (mmcblk0p9).  The stock boot is preserved in
# the full backup (images/p09-boot.img); see DOCUMENTATION.md for the rollback.
source "$(dirname "$0")/lib.sh"

ZIP=${1:-$ROOT_DIR/boot-root.zip}
[ -f "$ZIP" ] || die "zip not found: $ZIP"

require_device
require_root
require_recovery

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
unzip -p "$ZIP" boot.img > "$TMP/boot.img" || die "zip has no boot.img"
SIZE=$(stat -c%s "$TMP/boot.img")
WANT=$(md5sum "$TMP/boot.img" | awk '{print $1}')

confirm "This flashes boot.img ($SIZE bytes, md5 $WANT) to /dev/block/mmcblk0p9 on $SERIAL."

adbs push "$ZIP" /tmp/boot-root.zip >/dev/null
dsh 'twrp install /tmp/boot-root.zip'
dsh 'sync'

GOT=$(dsh "dd if=/dev/block/mmcblk0p9 bs=4096 count=$(( (SIZE + 4095) / 4096 )) 2>/dev/null | md5sum" | awk '{print $1}')
[ "$GOT" = "$WANT" ] || die "read-back mismatch: got $GOT want $WANT"
note "boot partition verified (first $SIZE bytes match boot.img)"
note "reboot to test: adb shell reboot"
