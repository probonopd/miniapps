#!/bin/sh
# Build audioroute.dex (run with app_process as root on the projector) without the Android SDK, like ../../build.sh.
# Usage: sh build.sh [outdir]  (the project disk is mounted noexec, so this only reads the sources from there)
set -eu
here=$(cd "$(dirname "$0")" && pwd)
lib=$here/../../build/libs
stubs=$lib/android-all-11-robolectric-6757853.jar
out=${1:-$here}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
javac --release 11 -encoding UTF-8 -Xlint:all,-options,-classfile -cp "$stubs" -d "$work/classes" "$here/AudioRoute.java"
jar cf "$work/audioroute.jar" -C "$work/classes" .
jdk=$(dirname "$(dirname "$(readlink -f "$(command -v javac)")")")
mkdir "$work/dex"
java -cp "$lib"/r8-*.jar com.android.tools.r8.D8 --release --min-api 21 --lib "$stubs" --lib "$jdk" \
	--output "$work/dex" "$work/audioroute.jar"
cp "$work/dex/classes.dex" "$out/audioroute.dex"
ls -l "$out/audioroute.dex"
