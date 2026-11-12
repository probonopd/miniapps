#!/bin/bash
# Remove the strict allowlist firewall and its boot service.
#
#   ./uninstall-amazon-block.sh [--enable-updates]
source "$(dirname "$0")/lib.sh"

ENABLE_UPDATES=
[ "${1:-}" = "--enable-updates" ] && ENABLE_UPDATES=1

require_device
require_root

confirm "This removes the amazonblock files and chains from $SERIAL."

remount_rw
dsh 'rm -f /system/etc/init/amazonblock.rc /system/bin/amazonblock.sh /system/etc/amazonblock.allow /system/etc/amazonblock.uids'
remount_ro

dsh 'iptables -P OUTPUT ACCEPT
     iptables -D OUTPUT -o lo -j ACCEPT 2>/dev/null
     iptables -D OUTPUT -j AMZBLOCK 2>/dev/null
     iptables -F AMZBLOCK 2>/dev/null
     iptables -X AMZBLOCK 2>/dev/null
     ip6tables -P OUTPUT ACCEPT
     ip6tables -D OUTPUT -o lo -j ACCEPT 2>/dev/null
     ip6tables -D OUTPUT -j AMZBLOCK 2>/dev/null
     ip6tables -F AMZBLOCK 2>/dev/null
     ip6tables -X AMZBLOCK 2>/dev/null'
note "chains removed (this boot); files removed (persistent)"

if [ -n "$ENABLE_UPDATES" ]; then
    dsh 'pm enable --user 0 com.amazon.device.software.ota
         pm enable --user 0 com.amazon.device.software.ota.override
         pm enable --user 0 com.amazon.android.marketplace
         pm enable --user 0 com.amazon.venezia' || true
    note "update packages re-enabled"
fi
