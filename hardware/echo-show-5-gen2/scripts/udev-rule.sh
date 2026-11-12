#!/bin/bash
# Host-side: install the udev rule for the Fire OS adb interface (1949:01e8)
# so adb can open the device as a normal user.
source "$(dirname "$0")/lib.sh"

RULE=/etc/udev/rules.d/51-amazon-echo-adb.rules

if [ "$(id -u)" -eq 0 ]; then
    SUDO=
else
    command -v sudo >/dev/null || die "need root or sudo"
    SUDO=sudo_ask
fi

$SUDO sh -c "cat > $RULE" <<'EOF'
SUBSYSTEM=="usb", ATTR{idVendor}=="1949", ATTR{idProduct}=="01e8", MODE="0666"
EOF

$SUDO udevadm control --reload-rules
$SUDO udevadm trigger --subsystem-match=usb --action=add
sleep 1
adb kill-server 2>/dev/null || true
adb start-server >/dev/null
adb devices -l
