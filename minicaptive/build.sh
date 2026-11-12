#!/bin/sh
#
# Copyright (c) 2026 Simon Peter
#
# SPDX-License-Identifier: MIT
#
# Build and sign minicaptive.apk without the Android SDK.
# Needs: JDK 17+ (javac, jar), apktool with a framework installed, python3 + "cryptography", curl, zip.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
b=$here/build
lib=$b/libs
. "$here/../common/apk.sh"
apk_fetch "$here"
stubs=$lib/android-all-14-robolectric-10818077.jar
jars=$lib/jsoup-1.17.2.jar

JAVAC_FLAGS=-Werror apk_compile "$b" "$stubs" 24 "$jars" "$jars" "$here"/java/org/minicaptive/*.java
apk_package "$here/src" 24 "$APK_VERSION_CODE" "$APK_VERSION_NAME" "$here/minicaptive.apk" "$b"
