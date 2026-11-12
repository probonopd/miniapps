#!/bin/bash
#
# Copyright (c) 2026 Simon Peter
#
# SPDX-License-Identifier: MIT
#
# Builds dropbear and dropbearkey as static binaries for armeabi-v7a, arm64-v8a or x86_64, no NDK: a statically linked
# binary has no runtime dependency on Android's own libc (bionic), so a normal Linux cross
# compiler works. See ../README.md for why dropbear needed patching at all.
#
# Needs the cross compiler for the target (Debian/Ubuntu: gcc-arm-linux-gnueabihf, gcc-aarch64-linux-gnu,
# gcc-x86-64-linux-gnu), curl, patch.
#
#   ./build.sh [armv7|arm64|x86_64]
set -euo pipefail
cd "$(dirname "$0")"
HERE=$(pwd)
./fetch.sh

ARCH=${1:-armv7}
# Static binaries with a plain Linux cross compiler: no runtime dependency on Android's libc.
case $ARCH in
	armv7) HOST=arm-linux-gnueabihf ;;
	arm64) HOST=aarch64-linux-gnu ;;
	x86_64) HOST=x86_64-linux-gnu ;;
	*) echo "usage: $0 [armv7|arm64|x86_64]" >&2; exit 1 ;;
esac
OUT=$HERE/out/$ARCH
# configure writes into the source tree: one copy per architecture.
SRC=$OUT/src
mkdir -p "$OUT"
if [ ! -d "$SRC" ]; then
	cp -a "$HERE/src/dropbear" "$SRC"
	# A tree built before for another architecture must not lend it its objects.
	find "$SRC" \( -name '*.o' -o -name '*.a' \) -delete
fi

stamp() { [ -f "$OUT/$1.done" ]; }
done_() { touch "$OUT/$1.done"; }

if ! stamp configure; then
	(cd "$SRC" && CC=$HOST-gcc ./configure --host=$HOST \
		--disable-zlib --disable-harden --disable-lastlog --disable-utmp --disable-utmpx \
		--disable-wtmp --disable-wtmpx --disable-loginfunc --disable-pututline --disable-pam \
		LDFLAGS=-static >/dev/null)
	done_ configure
fi

make -C "$SRC" PROGRAMS="dropbear dropbearkey" -j"$(nproc)" >/dev/null
cp "$SRC/dropbear" "$SRC/dropbearkey" "$OUT/"
file "$OUT/dropbear" "$OUT/dropbearkey"
ls -l "$OUT/dropbear" "$OUT/dropbearkey"
