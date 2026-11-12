#!/bin/bash
# Privacy hardening for Fire OS 6 (cronos):
#   - location off, providers cleared, fused location app disabled
#   - WLAN/BLE background scanning off, mock locations off
#   - captive-portal probing off, app error reporting off
#   - advertising ID zeroed, ad tracking limited (Fire OS may ignore it)
#   - telemetry / logging / usage-stats / crash-reporting packages disabled
#   - telemetry, smarthome, discovery and location daemons stopped
#     (amazonblock.sh stops the same list at every boot)
#
# The camera is KEPT WORKING by default so user-installed video conferencing
# apps can use it.  Use --disable-camera to disable the camera app and stop
# cameraserver as well.
#
#   ./privacy-hardening.sh [--disable-camera]
source "$(dirname "$0")/lib.sh"

NO_CAMERA=0
[ "${1:-}" = "--disable-camera" ] && NO_CAMERA=1

require_device
require_root
require_booted

confirm "This changes privacy settings, disables packages and stops daemons on $SERIAL."

note "location / scanning / reporting settings"
dsh 'settings put secure location_mode 0
     settings put secure location_providers_allowed -
     settings put global wifi_scan_always_enabled 0
     settings put global ble_scan_always_enabled 0
     settings put global assisted_gps_enabled 0
     settings put secure mock_location 0
     settings put global send_action_app_error 0
     settings put global captive_portal_mode 0
     settings put secure advertising_id 00000000-0000-0000-0000-000000000000
     settings put secure limit_ad_tracking 1
     settings put secure usage_stats_enabled 0
     settings put global network_recommendations_enabled 0
     settings put global wifi_wakeup_enabled 0
     settings put global bluetooth_on 0'
# svc has no bluetooth command here; 8 is IBluetoothManager.disable(pkg, persist).
dsh 'service call bluetooth_manager 8 s16 com.android.settings i32 1' >/dev/null

note "disabling telemetry / diagnostics / location packages"
PACKAGES="com.android.location.fused
com.fireos.usagestats.proxy
com.fireos.arcus.proxy
com.amazon.client.metrics
com.amazon.device.metrics
com.amazon.device.logmanager
com.amazon.device.crashmanager
com.amazon.logan
com.amazon.dp.logger
com.amazon.whad
com.amazon.avod
com.amazon.knight.hds
com.amazon.wirelessmetrics.service
com.amazon.device.messaging
com.amazon.device.sync
com.amazon.device.sync.sdk.internal
com.amazon.alexa.beaconbroadcaster
com.amazon.wifilocker
com.amazon.wifi.sync
com.amazon.whisperjoin.middleware
com.amazon.device.smarthome.ota
com.amazon.device.rdmapplication"

note "stopping telemetry / smarthome / discovery / location daemons"
SERVICES="aal aal-metric trackerd dha_service inlocservice halo-hub-broker
halo-logr meshmgrservice shblemeshd shlocalskillsd smarthomewifid amazonfiled
wifi_log_levels integrity_logger"

if [ "$NO_CAMERA" = 1 ]; then
    note "camera disabled by request"
    PACKAGES="$PACKAGES
com.android.camera2"
    SERVICES="$SERVICES
cameraserver"
fi

for p in $PACKAGES; do
    if dsh "pm path $p >/dev/null 2>&1"; then
        dsh "pm disable-user --user 0 $p" >/dev/null 2>&1 || note "could not disable $p"
    fi
done

for s in $SERVICES; do
    dsh "stop $s" >/dev/null 2>&1 || true
done

note "state"
dsh 'echo "location_mode=$(settings get secure location_mode) providers=$(settings get secure location_providers_allowed)"
     echo "wifi_scan_always=$(settings get global wifi_scan_always_enabled) ble_scan_always=$(settings get global ble_scan_always_enabled)"
     for s in aal aal-metric trackerd dha_service inlocservice cameraserver avahi-daemon mdnsd smarthomewifid; do
         printf "%-18s %s\n" "$s" "$(getprop init.svc.$s)"
     done
     echo "camera2_disabled=$(pm list packages -d | grep -c com.android.camera2)"'
