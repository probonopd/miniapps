#!/bin/sh
#
# Copyright (c) 2026 Simon Peter
#
# SPDX-License-Identifier: BSD-2-Clause
#
# The steps every mini app's build.sh shares, without the Android SDK: Java to dex with javac and D8, manifest and
# resources with apktool, and a v1 signature (see sign.py) with the one key all mini apps share. One key, because
# the apps trust each other through signature permissions (minicall takes calls only from apps signed like it),
# and because Android only installs an update signed with the key of the installed version.
#
# Usage, in an app's build.sh:  . "$here/../common/apk.sh"

MINIAPPS=$(cd "$(dirname "$here")" && pwd)
# Outside the repository's tracked files (keys/ is ignored): never to be committed or published.
MINIAPPS_KEY=$MINIAPPS/keys/signing-key.pem

# apk_fetch APPDIR: the app's pinned build inputs into APPDIR/build/libs.
apk_fetch() {
	sh "$MINIAPPS/common/fetch.sh" "$1"
}

# apk_compile BUILDDIR STUBS MIN_API CLASSPATH "DEX_JARS" JAVA_FILES...
# Compiles against the android.jar stubs and the classpath, then dexes the classes and DEX_JARS (space separated)
# into BUILDDIR/dex. JAVAC_FLAGS adds flags (minicall: -Werror).
apk_compile() {
	_b=$1 _stubs=$2 _api=$3 _cp=$4 _dexjars=$5
	shift 5
	rm -rf "$_b/classes" "$_b/dex"
	mkdir -p "$_b/classes" "$_b/dex"
	# shellcheck disable=SC2086 # JAVAC_FLAGS is a list of flags
	javac --release 11 -encoding UTF-8 -Xlint:all,-options,-classfile,-processing ${JAVAC_FLAGS:-} \
		-cp "${_cp:+$_cp:}$_stubs" -d "$_b/classes" "$@"
	jar cf "$_b/app.jar" -C "$_b/classes" .
	_jdk=$(dirname "$(dirname "$(readlink -f "$(command -v javac)")")")
	# shellcheck disable=SC2086 # the jar list is meant to split
	java -cp "$_b"/libs/r8-*.jar com.android.tools.r8.D8 --release --min-api "$_api" --lib "$_stubs" --lib "$_jdk" \
		--output "$_b/dex" "$_b/app.jar" $_dexjars
}

# Version of the APKs: CI passes its run number, local builds keep 1.
APK_VERSION_CODE=${APK_VERSION_CODE:-1}
APK_VERSION_NAME=${APK_VERSION_NAME:-1.0}

# apk_abi ARCH: the Android ABI directory of an architecture name of the native builds.
apk_abi() {
	case $1 in
	armv7) echo armeabi-v7a ;;
	arm64) echo arm64-v8a ;;
	x86_64) echo x86_64 ;;
	*) echo "unknown architecture $1" >&2; return 1 ;;
	esac
}

# apk_package_abis SRCDIR MIN_SDK OUT_BASE BUILDDIR NATIVEDIR [DIR...]
# With APK_SPLIT=1 one APK per ABI in NATIVEDIR/lib, named OUT_BASE-<abi>.apk (CI releases: Obtainium and
# people pick theirs by the name, and each stays small); else one OUT_BASE.apk with all of them.
apk_package_abis() {
	_pa_src=$1 _pa_min=$2 _pa_base=$3 _pa_b=$4 _pa_nat=$5
	shift 5
	if [ "${APK_SPLIT:-0}" != 1 ]; then
		apk_package "$_pa_src" "$_pa_min" "$APK_VERSION_CODE" "$APK_VERSION_NAME" "$_pa_base.apk" "$_pa_b" "$_pa_nat" "$@"
		return
	fi
	for _pa_abi in $(ls "$_pa_nat/lib"); do
		_pa_one=$(mktemp -d)
		mkdir -p "$_pa_one/lib"
		cp -r "$_pa_nat/lib/$_pa_abi" "$_pa_one/lib/"
		apk_package "$_pa_src" "$_pa_min" "$APK_VERSION_CODE" "$APK_VERSION_NAME" "$_pa_base-$_pa_abi.apk" "$_pa_b" \
			"$_pa_one" "$@"
		rm -rf "$_pa_one"
	done
}

# apk_package SRCDIR MIN_SDK VERSION_CODE VERSION_NAME OUT.apk BUILDDIR [DIR...]
# Manifest and resources of SRCDIR with apktool, the dex files of BUILDDIR/dex, and the contents of each DIR (native
# libraries, assets) go into OUT.apk, signed with the shared key. targetSdk 28: newer targets need a v2 signature.
apk_package() {
	_src=$1 _min=$2 _code=$3 _name=$4 _out=$5 _b=$6
	shift 6
	_work=$(mktemp -d)
	cp -r "$_src" "$_work/proj"
	cat >"$_work/proj/apktool.yml" <<YML
version: 2.9.3
usesFramework:
  ids:
  - 1
apkFileName: unsigned.apk
sdkInfo:
  minSdkVersion: '$_min'
  targetSdkVersion: '28'
packageInfo:
  renameManifestPackage: null
versionInfo:
  versionCode: '$_code'
  versionName: '$_name'
YML
	apktool b "$_work/proj" -o "$_work/unsigned.apk" >/dev/null
	(cd "$_b/dex" && zip -q "$_work/unsigned.apk" classes*.dex)
	for _d in "$@"; do
		(cd "$_d" && zip -q -r -D "$_work/unsigned.apk" .)
	done
	if [ "${APK_UNSIGNED:-0}" = 1 ]; then
		# CI: the build never sees the key; a separate job signs (common/sign.py) and publishes.
		cp "$_work/unsigned.apk" "$_out"
	else
		mkdir -p "$(dirname "$MINIAPPS_KEY")"
		python3 -I "$MINIAPPS/common/sign.py" "$_work/unsigned.apk" "$_out" "$MINIAPPS_KEY"
	fi
	rm -rf "$_work"
	ls -l "$_out"
}
