#!/bin/bash
#
# Copyright (c) 2026 Simon Peter
#
# SPDX-License-Identifier: MIT
#
# Headless end-to-end test of the helper on this machine: two helpers (built by
# helper/build.sh host) call each other over the LAN address with H.264 video
# and Opus audio through sockvideo and sockaudio, driven over ctrl_stdio.
# fakeapp.py plays the app's part on both sides. Exits non-zero on failure.
#
#   tests/call-test.sh [workdir]
#
# PARAMS_A and PARAMS_B add account parameters, e.g. PARAMS_A=';medianat=ice'.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
B=$here/../helper/out/host/baresip
W=${1:-$(mktemp -d)}
IP=$(ip -4 route get 1.1.1.1 | grep -oP 'src \K\S+')
SECS=12
rm -rf "$W" && mkdir -p "$W"

ffmpeg -hide_banner -loglevel error -y -f lavfi -i testsrc=size=640x480:rate=30 -t 4 \
	-c:v libx264 -profile:v baseline -pix_fmt yuv420p -g 30 -bsf:v h264_metadata=aud=insert -f h264 "$W/cam.h264"

mk() { # name port
	mkdir -p "$W/$1"
	cat > "$W/$1/config" <<EOF
sip_listen $IP:$2
audio_source sockaudio,mctest-$1.mic
audio_player sockaudio,mctest-$1.spk
ausrc_channels 1
auplay_channels 1
video_source sockvideo,mctest-$1.cam
video_display fakevideo,nil
sockvideo_display mctest-$1.disp
video_size 640x480
video_fps 30
video_bitrate 1000000
EOF
	for m in opus auconv auresamp sockaudio sockvideo fakevideo ice stun srtp dtls_srtp account contact menu ctrl_stdio; do
		echo "module $m.so" >> "$W/$1/config"
	done
	echo "<sip:$1@$IP:$2>;regint=0;answermode=auto$3" > "$W/$1/accounts"
	touch "$W/$1/contacts"
}
mk a 5080 "${PARAMS_A:-}"
mk b 5082 "${PARAMS_B:-}"

FAKEAPP_SECONDS=$((SECS + 10)) python3 -I "$here/fakeapp.py" mctest-a "$W/cam.h264" "$W/a/out" 660 2> "$W/a/fakeapp.err" &
FAKEAPP_SECONDS=$((SECS + 10)) python3 -I "$here/fakeapp.py" mctest-b "$W/cam.h264" "$W/b/out" 440 2> "$W/b/fakeapp.err" &
sleep 1
sleep $((SECS + 8)) | "$B" -f "$W/b" > "$W/b/stdout" 2> "$W/b/stderr" &
sleep 1
{
	echo '{"command":"dial","params":"sip:b@'"$IP"':5082","token":"1"}'
	sleep "$SECS"
	echo '{"command":"hangup","token":"2"}'
	sleep 2
} | "$B" -f "$W/a" > "$W/a/stdout" 2> "$W/a/stderr"
wait

fail=0
check() { if eval "$2"; then echo "ok   $1"; else echo "FAIL $1"; fail=1; fi; }
frames() { ffprobe -v error -count_frames -select_streams v:0 -show_entries stream=nb_read_frames,width,height \
	-of csv=p=0 "$1" 2>/dev/null | head -1; }
for s in a b; do
	f=$(frames "$W/$s/out/display.h264" || true)
	echo "$s display: $f (width,height,frames)"
	check "$s decodes video" '[[ "${f##*,}" =~ ^[0-9]+$ ]] && [ "${f##*,}" -ge $((SECS * 20)) ]'
	check "$s got speaker audio" '[ "$(stat -c %s "$W/$s/out/speaker.raw" 2>/dev/null || echo 0)" -gt 500000 ]'
done
check "a sees CALL_ESTABLISHED" 'grep -q "\"type\":\"CALL_ESTABLISHED\"" "$W/a/stdout"'
check "b sees CALL_INCOMING" 'grep -q "\"type\":\"CALL_INCOMING\"" "$W/b/stdout"'
check "a dial response ok" 'grep -q "\"response\":true,\"ok\":true.*\"token\":\"1\"" "$W/a/stdout"'
echo "workdir: $W"
exit $fail
