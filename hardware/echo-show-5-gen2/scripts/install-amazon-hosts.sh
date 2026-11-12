#!/bin/bash
# Blackhole Amazon/Alexa/OTA hostnames in /system/etc/hosts.
# Idempotent: the block is delimited by a marker and replaced on re-run.
# The original hosts file is saved once as /system/etc/hosts.bak-<date>.
source "$(dirname "$0")/lib.sh"

require_device
require_root

DOMAINS="amazon.com www.amazon.com m.amazon.com smile.amazon.com
amazon.de www.amazon.de amazon.co.uk amazon.fr amazon.it amazon.es
alexa.amazon.com alexa.amazon.de alexa.amazon.co.uk
api.amazonalexa.com api.eu.amazonalexa.com api.fe.amazonalexa.com
avs-alexa-na.amazon.com avs-alexa-eu.amazon.com avs-alexa-fe.amazon.com
device-messaging.amazon.com amzn-device-messaging.amazon.com
mas-ext.amazon.com mads.amazon.com mads-eu.amazon.com mads-na.amazon.com
completion.amazon.com completion-eu.amazon.com
fls-na.amazon.com fls-eu.amazon.com unagi-na.amazon.com unagi-eu.amazon.com
device-metrics-us.amazon.com device-metrics-us-2.amazon.com
firs-ta-g7g.amazon.com todo-ta-g7g.amazon.com
updates.amazon.com softwareupdate.amazon.com ota.amazon.com
appstore.amazon.com amazonappstore.com www.amazonappstore.com
amazonvideo.com www.amazonvideo.com primevideo.com www.primevideo.com
kindle.com www.kindle.com audible.com www.audible.com
ntp.amazon.com msh.amazon.com dcp.amazon.com dps.amazon.com
amazon-adsystem.com www.amazon-adsystem.com
aax-us-east.amazon-adsystem.com aax-eu.amazon-adsystem.com"

MARK="# BEGIN amazonblock hosts"

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
adbs pull /system/etc/hosts "$TMP/hosts" >/dev/null

python3 - "$TMP/hosts" "$TMP/hosts.new" "$MARK" "$DOMAINS" <<'PY'
import sys
src, dst, mark, domains_arg = sys.argv[1:5]
text = open(src, encoding='utf-8', errors='replace').read()
text = text.split(mark)[0].rstrip() + "\n"
domains = domains_arg.split()
lines = [text, mark]
for d in domains:
    lines.append(f"0.0.0.0 {d}")
    lines.append(f":: {d}")
open(dst, 'w', encoding='utf-8').write("\n".join(lines) + "\n")
PY

confirm "This replaces /system/etc/hosts on $SERIAL (a backup is kept once)."

remount_rw
if ! dsh 'ls /system/etc/hosts.bak-* >/dev/null 2>&1'; then
    dsh "cp /system/etc/hosts /system/etc/hosts.bak-$(date +%Y%m%d)"
fi
adbs push "$TMP/hosts.new" /system/etc/hosts
dsh 'chown root:root /system/etc/hosts; chmod 644 /system/etc/hosts; chcon u:object_r:system_file:s0 /system/etc/hosts'
remount_ro

N=$(dsh 'grep -c "^0.0.0.0" /system/etc/hosts')
note "hosts block installed ($N names, 0.0.0.0 + :: per name)"
