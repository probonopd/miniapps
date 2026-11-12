#!/bin/bash
# Verify the state of the Echo Show (cronos): root adb, strict allowlist,
# telemetry shutdown, privacy settings, update packages, setup, Settings,
# minitv.
source "$(dirname "$0")/lib.sh"

FAIL=0
ok()   { echo "PASS  $*"; }
bad()  { echo "FAIL  $*"; FAIL=$((FAIL+1)); }
info() { echo "INFO  $*"; }

require_device
info "serial $(dsh getprop ro.serialno)  build $(dsh getprop ro.build.version.name)"

id=$(dsh id)
case "$id" in *"uid=0(root)"*) ok "adb shell is root";; *) bad "adb shell is not root: $id";; esac
[ "$(dsh getprop sys.boot_completed)" = 1 ] && ok "Fire OS booted" || bad "not booted"

# --- strict firewall
N=$(dsh 'grep -cE "^[0-9]+$" /system/etc/amazonblock.allow 2>/dev/null' || echo 0)
if [ "${N:-0}" -gt 0 ]; then
    read -r W4 W6 <<<"$(dsh 'sh /system/bin/amazonblock.sh want')"
    V4=$(dsh 'iptables -S AMZBLOCK 2>/dev/null | grep -c "^-A AMZBLOCK"')
    V6=$(dsh 'ip6tables -S AMZBLOCK 2>/dev/null | grep -c "^-A AMZBLOCK"')
    [ "${V4:-0}" = "$W4" ] && ok "IPv4 allowlist rules $V4" || bad "IPv4 rules $V4 want $W4"
    [ "${V6:-0}" = "$W6" ] && ok "IPv6 allowlist rules $V6" || bad "IPv6 rules $V6 want $W6"
    for T in iptables ip6tables; do
        dsh "$T -S OUTPUT | sed -n 1p" | grep -q -- '-P OUTPUT DROP' \
            && ok "$T OUTPUT policy DROP" || bad "$T OUTPUT policy is not DROP"
        dsh "$T -S OUTPUT | sed -n 2p" | grep -q -- '-A OUTPUT -j AMZBLOCK' \
            && ok "$T AMZBLOCK first in OUTPUT" || bad "$T AMZBLOCK is not first in OUTPUT"
    done
    # dmesg alone can roll over within minutes when the display driver logs a lot.
    dsh '{ dmesg; logcat -d -b kernel; } 2>/dev/null | grep -q "amazonblock: firewall active (early)"' \
        && ok "firewall loaded before netd" || bad "no early firewall load in the kernel log"
    dsh 'iptables -S AMZBLOCK | tail -n 1' | grep -q -- '-j REJECT' \
        && ok "allowlist ends in REJECT" || bad "allowlist does not end in REJECT"
    if dsh 'pm path org.minitv >/dev/null 2>&1'; then
        MU=$(dsh 'grep "^org.minitv " /data/system/packages.list | cut -d" " -f2')
        dsh 'cat /system/etc/amazonblock.allow' | grep -qx -- "$MU" \
            && ok "minitv UID $MU is allowed" || bad "minitv UID $MU is NOT allowed"
    fi
else
    bad "strict allowlist not installed (/system/etc/amazonblock.allow missing/empty)"
fi

# --- update packages
for p in com.amazon.device.software.ota com.amazon.device.software.ota.override \
         com.amazon.android.marketplace com.amazon.venezia; do
    dsh "pm list packages -d | grep -qx package:$p" && ok "$p disabled" || bad "$p NOT disabled"
done

# --- telemetry / diagnostics / location / camera packages
for p in com.amazon.client.metrics com.amazon.device.metrics com.amazon.device.logmanager \
         com.amazon.device.crashmanager com.amazon.logan com.amazon.dp.logger \
         com.amazon.wirelessmetrics.service com.amazon.whad com.amazon.avod com.amazon.knight.hds com.amazon.device.messaging \
         com.amazon.device.sync com.amazon.alexa.beaconbroadcaster com.amazon.wifilocker \
         com.amazon.wifi.sync com.amazon.whisperjoin.middleware com.amazon.device.smarthome.ota \
         com.fireos.usagestats.proxy com.android.location.fused; do
    if dsh "pm path $p >/dev/null 2>&1"; then
        dsh "pm list packages -d | grep -qx package:$p" && ok "$p disabled" || bad "$p NOT disabled"
    else
        info "$p not installed"
    fi
done

# --- services stopped
for s in aal-metric trackerd dha_service inlocservice halo-hub-broker halo-logr \
         meshmgrservice shlocalskillsd smarthomewifid amazonfiled wifi_log_levels \
         integrity_logger whad_cc; do
    st=$(dsh "getprop init.svc.$s")
    if [ "$st" = stopped ] || [ -z "$st" ]; then
        ok "service $s stopped"
    else
        bad "service $s is $st"
    fi
done
info "mdnsd=$(dsh 'getprop init.svc.mdnsd') avahi-daemon=$(dsh 'getprop init.svc.avahi-daemon') cameraserver=$(dsh 'getprop init.svc.cameraserver')"

# --- privacy settings
[ "$(dsh 'settings get secure location_mode')" = 0 ] && ok "location_mode=0" || bad "location_mode != 0"
[ "$(dsh 'settings get global wifi_scan_always_enabled')" = 0 ] && ok "wifi scan always off" || bad "wifi scan always on"
[ "$(dsh 'settings get global ble_scan_always_enabled')" = 0 ] && ok "ble scan always off" || bad "ble scan always on"
[ "$(dsh 'settings get global send_action_app_error')" = 0 ] && ok "app error reporting off" || bad "app error reporting not off"
[ "$(dsh 'settings get global captive_portal_mode')" = 0 ] && ok "captive portal probing off" || bad "captive portal probing not off"

# --- hands-free voice
if dsh 'ls /system/bin/pryonwake >/dev/null 2>&1'; then
    [ "$(dsh 'getprop init.svc.echowake')" = running ] && ok "echowake running" || bad "echowake not running"
    dsh 'pidof pryonwake >/dev/null' && ok "pryonwake running" || bad "pryonwake not running"
    dsh 'pidof app_process >/dev/null' && ok "mic helper running" || bad "mic helper not running"
    [ "$(dsh 'dumpsys media.audio_flinger | grep -c "Thread name: AudioIn"')" -ge 3 ] \
        && ok "third recording active" || bad "third recording not active"
else
    info "echowake not installed"
fi

# --- setup / Settings / minitv
[ "$(dsh 'settings get global device_provisioned')" = 1 ] && ok "device_provisioned=1" || bad "not provisioned"
[ "$(dsh 'settings get secure user_setup_complete')" = 1 ] && ok "user_setup_complete=1" || bad "setup not complete"
dsh 'pm list packages -d | grep -qx package:com.amazon.kindle.otter.oobe' && ok "OOBE disabled" || bad "OOBE not disabled"
dsh 'ls /data/data/com.android.settings/files/bundle_a/preference-settings.json >/dev/null 2>&1' \
    && ok "Settings bundle present" || bad "Settings bundle missing"
dsh 'pm path org.minitv >/dev/null 2>&1' && ok "minitv installed" || info "minitv not installed"
dsh 'ls /sdcard/playlist.m3u >/dev/null 2>&1' && ok "minitv playlist present" || info "minitv playlist missing"

info "wifi_on=$(dsh 'settings get global wifi_on')  service.adb.tcp.port=$(dsh 'getprop service.adb.tcp.port')"

echo
if [ "$FAIL" -eq 0 ]; then echo "ALL CHECKS PASSED"; else echo "$FAIL CHECK(S) FAILED"; exit 1; fi
