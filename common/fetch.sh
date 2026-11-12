#!/bin/sh
#
# Copyright (c) 2026 Simon Peter
#
# SPDX-License-Identifier: MIT
#
# Downloads an app's build inputs listed in APPDIR/libs.txt into APPDIR/build/libs (once) and verifies them
# against APPDIR/libs.sha256. G = Google Maven (AndroidX, Media3, R8/D8), M = Maven Central, U = a full URL.
# Usage: sh common/fetch.sh APPDIR
set -eu
here=$(cd "${1:?usage: fetch.sh APPDIR}" && pwd)
lib=$here/build/libs
mkdir -p "$lib"
grep -v '^#' "$here/libs.txt" | while IFS='|' read -r repo path; do
	f=$lib/$(basename "$path")
	[ -s "$f" ] && continue
	case $repo in
	G) base=https://dl.google.com/dl/android/maven2 ;;
	M) base=https://repo1.maven.org/maven2 ;;
	U) base=${path%/*}; path=${path##*/} ;;
	esac
	echo "fetch $(basename "$path")"
	curl -fsSL -o "$f.part" "$base/$path"
	mv "$f.part" "$f"
done
cd "$lib"
if [ -s "$here/libs.sha256" ]; then
	sha256sum -c --quiet "$here/libs.sha256"
else
	for f in $(grep -v '^#' "$here/libs.txt" | sed 's|.*/||'); do sha256sum "$f"; done >"$here/libs.sha256"
	echo "libs.sha256 created, review and keep it"
fi
