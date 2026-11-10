#!/bin/sh
#
# Copyright (c) 2026 Simon Peter
#
# SPDX-License-Identifier: BSD-2-Clause
#
# Build and sign minicall.apk without the Android SDK.
# Needs: JDK 17+ (javac, jar), apktool with a framework installed, python3 + "cryptography", curl, zip,
# and the helper built by helper/build.sh armv7.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
b=$here/build
lib=$b/libs
. "$here/../common/apk.sh"
apk_fetch "$here"
stubs=$lib/android-all-14-robolectric-10818077.jar

# Only lib*.so files are extracted at install, so the helper program travels under that name.
# 32-bit for the Echo Show and its like, 64-bit for phones that no longer run 32-bit programs.
# ARCHS: the helpers to package (CI: armv7 arm64 x86_64).
rm -rf "$b/native"
for arch in ${ARCHS:-armv7 arm64}; do
	helper=$here/helper/out/$arch/baresip
	[ -x "$helper" ] || { echo "missing $helper: run helper/build.sh $arch" >&2; exit 1; }
	abi=$(apk_abi "$arch")
	mkdir -p "$b/native/lib/$abi"
	cp "$helper" "$b/native/lib/$abi/libbaresip.so"
done

# MQTT for calls from the web page
jars="$lib/hivemq-mqtt-client-shaded-1.4.1.jar $lib/rxjava-2.2.21.jar $lib/reactive-streams-1.0.4.jar"

JAVAC_FLAGS=-Werror apk_compile "$b" "$stubs" 24 "$(echo $jars | tr ' ' ':')" "$jars" "$here"/java/org/minicall/*.java
apk_package_abis "$here/src" 24 "$here/minicall" "$b" "$b/native"
