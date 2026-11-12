#!/usr/bin/env python3
"""Build the amazonblock UID list from a Fire OS packages.list.

Usage: generate-uids.py <packages.list> <system-packages.txt> <out.uids>

packages.list is /data/system/packages.list from the device.
system-packages.txt is `adb shell pm list packages -s` with "package:" stripped.

Blocked = every UID used by a system package, except the shared infrastructure
UIDs below and except UIDs given with --allow.
"""
import argparse
import sys

DEFAULT_ALLOWED = {
    0,      # root
    1000,   # system (Settings, Keychain, input, location, ...)
    1001,   # radio
    1002,   # bluetooth
    2000,   # shell / adb
    10024,  # com.android.providers.downloads (third-party DownloadManager)
    10139,  # com.android.captiveportallogin
}


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("packages_list")
    ap.add_argument("system_packages")
    ap.add_argument("out")
    ap.add_argument("--allow", action="append", type=int, default=[],
                    help="extra UID to keep online (repeatable)")
    args = ap.parse_args()

    uid_of = {}
    with open(args.packages_list, encoding="utf-8", errors="replace") as fh:
        for line in fh:
            parts = line.split()
            if len(parts) >= 2 and parts[1].isdigit():
                uid_of[parts[0]] = int(parts[1])

    with open(args.system_packages, encoding="utf-8", errors="replace") as fh:
        syspkgs = [l.strip() for l in fh if l.strip()]

    allowed = set(DEFAULT_ALLOWED) | set(args.allow)
    blocked = sorted({uid_of[p] for p in syspkgs
                      if p in uid_of and uid_of[p] not in allowed})

    with open(args.out, "w", encoding="utf-8") as fh:
        fh.write("\n".join(map(str, blocked)) + "\n")

    print(f"{len(blocked)} blocked UIDs -> {args.out}")
    missing = [p for p in syspkgs if p not in uid_of]
    if missing:
        print(f"note: {len(missing)} system packages not in packages.list: "
              f"{', '.join(missing[:5])}{' ...' if len(missing) > 5 else ''}",
              file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
