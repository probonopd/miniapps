#!/system/bin/sh
#
# Copyright (c) 2026 Simon Peter
#
# SPDX-License-Identifier: BSD-2-Clause
#
# Display off and on for minitv: while the flag file exists, the backlight is 0; afterwards it
# gets back the level it had. Panel, touch and Android keep running, so playback, voice control
# and calls go on.
#
# MediaTek AAL: with AAL initialized at boot, the kernel switches the backlight off for a 0 but
# leaves switching it back on to the aal service (ddp_aal.c, disp_aal_notify_backlight_changed).
# Without that service a dark display stays dark until a reboot, so nothing is darkened unless aal
# runs. Panel power (fb0 blank, SurfaceFlinger power mode) is not touched: the panel did not
# come back from either on this Echo.
FLAG=/data/media/0/minitv/screen-off
BL=/sys/class/leds/lcd-backlight/brightness

off=0
saved=
# Every start (boot, restart) forgets an old request: a dark Echo must never survive a power cycle.
rm -f "$FLAG"
while true; do
    if [ -e "$FLAG" ]; then
        if [ "$off" = 0 ]; then
            start aal
            if [ "$(getprop init.svc.aal)" = running ]; then
                saved=$(cat "$BL")
                echo 0 > "$BL"
                off=1
                log -t echoscreen "display off (backlight was $saved)"
            else
                log -t echoscreen "aal does not run: display left on, it could not be switched on again"
                rm -f "$FLAG"
            fi
        else
            # Android's automatic brightness sets its own level at every light change: back to dark.
            [ "$(cat "$BL")" = 0 ] || echo 0 > "$BL"
        fi
    elif [ "$off" = 1 ]; then
        # Never 0, which would leave the display dark (a request that came before Android had set any level).
        [ "${saved:-0}" -gt 0 ] || saved=90
        echo "$saved" > "$BL"
        off=0
        log -t echoscreen "display on (backlight $saved)"
    fi
    sleep 1
done
