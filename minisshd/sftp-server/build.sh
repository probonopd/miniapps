#!/bin/bash
#
# Copyright (c) 2026 Simon Peter
#
# SPDX-License-Identifier: BSD-2-Clause
#
# Builds OpenSSH's sftp-server as a static armeabi-v7a, arm64-v8a or x86_64 binary, no NDK - same reasoning as
# ../dropbear/build.sh. Built --without-openssl (sftp-server does no crypto of its own; that
# already happened in dropbear before this gets exec'd) to avoid a much bigger dependency;
# zlib is still required by OpenSSH's shared build, so it's built here too, statically.
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
mkdir -p "$OUT"
# configure writes into the source trees: one copy per architecture.
for t in zlib openssh; do
	if [ ! -d "$OUT/$t" ]; then
		cp -a "$HERE/src/$t" "$OUT/$t"
		# A tree built before for another architecture must not lend it its objects.
		find "$OUT/$t" \( -name '*.o' -o -name '*.a' \) -delete
	fi
done

stamp() { [ -f "$OUT/$1.done" ]; }
done_() { touch "$OUT/$1.done"; }

ZPREFIX=$OUT/zlib-prefix
if ! stamp zlib; then
	(cd "$OUT/zlib" && CC=$HOST-gcc AR=$HOST-ar \
		./configure --static --prefix="$ZPREFIX" >/dev/null &&
		make -j"$(nproc)" libz.a >/dev/null && make install >/dev/null)
	done_ zlib
fi

SRC=$OUT/openssh
if ! stamp configure; then
	(cd "$SRC" && CC=$HOST-gcc ./configure --host=$HOST \
		--without-openssl --with-zlib="$ZPREFIX" \
		LDFLAGS="-static -L$ZPREFIX/lib" CPPFLAGS="-I$ZPREFIX/include" >/dev/null)
	done_ configure
fi

make -C "$SRC" sftp-server -j"$(nproc)" >/dev/null
cp "$SRC/sftp-server" "$OUT/"
file "$OUT/sftp-server"
ls -l "$OUT/sftp-server"
