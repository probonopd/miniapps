#!/bin/bash
#
# Copyright (c) 2026 Simon Peter
#
# SPDX-License-Identifier: BSD-2-Clause
#
# Install the display switch for minitv (cmd/screen via MQTT, "Bildschirm aus" by voice).
#
#   ./install-echoscreen.sh
source "$(dirname "$0")/lib.sh"
require_device
require_root
require_booted
dsh 'stop echoscreen' >/dev/null 2>&1 || true
remount_rw
adbs push "$ROOT_DIR/echoscreen/echoscreen.sh" /system/bin/echoscreen.sh >/dev/null
adbs push "$ROOT_DIR/echoscreen/echoscreen.rc" /system/etc/init/echoscreen.rc >/dev/null
# An earlier version switched the panel through SurfaceFlinger with this helper.
dsh 'rm -f /system/framework/screenpower.jar'
dsh 'chown root:root /system/bin/echoscreen.sh /system/etc/init/echoscreen.rc
     chmod 755 /system/bin/echoscreen.sh
     chmod 644 /system/etc/init/echoscreen.rc
     chcon u:object_r:system_file:s0 /system/bin/echoscreen.sh /system/etc/init/echoscreen.rc'
remount_ro
dsh 'mkdir -p /data/media/0/minitv'
# A service that runs already (an update) starts again with the new script; a first install needs the reboot.
dsh 'start echoscreen' >/dev/null 2>&1 || true
note "installed; on a first install init reads the rc file at boot, so reboot once"
