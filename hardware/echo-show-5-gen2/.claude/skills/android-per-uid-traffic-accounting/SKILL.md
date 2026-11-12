---
name: android-per-uid-traffic-accounting
description: Use when you must see what a rooted Android device sends but it has no tcpdump and no iptables LOG target (only NFLOG/NFQUEUE) - which apps send blocked packets, or whether anything unexpected got out. Covers per-UID counting with a temporary iptables chain before REJECT, mapping UIDs via packages.list, a device-side watchdog on nf_conntrack that cuts WLAN on the first unexpected flow, and what each method cannot show.
---

# Per-UID traffic accounting without tcpdump

Worked on Fire OS 6.5.4.8 (Android 7.1.2). Scripts: `miniapps/hardware/echo-show-5-gen2/scripts/rejected-by-uid.sh`, `rejected-by-uid.py`, `watch-wlan.sh`.

## Check what exists first
`cat /proc/net/ip_tables_targets /proc/net/ip_tables_matches`, `which tcpdump`, `ls -la /proc/net/nf_conntrack`. Host tcpdump only helps with a mirror port or a capture on the router.

## Who sends rejected packets
1. Build a chain with one rule per UID: `-A ACCT -m owner --uid-owner N -j RETURN`. RETURN changes nothing, so the firewall stays closed. Include UIDs from `/data/system/packages.list` plus service users (0, 1000, ...).
2. Load with `iptables-restore -w --noflush` for v4 and v6.
3. Insert one rule right before the final REJECT: `-I CHAIN <position> -j ACCT`.
4. Wait at least 3 minutes; apps back off. Read `iptables -vnxL ACCT`, keep nonzero rows, map UIDs to packages.
5. Remove the jump, flush and delete, in a trap so it also happens on failure. A firewall reload removes it as well.

Limits: UID only, no destinations. Packets not matched (isolated UIDs in the 99000 range) show as unattributed: compare with the jump rule's own counter. UID 1000 is shared by many packages.

## Watching what gets through
Rejected packets never reach conntrack, so `/proc/net/nf_conntrack` lists accepted flows. Poll it once a second in a shell on the device, so that the reaction does not wait for the host, and trip with `svc wifi disable` plus `ip link set wlan0 down`.

- Take the first `dst=` only; the second is the reply tuple.
- Expanded IPv6 appears (`2a02:0908:...`). Test private ranges against that form.
- ICMP lines have no ports: use a separate sed expression.
- Time with `/proc/uptime`: NTP may jump the clock.
- Stop allowed apps first, or tell their flows apart: conntrack has no UID.
- Pipe through `stdbuf -oL tr -d '\r'` on the host, or output arrives only at exit.
- Test the trip with a fake table file (a `WATCH_CT` path override).
- Killing the host side does not kill the device loop; check `ps` and kill it, or it will still trip.

Limits: no DNS names, no payload, about 1 s reaction, and it starts only once adb is up. Those need a router capture.
