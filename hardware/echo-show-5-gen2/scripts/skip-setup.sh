#!/bin/bash
# Skip the Fire OS OOBE/setup wizard and start the Echo Show launcher.
#
#   ./skip-setup.sh
#
# Revert with:
#   adb shell pm enable --user 0 com.amazon.kindle.otter.oobe
#   adb shell settings put global device_provisioned 0
#   adb shell settings put secure user_setup_complete 0
source "$(dirname "$0")/lib.sh"

require_device
require_root
require_booted

confirm "This marks $SERIAL as provisioned and disables the OOBE wizard."

dsh 'settings put global device_provisioned 1
     settings put secure user_setup_complete 1'

if dsh 'pm path com.amazon.kindle.otter.oobe >/dev/null 2>&1'; then
    dsh 'pm disable-user --user 0 com.amazon.kindle.otter.oobe' || true
    dsh 'am force-stop com.amazon.kindle.otter.oobe' || true
fi

dsh 'am start -n com.amazon.paladin/.app.MMLauncherActivity' >/dev/null 2>&1 || true
sleep 3

FOCUS=$(dsh 'dumpsys window 2>/dev/null | grep -m1 mCurrentFocus')
case "$FOCUS" in
    *com.amazon.paladin*) note "launcher focused: $FOCUS";;
    *) note "current focus: $FOCUS (check the screen)";;
esac

note "provisioned=$(dsh 'settings get global device_provisioned') setup_complete=$(dsh 'settings get secure user_setup_complete')"
