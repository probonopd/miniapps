---
name: android-early-boot-firewall
description: Use when a rooted Android or Fire OS device (old enough for iptables, Android 7 tested) must have an egress firewall with no gap at boot, e.g. keeping vendor daemons offline when WLAN auto-connects. Covers why a boot_completed service leaves a ~36 s open window, blocking init with exec in on post-fs-data before class main, failing closed (OUTPUT policy DROP, stopping netd), surviving netd restarts, hooking a chain without unhooking it, deriving the expected rule count from the rule generator, logging to kmsg, reload resetting counters, and device pitfalls (tail -1 printing 10 lines, pm disable-user lost on quick reboot, disabled persistent apps still running, adb reply rules, NTP clock jump).
---

# Egress firewall with no boot gap (Android init)

Worked on Echo Show 5 2nd gen (cronos, Fire OS 6.5.4.8, Android 7.1.2, MT8163), root via boot image, dm-verity off. Files: `miniapps/hardware/echo-show-5-gen2/amazonblock/`, `scripts/install-amazon-block.sh`, `DOCUMENTATION.md` section 9.

## The gap
`on property:sys.boot_completed=1` runs tens of seconds after vendor daemons start. Measured: rules at 36 s; before that `OUTPUT` was ACCEPT. A device only has this gap once WLAN is left on with a saved network, so a test with WLAN off will not show it.

## Load order
`exec` blocks init. `start` does not and races class main.

```
on post-fs-data
    exec u:r:adbd:s0 root root -- /system/bin/sh /system/bin/fw.sh early
```

- Check `grep -n class_start /init*.rc`: `post-fs-data` must trigger before `class_start main` (netd, zygote) on both the normal and encrypted paths.
- init itself had no netfilter permission. `seclabel` must be a domain that does; `adbd` is permissive on this boot image, so check with `getenforce` plus a real run.
- init's main loop keeps serving properties during `exec`, so `setprop` from the script does not deadlock.
- A hang in this script stops the boot: bound every retry. Know the way into recovery first.
- Measured result: 4.96 s after power-on.

## Fail closed, in layers
1. Set `-P OUTPUT DROP` and a loopback ACCEPT first. They hold even if the chain load fails.
2. Load the chain with `iptables-restore -w --noflush`; it ends in REJECT.
3. If even the policy fails, `setprop fw.state failed`, and in the rc:
   `on property:fw.state=failed` and `on property:init.svc.netd=running && property:fw.state=failed` both `stop netd`. No netd means WLAN cannot come up. USB adb still works. Test with `setprop`; a normal reboot clears it.

## Keep it alive
- Rerun on `on property:init.svc.netd=running`: netd rebuilds its chains on start. Test by flushing `OUTPUT` and restarting netd.
- Check whether the hook is first (`-S OUTPUT | sed -n 2p`) and only `-I` if not. Delete-then-insert leaves the chain unhooked for a moment.
- Generate rules from functions, and count those same functions for verification (`fw.sh want`). A hardcoded `allowed + 10` silently broke when a rule was added.
- Log to `/dev/kmsg`, not logcat: in order with init's timestamps. Check for the early line in `dmesg`.

## Rule design
- Match by UID (`-m owner`), not hostname. Hosts files are a second layer.
- DNS, DHCP, NTP for root and system UIDs only. netd resolves for apps as root.
- Kernel-sent SYN-ACKs have no socket owner, so `--uid-owner 0` does not cover a listening adbd. Use `--sport 5555` limited to private destinations (RFC 1918, `fc00::/7`, `fe80::/10`).
- Java DHCP uses raw packet sockets, which bypass OUTPUT.
- Rules are duplicated for IPv4 and IPv6; test both. Devices often get global v6 addresses.

## Pitfalls hit
- `tail -1` printed 10 lines from a pipe; use `tail -n 1`. A `tail -1 | grep -q REJECT` check passed without proving anything.
- `service`/`ps` flags vary: no `awk`, `ps -A` fails, plain `ps` works. toybox `sed -E` errors with "no s//\4/" if a backreference group is optional.
- `pm disable-user` reaches `package-restrictions.xml` a few seconds later. An immediate reboot loses it; wait 20 s, then check `enabled="3"`.
- A disabled PERSISTENT app can keep a running process. Stop its init daemon too and check its ports.
- `svc bluetooth` does not exist on Android 7: use `service call bluetooth_manager 8 s16 <pkg> i32 1`.
- The first NTP sync can jump the clock by years: time scripts with `/proc/uptime`.
- A chain reload resets counters, so a late reload hides everything before it.
- `pkill -f name` from a shell whose command line contains `name` kills that shell. Find the PID first.

## Verify
Reboot with WLAN on and a saved network. Check `dmesg`, policy, hook, rule count; then watch with the android-per-uid-traffic-accounting skill. Only a router-side capture proves nothing left during boot.
