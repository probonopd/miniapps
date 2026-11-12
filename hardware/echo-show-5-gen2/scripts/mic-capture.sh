#!/bin/bash
# Record the raw 4-channel mic PCM and print per-channel statistics.
#
#   ./mic-capture.sh [seconds] [out.raw]    default 8 s, temporary file
#
# audioserver holds the PCM all the time, so it is stopped for the recording:
# sound and the wake word stop too, and start again afterwards. The recording
# is room audio. It is pulled to this host only and deleted unless out.raw is
# given. Build the tool first: tools/build-echocap.sh
source "$(dirname "$0")/lib.sh"

SEC=${1:-8}
OUT=${2:-}
require_device
require_root
require_booted
[ -x "$ROOT_DIR/tools/echocap" ] || die "tools/echocap missing, run tools/build-echocap.sh"

KEEP=$OUT
[ -n "$OUT" ] || OUT=$(mktemp)
cleanup() {
    dsh 'start audioserver; rm -f /data/local/tmp/mic.raw /data/local/tmp/echocap' >/dev/null 2>&1 || true
    [ -n "$KEEP" ] || rm -f "$OUT"
}
trap cleanup EXIT

adbs push "$ROOT_DIR/tools/echocap" /data/local/tmp/echocap >/dev/null
note "privacy state (0 = mics on): $(dsh 'cat /sys/devices/platform/gpio-privacy/state')"
dsh 'chmod 755 /data/local/tmp/echocap
     stop audioserver; sleep 1
     tinymix "ADC_A Left Ip Select ADC_A DIF1_L switch" 1
     tinymix "ADC_A Right Ip Select ADC_A DIF1_R switch" 1
     tinymix "ADC_A MICPGA Volume Ctrl" 40
     tinymix "ADC_A Digital Volume Control" 88'
note "recording $SEC s"
dsh "/data/local/tmp/echocap 22 /data/local/tmp/mic.raw $SEC 4 16000 257 10"
adbs pull /data/local/tmp/mic.raw "$OUT" >/dev/null
python3 "$ROOT_DIR/tools/analyze-capture.py" "$OUT"
