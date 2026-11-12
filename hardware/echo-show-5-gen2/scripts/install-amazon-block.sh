#!/bin/bash
# Install the strict egress allowlist + telemetry shutdown.
#
#   ./install-amazon-block.sh [--uid N ...]
#
# Allowed through the firewall: user-installed app UIDs (from
# `pm list packages -3`), loopback, DNS/DHCP/NTP for root and system, ICMP and
# adbd replies to private addresses.
# Everything else is REJECTed, including root, system and Amazon daemons.
source "$(dirname "$0")/lib.sh"

EXTRA=()
while [ $# -gt 0 ]; do
    case "$1" in
        --uid) EXTRA+=(--uid "$2"); shift 2;;
        *) die "unknown argument: $1";;
    esac
done

require_device
require_root
require_booted

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT

note "reading package database from $SERIAL"
adbs exec-out 'cat /data/system/packages.list' > "$TMP/packages.list"
dsh 'pm list packages -3' | sed 's/^package://' > "$TMP/thirdparty.txt"

python3 "$ROOT_DIR/scripts/generate-allow.py" \
    "$TMP/packages.list" "$TMP/thirdparty.txt" "$TMP/amazonblock.allow" "${EXTRA[@]}"

note "allowed UIDs: $(tr '\n' ' ' < "$TMP/amazonblock.allow")"
note "(everything else is blocked, including root and Amazon daemons)"

confirm "This writes /system/bin/amazonblock.sh, /system/etc/amazonblock.allow and /system/etc/init/amazonblock.rc on $SERIAL."

remount_rw
adbs push "$ROOT_DIR/amazonblock/amazonblock.sh"   /system/bin/amazonblock.sh
adbs push "$TMP/amazonblock.allow"                  /system/etc/amazonblock.allow
adbs push "$ROOT_DIR/amazonblock/amazonblock.rc"   /system/etc/init/amazonblock.rc
dsh 'rm -f /system/etc/amazonblock.uids
     chown root:root /system/bin/amazonblock.sh /system/etc/amazonblock.allow /system/etc/init/amazonblock.rc
     chmod 755 /system/bin/amazonblock.sh
     chmod 644 /system/etc/amazonblock.allow /system/etc/init/amazonblock.rc
     chcon u:object_r:system_file:s0 /system/bin/amazonblock.sh /system/etc/amazonblock.allow /system/etc/init/amazonblock.rc'
remount_ro

note "applying now"
dsh 'sh /system/bin/amazonblock.sh; echo rc=$?' | grep -qx 'rc=0' || die "amazonblock.sh failed, see dmesg"

N=$(grep -cE '^[0-9]+$' "$TMP/amazonblock.allow")
read -r W4 W6 <<<"$(dsh 'sh /system/bin/amazonblock.sh want')"
V4=$(dsh 'iptables -S AMZBLOCK | grep -c "^-A AMZBLOCK"')
V6=$(dsh 'ip6tables -S AMZBLOCK | grep -c "^-A AMZBLOCK"')
[ "$V4" = "$W4" ] && [ "$V6" = "$W6" ] \
    || die "rule count mismatch: v4=$V4/$W4 v6=$V6/$W6"
dsh 'iptables -S OUTPUT | sed -n 1p' | grep -q -- '-P OUTPUT DROP' \
    || die "OUTPUT policy is not DROP"
dsh 'iptables -S OUTPUT | sed -n 2p' | grep -q -- '-A OUTPUT -j AMZBLOCK' \
    || die "AMZBLOCK is not the first OUTPUT rule"
dsh 'iptables -S AMZBLOCK | tail -n 1' | grep -q -- '-j REJECT' \
    || die "AMZBLOCK does not end in REJECT"

note "installed: strict allowlist ($N allowed UID(s), $V4 v4 / $V6 v6 rules)"
note "telemetry/smarthome services stopped; they are stopped again at every boot"
