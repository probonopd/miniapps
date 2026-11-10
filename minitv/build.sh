#!/bin/sh
# Build and sign minitv.apk without the Android SDK.
# Needs: JDK 17+ (javac, jar), apktool with a framework installed, python3 + "cryptography", curl, unzip, zip.
# The APK carries the speech model (about 46 MB) and one native library of 9 MB.
# Usage: sh build.sh
set -eu
here=$(cd "$(dirname "$0")" && pwd)
b=$here/build
lib=$b/libs
. "$here/../common/apk.sh"
apk_fetch "$here"

# AAR -> classes.jar
mkdir -p "$b/jars"
for aar in "$lib"/*.aar; do
	unzip -p "$aar" classes.jar >"$b/jars/$(basename "$aar" .aar).jar"
done
cp "$lib"/guava-*.jar "$lib"/failureaccess-*.jar "$lib"/hivemq-mqtt-client-shaded-*.jar \
	"$lib"/rxjava-*.jar "$lib"/reactive-streams-*.jar "$lib"/annotations-*.jar "$b/jars/"
stubs=$lib/android-all-11-robolectric-6757853.jar
cp_all=$(ls "$b"/jars/*.jar | tr '\n' ':')

# Native libraries (only 32-bit ARM: the projector's userspace is armeabi-v7a) and the speech model as an asset
rm -rf "$b/native" "$b/model"
mkdir -p "$b/model"
# ARCHS: armv7 for the devices it was made for; CI adds arm64 and x86_64 (Vosk ships all of them).
for arch in ${ARCHS:-armv7}; do
	abi=$(apk_abi "$arch")
	mkdir -p "$b/native/lib/$abi"
	for aar in "$lib"/vosk-android-*.aar "$lib"/jna-*.aar; do
		unzip -o -j -q "$aar" "jni/$abi/*.so" -d "$b/native/lib/$abi"
	done
done
unzip -q "$lib"/vosk-model-small-de-*.zip -d "$b/model"
mkdir "$b/model/assets"
mv "$b"/model/vosk-model-small-de-* "$b/model/assets/model"

# Java -> classes -> dex, then manifest and resources, dex, native libraries and the speech model into the APK
apk_compile "$b" "$stubs" 21 "${cp_all%:}" "$(ls "$b"/jars/*.jar | tr '\n' ' ')" "$here"/java/org/minitv/*.java
apk_package_abis "$here/src" 21 "$here/minitv" "$b" "$b/native" "$b/model"
