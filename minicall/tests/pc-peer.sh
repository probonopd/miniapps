#!/bin/bash
#
# Copyright (c) 2026 Simon Peter
#
# SPDX-License-Identifier: BSD-2-Clause
#
# A SIP video phone on this machine for tests against a device: the host helper
# with fakeapp.py (test pattern camera in H.264, 440 Hz microphone). What it
# receives lands in WORKDIR/out (display.h264, speaker.raw).
#
#   tests/pc-peer.sh WORKDIR SECONDS [sip:uri-to-dial]
#
# Without an address it waits for a call (answered at once) for SECONDS.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
B=$here/../helper/out/host/baresip
W=$1 SECS=$2 DIAL=${3:-}
IP=$(ip -4 route get 1.1.1.1 | grep -oP 'src \K\S+')
rm -rf "$W" && mkdir -p "$W"

ffmpeg -hide_banner -loglevel error -y -f lavfi -i testsrc=size=640x480:rate=30 -t 4 \
	-c:v libx264 -profile:v baseline -pix_fmt yuv420p -g 30 -bsf:v h264_metadata=aud=insert -f h264 "$W/cam.h264"
cat > "$W/config" <<EOF
sip_listen $IP:5070
audio_source sockaudio,mcpeer.mic
audio_player sockaudio,mcpeer.spk
ausrc_channels 1
auplay_channels 1
video_source sockvideo,mcpeer.cam
video_display fakevideo,nil
sockvideo_display mcpeer.disp
video_size 640x480
video_fps 30
video_bitrate 1000000
EOF
for m in opus g711 auconv auresamp sockaudio sockvideo fakevideo ice stun srtp dtls_srtp account contact menu ctrl_stdio; do
	echo "module $m.so" >> "$W/config"
done
echo "<sip:pc@$IP:5070>;regint=0;answermode=auto;mediaenc=srtp;medianat=ice;stunserver=\"stun:stun.l.google.com:19302\"" > "$W/accounts"
touch "$W/contacts"

FAKEAPP_SECONDS=$((SECS + 5)) python3 -I "$here/fakeapp.py" mcpeer "$W/cam.h264" "$W/out" 440 2> "$W/fakeapp.err" &
sleep 1
{
	if [ -n "$DIAL" ]; then
		sleep 1
		echo '{"command":"dial","params":"'"$DIAL"'","token":"1"}'
	fi
	sleep "$SECS"
	echo '{"command":"hangup","token":"2"}'
	sleep 1
} | "$B" -f "$W" > "$W/stdout" 2> "$W/stderr"
wait
f=$(ffprobe -v error -count_frames -select_streams v:0 -show_entries stream=nb_read_frames,width,height \
	-of csv=p=0 "$W/out/display.h264" 2>/dev/null | head -1 || true)
echo "received video: ${f:-none} (width,height,frames)"
grep -o '"type":"CALL_[A-Z]*"' "$W/stdout" | sort | uniq -c | tr '\n' ' '; echo
