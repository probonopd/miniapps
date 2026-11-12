#!/bin/bash
# Install and start minitv (the TV front end) on the Echo Show.
#
#   ./install-minitv.sh path/to/minitv.apk [path/to/playlist.m3u]
#
# The playlist must exist at /sdcard/playlist.m3u, otherwise minitv hands over
# to another launcher on start (documented behaviour of the app).
source "$(dirname "$0")/lib.sh"

APK=${1:?usage: install-minitv.sh path/to/minitv.apk [path/to/playlist.m3u]}
PLAYLIST=${2:-$(dirname "$APK")/playlist.m3u}

[ -f "$APK" ] || die "APK not found: $APK"
[ -f "$PLAYLIST" ] || die "playlist not found: $PLAYLIST"

require_device
require_root
require_booted

note "installing $(basename "$APK") with -g (grants storage permission)"
adbs install -r -g "$APK"

note "pushing playlist"
adbs push "$PLAYLIST" /sdcard/playlist.m3u >/dev/null

note "starting minitv"
dsh 'am start -n org.minitv/.MainActivity' >/dev/null 2>&1 || true
sleep 4

FOCUS=$(dsh 'dumpsys window 2>/dev/null | grep -m1 mCurrentFocus')
case "$FOCUS" in
    *org.minitv*) note "minitv is running: $FOCUS";;
    *) note "current focus: $FOCUS (if another launcher opened, check the playlist)";;
esac

note "package: $(dsh 'pm path org.minitv' | head -1)"
