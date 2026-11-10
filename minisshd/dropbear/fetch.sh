#!/bin/bash
#
# Copyright (c) 2026 Simon Peter
#
# SPDX-License-Identifier: BSD-2-Clause
#
# Downloads dropbear's source into dl/, extracts and patches it into src/ (pinned by sources.sha256).
# --pin writes sources.sha256 from what was downloaded (only after a version bump in sources.txt).
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p dl src
while IFS='|' read -r name url; do
	case "$name" in ''|'#'*) continue;; esac
	file=dl/$name.tar.bz2
	if [ ! -f "$file" ]; then
		curl -fsSL -o "$file.part" "$url"
		mv "$file.part" "$file"
	fi
done < sources.txt
if [ "${1:-}" = --pin ]; then
	(cd dl && sha256sum *.tar.bz2) > sources.sha256
else
	(cd dl && sha256sum -c --quiet ../sources.sha256)
fi
while IFS='|' read -r name url; do
	case "$name" in ''|'#'*) continue;; esac
	[ -d src/$name ] && continue
	mkdir -p src/$name
	tar -xjf dl/$name.tar.bz2 -C src/$name --strip-components=1
	for p in patches/$name-*.patch; do
		[ -f "$p" ] || continue
		patch -d src/$name -p1 --quiet < "$p"
	done
done < sources.txt
echo "source ready in $(pwd)/src"
