#!/usr/bin/env python3
"""Build the strict allowlist UID file from the device package database.

Usage: generate-allow.py <packages.list> <thirdparty-packages.txt> <out.allow> [--uid N]

thirdparty-packages.txt is `adb shell pm list packages -3` with "package:"
stripped.  Only these UIDs are allowed internet by the strict firewall.
"""
import argparse
import sys


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("packages_list")
    ap.add_argument("thirdparty")
    ap.add_argument("out")
    ap.add_argument("--uid", action="append", type=int, default=[],
                    help="extra UID to allow (repeatable)")
    args = ap.parse_args()

    uid_of = {}
    with open(args.packages_list, encoding="utf-8", errors="replace") as fh:
        for line in fh:
            p = line.split()
            if len(p) >= 2 and p[1].isdigit():
                uid_of[p[0]] = int(p[1])

    with open(args.thirdparty, encoding="utf-8", errors="replace") as fh:
        pkgs = [l.strip() for l in fh if l.strip()]

    uids = set(args.uid)
    missing = []
    for p in pkgs:
        if p in uid_of:
            uids.add(uid_of[p])
        else:
            missing.append(p)

    with open(args.out, "w", encoding="utf-8") as fh:
        fh.write("\n".join(str(u) for u in sorted(uids)) + "\n")

    print(f"{len(uids)} allowed UIDs -> {args.out}")
    if missing:
        print("warning: packages without UID: " + ", ".join(missing), file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
