#!/bin/bash
#
# Copyright (c) 2026 Simon Peter
#
# SPDX-License-Identifier: BSD-2-Clause
#
# Builds the baresip helper as one static musl binary with zig, no NDK:
# a musl process cannot live inside the app's Dalvik process (wrong libc ABI),
# so the app runs it as a child and talks to it over sockets and pipes.
#
#   ./build.sh host    x86_64, for tests on this machine (the same binary as x86_64)
#   ./build.sh x86_64  x86_64, for Intel Android devices and emulators
#   ./build.sh armv7   armeabi-v7a, for the Echo Show
#   ./build.sh arm64   arm64-v8a, for phones without 32-bit support
set -euo pipefail
cd "$(dirname "$0")"
HERE=$(pwd)

case "${1:?usage: build.sh host|x86_64|armv7|arm64}" in
	host|x86_64) TARGET=x86_64-linux-musl; CPU=; OSSL=linux-x86_64; VPXT=generic-gnu;;
	armv7) TARGET=arm-linux-musleabihf; CPU=-mcpu=generic+v7a+neon; OSSL=linux-armv4; VPXT=generic-gnu;;
	arm64) TARGET=aarch64-linux-musl; CPU=; OSSL=linux-aarch64; VPXT=generic-gnu;;
	*) echo "unknown target $1" >&2; exit 1;;
esac
ARCH=$1
ZIG=${ZIG:-$HOME/opt/zig-venv/lib/python3.13/site-packages/ziglang/zig}
[ -x "$ZIG" ] || { echo "zig not found at $ZIG" >&2; exit 1; }
JOBS=$(nproc)

OUT=$HERE/out/$ARCH
PREFIX=$OUT/prefix
BIN=$OUT/bin
mkdir -p "$PREFIX" "$BIN" "$OUT/build"

# Autotools and OpenSSL's Configure want plain program names, not "zig cc -target".
cat > "$BIN/cc" <<EOF
#!/bin/sh
exec "$ZIG" cc -target $TARGET $CPU "\$@"
EOF
cat > "$BIN/c++" <<EOF
#!/bin/sh
exec "$ZIG" c++ -target $TARGET $CPU "\$@"
EOF
printf '#!/bin/sh\nexec "%s" ar "$@"\n' "$ZIG" > "$BIN/ar"
printf '#!/bin/sh\nexec "%s" ranlib "$@"\n' "$ZIG" > "$BIN/ranlib"
chmod +x "$BIN"/*
export CC=$BIN/cc CXX=$BIN/c++ AR=$BIN/ar RANLIB=$BIN/ranlib

CMAKE_COMMON=(
	-G Ninja
	-DCMAKE_SYSTEM_NAME=Linux
	-DCMAKE_C_COMPILER="$CC" -DCMAKE_CXX_COMPILER="$CXX"
	-DCMAKE_AR="$AR" -DCMAKE_RANLIB="$RANLIB"
	-DCMAKE_BUILD_TYPE=Release
	-DCMAKE_INSTALL_PREFIX="$PREFIX"
	-DCMAKE_PREFIX_PATH="$PREFIX"
	-DCMAKE_FIND_ROOT_PATH="$PREFIX"
	-DCMAKE_FIND_ROOT_PATH_MODE_LIBRARY=ONLY
	-DCMAKE_FIND_ROOT_PATH_MODE_INCLUDE=ONLY
	-DCMAKE_FIND_ROOT_PATH_MODE_PACKAGE=ONLY
	-DCMAKE_POSITION_INDEPENDENT_CODE=ON
	-DBUILD_SHARED_LIBS=OFF
)

stamp() { [ -f "$OUT/build/$1.done" ]; }
done_() { touch "$OUT/build/$1.done"; }

if ! stamp openssl; then
	rm -rf "$OUT/build/openssl" && cp -a src/openssl "$OUT/build/openssl"
	(cd "$OUT/build/openssl" &&
		./Configure "$OSSL" no-shared no-tests no-docs no-apps no-engine no-module no-asm \
			--prefix="$PREFIX" --libdir=lib --cross-compile-prefix= CC="$CC" AR="$AR" RANLIB="$RANLIB" >/dev/null &&
		make -j"$JOBS" build_libs >/dev/null && make install_dev >/dev/null)
	done_ openssl
fi

if ! stamp opus; then
	cmake -S src/opus -B "$OUT/build/opus" "${CMAKE_COMMON[@]}" \
		-DOPUS_BUILD_PROGRAMS=OFF -DOPUS_BUILD_TESTING=OFF >/dev/null
	cmake --build "$OUT/build/opus" -j"$JOBS" >/dev/null && cmake --install "$OUT/build/opus" >/dev/null
	done_ opus
fi

if ! stamp libvpx; then
	rm -rf "$OUT/build/libvpx" && mkdir -p "$OUT/build/libvpx"
	# generic-gnu: no hand-written assembly, which needs a GNU assembler zig does not provide for armv7.
	(cd "$OUT/build/libvpx" &&
		CROSS= LD="$CC" AS="$CC" "$HERE/src/libvpx/configure" --target="$VPXT" --prefix="$PREFIX" \
			--disable-examples --disable-tools --disable-docs --disable-unit-tests --disable-vp9 \
			--enable-static --disable-shared --enable-pic --enable-realtime-only >/dev/null &&
		make -j"$JOBS" >/dev/null && make install >/dev/null)
	done_ libvpx
fi

if ! stamp re; then
	cmake -S src/re -B "$OUT/build/re" "${CMAKE_COMMON[@]}" \
		-DLIBRE_BUILD_SHARED=OFF -DUSE_RTMP=OFF -DUSE_BFCP=OFF -DUSE_PCP=OFF \
		-DOPENSSL_ROOT_DIR="$PREFIX" -DOPENSSL_USE_STATIC_LIBS=ON >/dev/null
	cmake --build "$OUT/build/re" -j"$JOBS" >/dev/null && cmake --install "$OUT/build/re" >/dev/null
	done_ re
fi

# WebRTC's audio processing (echo canceller, noise suppression, gain control) for the webrtc_aec module.
"$HERE/build-apm.sh" "$ARCH"
# The static link list from its pkg-config file, as a CMake list: library, abseil, libc++ (C++ code in a C program).
APM_LIBS="$PREFIX/lib/libwebrtc-audio-processing-1.a;-L$PREFIX/lib"
for f in $(grep -o -- '-l[^ ]*' "$OUT/build/apm-flags.txt"); do
	[ "$f" = -lwebrtc-audio-processing-1 ] || APM_LIBS="$APM_LIBS;$f"
done
APM_LIBS="$APM_LIBS;-lc++"

# Only what a call needs: no sound or camera modules, the app feeds both over pipes.
MODULES="account;contact;menu;ctrl_tcp;cons;aufile;ausine;fakevideo;g711;opus;vp8;ice;stun;turn;srtp;dtls_srtp;auconv;auresamp;selfview;webrtc_aec"
cmake -S src/baresip -B "$OUT/build/baresip" "${CMAKE_COMMON[@]}" \
	-DSTATIC=ON -DMODULES="$MODULES" \
	-DWEBRTC_AEC_INCLUDE_DIR="$PREFIX/include/webrtc-audio-processing-1;$PREFIX/include" \
	-DWEBRTC_AEC_LIBRARY="$APM_LIBS" \
	-DAPP_MODULES_DIR="$HERE/modules" -DAPP_MODULES="sockaudio;sockvideo;ctrl_stdio" \
	-DOPENSSL_ROOT_DIR="$PREFIX" -DOPENSSL_USE_STATIC_LIBS=ON \
	-DCMAKE_EXE_LINKER_FLAGS="-static -s" >/dev/null
cmake --build "$OUT/build/baresip" -j"$JOBS" --target baresip_exe >/dev/null
cp "$OUT/build/baresip/baresip" "$OUT/baresip"
file "$OUT/baresip"
ls -l "$OUT/baresip"
