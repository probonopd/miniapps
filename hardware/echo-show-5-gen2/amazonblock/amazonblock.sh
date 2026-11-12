#!/system/bin/sh
# Strict egress allowlist + telemetry/smarthome service shutdown.
#
# Fails closed: OUTPUT policy is DROP, the AMZBLOCK chain ends in REJECT, and
# if neither can be set, amazonblock.rc keeps netd stopped.  Allowed:
#   - loopback
#   - UIDs listed in /system/etc/amazonblock.allow (user-installed apps)
#   - root and system UIDs only: DNS, DHCP, NTP
#   - ICMP / ICMPv6 (path MTU, neighbor discovery)
#   - adbd replies (source port 5555) to private addresses only
#
# Modes:
#   early   run by init before netd and zygote (see amazonblock.rc)
#   (none)  run after netd starts and at boot_completed; also stops daemons
#   want    print the expected number of AMZBLOCK rules (iptables ip6tables)

IPT=/system/bin/iptables
IP6T=/system/bin/ip6tables
REST4=/system/bin/iptables-restore
REST6=/system/bin/ip6tables-restore
ALLOW=/system/etc/amazonblock.allow

# Root is where netd resolves DNS for the blocked apps; system runs the
# network time and DHCP clients.
SYSTEM_UIDS="0 1000"

# Telemetry / smarthome / discovery daemons that must not run. Not "aal": that is MediaTek's display
# service (no network), and with AAL initialized the kernel leaves switching the backlight back on
# after a 0 to it - stopped, a dark display stayed dark until a reboot. aal-metric is Amazon's.
SERVICES="aal-metric trackerd dha_service inlocservice halo-hub-broker
halo-logr meshmgrservice shblemeshd shlocalskillsd smarthomewifid amazonfiled
wifi_log_levels integrity_logger whad_cc"

# kmsg rather than logcat: the early stage runs before logd is useful, and
# dmesg then shows it in order with init's own timestamps.
alog() { echo "amazonblock: $*" > /dev/kmsg 2>/dev/null; }

allow_uids() { grep -E '^[0-9]+$' "$ALLOW" 2>/dev/null; }

system_rule() { # proto port
    for uid in $SYSTEM_UIDS; do
        echo "-A AMZBLOCK -m owner --uid-owner $uid -p $1 --dport $2 -j ACCEPT"
    done
}

common_rules() {
    echo '-A AMZBLOCK -o lo -j ACCEPT'
    allow_uids | while read -r uid; do
        echo "-A AMZBLOCK -m owner --uid-owner $uid -j ACCEPT"
    done
    system_rule udp 53
    system_rule tcp 53
    system_rule udp 123
}

# adbd has to answer on the LAN, but must not be usable to reach the internet.
adb_rule() { echo "-A AMZBLOCK -p tcp -d $1 --sport 5555 -j ACCEPT"; }

rules4() {
    common_rules
    system_rule udp 67
    system_rule udp 68
    echo '-A AMZBLOCK -p icmp -j ACCEPT'
    adb_rule 10.0.0.0/8
    adb_rule 172.16.0.0/12
    adb_rule 192.168.0.0/16
    echo '-A AMZBLOCK -j REJECT'
}

rules6() {
    common_rules
    system_rule udp 546
    system_rule udp 547
    echo '-A AMZBLOCK -p ipv6-icmp -j ACCEPT'
    adb_rule fc00::/7
    adb_rule fe80::/10
    echo '-A AMZBLOCK -j REJECT'
}

load() { # restore-binary rules-function
    {
        echo '*filter'
        echo ':AMZBLOCK - [0:0]'
        "$2"
        echo COMMIT
    } | "$1" -w --noflush
}

# Only DROP plus loopback: this has to hold even if the chain cannot be loaded.
close_policy() { # iptables-binary
    "$1" -w -P OUTPUT DROP || return 1
    "$1" -w -C OUTPUT -o lo -j ACCEPT 2>/dev/null \
        || "$1" -w -A OUTPUT -o lo -j ACCEPT
}

# Insert rather than delete and reinsert: the chain must never be unhooked.
hook() { # iptables-binary
    "$1" -w -S OUTPUT 2>/dev/null | sed -n 2p | grep -q -- '-A OUTPUT -j AMZBLOCK$' \
        || "$1" -w -I OUTPUT 1 -j AMZBLOCK
}

verified() { # iptables-binary rules-function
    [ "$("$1" -w -S AMZBLOCK 2>/dev/null | grep -c '^-A AMZBLOCK')" = "$("$2" | wc -l)" ] \
        && "$1" -w -S AMZBLOCK 2>/dev/null | tail -n 1 | grep -q -- '-j REJECT' \
        && "$1" -w -S OUTPUT 2>/dev/null | sed -n 2p | grep -q -- '-A OUTPUT -j AMZBLOCK$'
}

# While the policy is closed and the chain not yet loaded or hooked (at boot, and again when netd
# starts), the kernel's IPv6 housekeeping (duplicate address detection, router solicitation, MLD
# reports) is dropped. A WLAN that connected in such a moment keeps addresses the router never
# learned: IPv6 to the Internet then gets no answers until the next re-registration, and every app
# waits for its IPv6 connections to time out first. Re-registering once the rules are complete fixes it.
renew_ipv6() {
    f=/proc/sys/net/ipv6/conf/wlan0/disable_ipv6
    [ -w "$f" ] && [ "$(cat "$f")" = 0 ] || return 0
    echo 1 > "$f"
    echo 0 > "$f"
    alog "IPv6 on wlan0 renewed"
}

stop_services() {
    for s in $SERVICES; do
        stop "$s" 2>/dev/null
    done
}

if [ "$1" = want ]; then
    echo "$(rules4 | wc -l) $(rules6 | wc -l)"
    exit 0
fi

n=0
while [ "$n" -lt 5 ]; do
    n=$((n + 1))
    close_policy "$IPT" && close_policy "$IP6T" && policy=1 || policy=
    load "$REST4" rules4; load "$REST6" rules6
    hook "$IPT"; hook "$IP6T"
    if [ -n "$policy" ] && verified "$IPT" rules4 && verified "$IP6T" rules6; then
        alog "firewall active (${1:-late})"
        [ "$1" = early ] || stop_services
        # Earlier versions stopped it; a running one is left alone.
        [ "$1" = early ] || start aal
        [ "$1" = early ] || renew_ipv6
        exit 0
    fi
    alog "attempt $n failed: policy=$policy"
    sleep 2
done

# With the DROP policy in place the device is already offline, so netd can
# keep running; stopping it is only for the case where iptables itself fails.
if [ -n "$policy" ]; then
    alog "INCOMPLETE: chain not loaded, OUTPUT policy is DROP"
else
    alog "FAILED: no OUTPUT policy, stopping netd"
    setprop amazonblock.state failed
fi
[ "$1" = early ] || stop_services
exit 1
