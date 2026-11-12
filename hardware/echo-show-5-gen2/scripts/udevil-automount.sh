#!/bin/bash
# Host-side: make udevil's devmon automounter work again.
#
# Problem: devmon mounts with "-o noexec,..." but udevil.conf's allowed_options
# did not list noexec, so every mount was refused with
#   udevil: Option 'noexec' ist keine erlaubte Option
# Fix: add noexec to allowed_options (backup kept) and restart devmon.
source "$(dirname "$0")/lib.sh"

CONF=/etc/udevil/udevil.conf
[ -f "$CONF" ] || die "udevil not installed"

if ! grep -Eq '^allowed_options[[:space:]]*=.*\bnoexec\b' "$CONF"; then
    note "adding noexec to allowed_options in $CONF"
    sudo_ask cp -n "$CONF" "$CONF.bak-$(date +%Y%m%d)"
    sudo_ask sed -i 's/^allowed_options\( *\)= */allowed_options\1= noexec, /' "$CONF"
fi

grep -E '^allowed_options[[:space:]]*=' "$CONF" | head -1

# restart devmon (bracket pattern avoids pkill matching this script itself)
pkill -f '/bin/[d]evmon' 2>/dev/null || true
sleep 1
setsid devmon >/tmp/devmon.log 2>&1 < /dev/null &
sleep 2

pgrep -a -f '/bin/[d]evmon' || die "devmon did not start (see /tmp/devmon.log)"
devmon --mount-all || true
sleep 1
findmnt -rn -o TARGET,SOURCE | grep -E '/media/|/run/media/' || note "no removable media mounted right now"
note "devmon is running; new devices are automounted"
