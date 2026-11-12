#!/bin/bash
# Restore the Fire OS Settings app configuration bundle that the Echo never
# extracted, so com.android.settings/.Settings no longer crashes with
#   FileNotFoundException: null/preference-settings.json
#
#   ./fix-settings-app.sh [--pfm DE] [--locale de-DE]
#
# The bundle files come from the KnightSettings APK's own assets, so no network
# is needed.
source "$(dirname "$0")/lib.sh"

PFM=DE
LOCALE=de-DE
while [ $# -gt 0 ]; do
    case "$1" in
        --pfm) PFM=$2; shift 2;;
        --locale) LOCALE=$2; shift 2;;
        *) die "unknown argument: $1";;
    esac
done

require_device
require_root
require_booted

APK=/system/priv-app/KnightSettings/KnightSettings.apk
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT

note "pulling $APK"
adbs pull "$APK" "$TMP/KnightSettings.apk" >/dev/null

if ! unzip -l "$TMP/KnightSettings.apk" "assets/${PFM}.tgz" >/dev/null 2>&1; then
    echo "available bundle assets:"
    unzip -l "$TMP/KnightSettings.apk" 'assets/*.tgz' | awk '{print "  " $4}'
    die "assets/${PFM}.tgz not found; pass --pfm"
fi
if ! unzip -l "$TMP/KnightSettings.apk" "assets/${LOCALE}.tgz" >/dev/null 2>&1; then
    die "assets/${LOCALE}.tgz not found; pass --locale"
fi

mkdir -p "$TMP/bundle"
( cd "$TMP/bundle" && unzip -p "$TMP/KnightSettings.apk" "assets/${PFM}.tgz" | tar xzf - )
( cd "$TMP/bundle" && unzip -p "$TMP/KnightSettings.apk" "assets/${LOCALE}.tgz" | tar xzf - )
[ -f "$TMP/bundle/preference-settings.json" ] || die "bundle has no preference-settings.json"

confirm "This writes the Settings bundle and shared preference on $SERIAL."

dsh 'am force-stop com.android.settings
     mkdir -p /data/data/com.android.settings/files/bundle_a'
adbs push "$TMP/bundle/." /data/data/com.android.settings/files/bundle_a/ >/dev/null
dsh 'chown -R system:system /data/data/com.android.settings/files/bundle_a
     chmod 771 /data/data/com.android.settings/files/bundle_a
     chmod 660 /data/data/com.android.settings/files/bundle_a/*
     restorecon -R /data/data/com.android.settings/files/bundle_a 2>/dev/null || true'

PREFS=/data/data/com.android.settings/shared_prefs/com.amazon.settings.CONFIG_BUNDLE_STORE.xml
adbs pull "$PREFS" "$TMP/prefs.xml" >/dev/null 2>&1 || printf "<?xml version='1.0' encoding='utf-8' standalone='yes' ?>\n<map>\n</map>\n" > "$TMP/prefs.xml"
python3 - "$TMP/prefs.xml" "$TMP/prefs.new" <<'PY'
import sys
src, dst = sys.argv[1:3]
s = open(src).read()
key = '<string name="bundle_location">/data/data/com.android.settings/files/bundle_a</string>'
if 'name="bundle_location"' in s:
    import re
    s = re.sub(r'<string name="bundle_location">[^<]*</string>', key, s)
else:
    s = s.replace('</map>', '    ' + key + '\n</map>')
open(dst, 'w').write(s)
PY
adbs push "$TMP/prefs.new" "$PREFS" >/dev/null
dsh "chown system:system $PREFS; chmod 660 $PREFS; restorecon $PREFS 2>/dev/null || true"

dsh 'am start -n com.android.settings/.Settings' >/dev/null 2>&1 || true
sleep 4
FOCUS=$(dsh 'dumpsys window 2>/dev/null | grep -m1 mCurrentFocus')
case "$FOCUS" in
    *com.android.settings*) note "Settings is running: $FOCUS";;
    *) die "Settings did not come to the front: $FOCUS";;
esac
