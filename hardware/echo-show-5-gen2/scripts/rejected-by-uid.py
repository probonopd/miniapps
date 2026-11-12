#!/usr/bin/env python3
"""Helper for rejected-by-uid.sh: build the counting chain, report its counters."""
import collections
import re
import sys

# Android and Amazon service users that have no entry in packages.list.
SERVICE_UIDS = {0, 1000, 1001, 1002, 1010, 1013, 1017, 1019, 1021, 1027, 1041,
                1051, 2000, 3003, 5027, 5108, 5120, 9999}


def rules(path):
    uids = {int(line.split()[1]) for line in open(path)} | SERVICE_UIDS
    print("*filter\n:AMZACCT - [0:0]")
    for uid in sorted(uids):
        print(f"-A AMZACCT -m owner --uid-owner {uid} -j RETURN")
    print("COMMIT")


def report(path):
    names = collections.defaultdict(list)
    for line in open(path):
        fields = line.split()
        names[fields[1]].append(fields[0])
    counts = collections.Counter()
    total = 0
    for line in sys.stdin:
        by_uid = re.match(r"\s*(\d+)\s+\d+\s+RETURN.*UID match (\d+)", line)
        if by_uid:
            counts[by_uid[2]] += int(by_uid[1])
        chain = re.match(r"\s*(\d+)\s+\d+\s+AMZACCT", line)
        if chain:
            total += int(chain[1])
    print(f"rejected {total}, attributed {sum(counts.values())}")
    for uid, count in counts.most_common():
        if count:
            print(f"{count:6} {uid:>6} {','.join(names.get(uid, ['-']))[:80]}")


if __name__ == "__main__":
    {"rules": rules, "report": report}[sys.argv[1]](sys.argv[2])
