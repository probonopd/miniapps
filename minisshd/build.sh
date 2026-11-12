#!/bin/sh
#
# Copyright (c) 2026 Simon Peter
#
# SPDX-License-Identifier: MIT
#
# Build and sign minisshd.apk without the Android SDK.
# Needs: JDK 17+ (javac, jar), apktool with a framework installed, python3 + "cryptography", curl, zip,
# and the binaries built by dropbear/build.sh and sftp-server/build.sh.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
b=$here/build
lib=$b/libs
. "$here/../common/apk.sh"
apk_fetch "$here"
stubs=$lib/android-all-14-robolectric-10818077.jar

# Only lib*.so files are extracted at install, so dropbear, dropbearkey and sftp-server travel
# under those names (sftpserver, since a .so name can't carry the hyphen). ARCHS: armv7 by default,
# since the TV boxes' userspace is 32-bit even where the chip is 64-bit capable; CI adds arm64 and x86_64.
rm -rf "$b/native"
for arch in ${ARCHS:-armv7}; do
	abi=$(apk_abi "$arch")
	mkdir -p "$b/native/lib/$abi"
	for bin in dropbear dropbearkey; do
		out=$here/dropbear/out/$arch/$bin
		[ -x "$out" ] || { echo "missing $out: run dropbear/build.sh $arch" >&2; exit 1; }
		cp "$out" "$b/native/lib/$abi/lib$bin.so"
	done
	out=$here/sftp-server/out/$arch/sftp-server
	[ -x "$out" ] || { echo "missing $out: run sftp-server/build.sh $arch" >&2; exit 1; }
	cp "$out" "$b/native/lib/$abi/libsftpserver.so"
done

apk_compile "$b" "$stubs" 24 "" "" "$here"/java/org/minisshd/*.java
apk_package_abis "$here/src" 24 "$here/minisshd" "$b" "$b/native"
