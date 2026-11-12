#!/bin/bash
# Install hands-free voice control: Amazon's wake word engine starts minitv's voice command.
#
#   ./install-echowake.sh [manifest]
#
# Default manifest: /system/local/models/keyword/de-DE/ALEXA/pryon.manifest
# Other words: replace ALEXA with COMPUTER, ECHO or AMAZON.
source "$(dirname "$0")/lib.sh"

MANIFEST=${1:-/system/local/models/keyword/de-DE/ALEXA/pryon.manifest}
require_device
require_root
require_booted
[ "$(dsh "ls $MANIFEST 2>/dev/null")" = "$MANIFEST" ] || die "no manifest $MANIFEST"

ANDROID_SERIAL=$SERIAL "$ROOT_DIR/tools/build-pryonwake.sh"
"$ROOT_DIR/tools/build-micpipe.sh"

TMP=$(mktemp); trap 'rm -f "$TMP"' EXIT
echo "$MANIFEST" > "$TMP"
dsh 'stop echowake' >/dev/null 2>&1 || true
remount_rw
adbs push "$ROOT_DIR/tools/pryonwake"          /system/bin/pryonwake >/dev/null
adbs push "$ROOT_DIR/tools/micpipe.jar"        /system/framework/micpipe.jar >/dev/null
adbs push "$ROOT_DIR/echowake/echowake.sh"     /system/bin/echowake.sh >/dev/null
adbs push "$ROOT_DIR/echowake/echowake.rc"     /system/etc/init/echowake.rc >/dev/null
adbs push "$TMP"                               /system/etc/echowake.conf >/dev/null
dsh 'chown root:root /system/bin/pryonwake /system/bin/echowake.sh /system/framework/micpipe.jar /system/etc/init/echowake.rc /system/etc/echowake.conf
     chmod 755 /system/bin/pryonwake /system/bin/echowake.sh
     chmod 644 /system/framework/micpipe.jar /system/etc/init/echowake.rc /system/etc/echowake.conf
     chcon u:object_r:system_file:s0 /system/bin/pryonwake /system/bin/echowake.sh /system/framework/micpipe.jar /system/etc/init/echowake.rc /system/etc/echowake.conf'
remount_ro
# Keeps minitv's speech model loaded; a reload would take longer than anyone waits.
dsh 'touch /sdcard/voice-foreground'
adbs push "$ROOT_DIR/echowake/voice-apps" /sdcard/voice-apps >/dev/null
note "installed; init reads the rc file at boot, so reboot once"
