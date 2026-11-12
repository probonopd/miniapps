#!/bin/bash
# Show which apps send the packets the firewall rejects.
#
#   ./rejected-by-uid.sh [seconds]    default 180
#
# Adds a temporary chain before REJECT with one counting rule per UID, so the
# firewall stays closed while it runs, and removes it again. Destinations are
# not recorded: the device has no LOG target.
source "$(dirname "$0")/lib.sh"

SECONDS_TO_COUNT=${1:-180}
require_device
require_root
require_booted

TMP=$(mktemp -d)
cleanup() {
    dsh 'for T in iptables ip6tables; do
             $T -w -D AMZBLOCK -j AMZACCT; $T -w -F AMZACCT; $T -w -X AMZACCT
         done; rm -f /data/local/tmp/acct.rules' >/dev/null 2>&1 || true
    rm -rf "$TMP"
}
trap cleanup EXIT

adbs exec-out 'cat /data/system/packages.list' > "$TMP/packages.list"
python3 -I "$ROOT_DIR/scripts/rejected-by-uid.py" rules "$TMP/packages.list" > "$TMP/acct.rules"
adbs push "$TMP/acct.rules" /data/local/tmp/acct.rules >/dev/null
read -r W4 W6 <<<"$(dsh 'sh /system/bin/amazonblock.sh want')"
dsh "iptables-restore -w --noflush < /data/local/tmp/acct.rules
     ip6tables-restore -w --noflush < /data/local/tmp/acct.rules
     iptables -w -I AMZBLOCK $W4 -j AMZACCT
     ip6tables -w -I AMZBLOCK $W6 -j AMZACCT"

note "counting for $SECONDS_TO_COUNT s"
sleep "$SECONDS_TO_COUNT"
dsh 'iptables -w -vnxL AMZACCT; ip6tables -w -vnxL AMZACCT
     iptables -w -vnxL AMZBLOCK | grep AMZACCT; ip6tables -w -vnxL AMZBLOCK | grep AMZACCT' \
    | python3 -I "$ROOT_DIR/scripts/rejected-by-uid.py" report "$TMP/packages.list"
