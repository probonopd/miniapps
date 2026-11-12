#!/bin/bash
# Watch WLAN traffic over USB adb and switch WLAN off on the first unexpected
# connection.
#
#   ./watch-wlan.sh [minutes]    default 30; exits 2 if it had to switch WLAN off
#
# The device has no tcpdump or LOG target, so this reads nf_conntrack, which
# lists what the firewall let through. Rejected attempts never reach it; they
# are only visible as the AMZBLOCK REJECT counter, which is reported, not acted
# on: they did not leave the device.
#
# Trip: any flow to a public address, except DNS/DHCP/NTP (root and system only)
# and ICMP.
# minitv is stopped first, so its streams cannot be mistaken for anything else.
# DNS names are not visible here.
# WATCH_CT overrides the conntrack path, to test the trip with a fake table.
source "$(dirname "$0")/lib.sh"

MINUTES=${1:-30}
require_device
require_root
require_booted

LOG="$ROOT_DIR/wlan-watch-$(date +%Y%m%d-%H%M%S).log"
dsh 'am force-stop org.minitv'

# Runs on the device so that the reaction does not depend on host latency.
adb -s "$SERIAL" shell "MINUTES=$MINUTES CT=${WATCH_CT:-/proc/net/nf_conntrack} sh -s" <<'DEVICE' | stdbuf -oL tr -d '\r' | tee "$LOG"
PRIVATE='^(10\.|127\.|192\.168\.|172\.(1[6-9]|2[0-9]|3[01])\.|169\.254\.|22[4-9]\.|23[0-9]\.|255\.|0\.0\.0\.0|::1$|f[cd]|fe[89ab]|ff)'
flows() {
    sed -nE -e 's/^ipv[46] +[0-9]+ +([a-z0-9]+) +[0-9]+ [^=]*src=[^ ]+ +dst=([^ ]+) +sport=[0-9]+ +dport=([0-9]+).*/\1 \2 \3/p' \
        -e 's/^ipv[46] +[0-9]+ +(icmp[v6]*) +[0-9]+ [^=]*src=[^ ]+ +dst=([^ ]+) .*/\1 \2 0/p' \
        "$CT" | sort -u
}
rejected() {
    r4=$(iptables -w -vnxL AMZBLOCK | grep REJECT | sed -E 's/^ *([0-9]+).*/\1/')
    r6=$(ip6tables -w -vnxL AMZBLOCK | grep REJECT | sed -E 's/^ *([0-9]+).*/\1/')
    echo $((r4 + r6))
}
BASE=$(rejected); LASTR=$BASE; WAS=
: > /data/local/tmp/wlan-watch.seen
# Uptime, not the wall clock: the first NTP sync can jump the date by years.
uptime_s() { cut -d. -f1 /proc/uptime; }
END=$(( $(uptime_s) + MINUTES * 60 ))
echo "WATCH started, $MINUTES min"
while [ "$(uptime_s)" -lt "$END" ]; do
    UP=$(ip addr show wlan0 2>/dev/null | grep -c 'inet ')
    [ "$UP" != "$WAS" ] && { echo "$(date +%T) wlan0 addresses: ${UP:-0}"; WAS=$UP; }
    flows | while read -r proto dst port; do
        echo "$dst" | grep -qE "$PRIVATE" && continue
        case "$port" in
            0|53|67|68|123|546|547) tag=SEEN ;;
            *) tag=TRIP ;;
        esac
        echo "$tag $proto $dst $port"
    done > /data/local/tmp/wlan-watch.now
    grep -v -F -x -f /data/local/tmp/wlan-watch.seen /data/local/tmp/wlan-watch.now 2>/dev/null \
        | while read -r line; do echo "$(date +%T) $line"; done
    cat /data/local/tmp/wlan-watch.now >> /data/local/tmp/wlan-watch.seen
    if grep -q '^TRIP' /data/local/tmp/wlan-watch.now; then
        svc wifi disable
        ip link set wlan0 down 2>/dev/null
        echo "$(date +%T) TRIPPED: WLAN switched off"
        rm -f /data/local/tmp/wlan-watch.now /data/local/tmp/wlan-watch.seen
        exit 2
    fi
    R=$(rejected)
    [ "$R" != "$LASTR" ] && { echo "$(date +%T) rejected so far: $((R - BASE))"; LASTR=$R; }
    sleep 1
done
rm -f /data/local/tmp/wlan-watch.now /data/local/tmp/wlan-watch.seen
echo "WATCH finished, no unexpected flows"
exit 0
DEVICE
RC=${PIPESTATUS[0]}
note "log: $LOG"
exit "$RC"
