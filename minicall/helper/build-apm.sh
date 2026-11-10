#!/bin/bash
#
# Copyright (c) 2026 Simon Peter
#
# SPDX-License-Identifier: BSD-2-Clause
#
# Builds abseil-cpp and webrtc-audio-processing 1.3 (the "-1" API baresip's
# webrtc_aec module uses) as static libraries into out/<arch>/prefix, with zig
# and without an NDK. Kept apart from build.sh so the baresip build can run
# without the C++ toolchain cost; run build.sh first or let this create the wrappers.
#
#   ./build-apm.sh host|armv7|arm64
set -euo pipefail
cd "$(dirname "$0")"
HERE=$(pwd)

case "${1:?usage: build-apm.sh host|armv7|arm64}" in
	host)  TARGET=x86_64-linux-musl; CPU=; CPUFAM=x86_64; CPUNAME=x86_64; NEON=no;;
	armv7) TARGET=arm-linux-musleabihf; CPU=-mcpu=generic+v7a+neon; CPUFAM=arm; CPUNAME=armv7; NEON=yes;;
	arm64) TARGET=aarch64-linux-musl; CPU=; CPUFAM=aarch64; CPUNAME=aarch64; NEON=auto;;
	*) echo "unknown target $1" >&2; exit 1;;
esac
ARCH=$1
ZIG=${ZIG:-$HOME/opt/zig-venv/lib/python3.13/site-packages/ziglang/zig}
[ -x "$ZIG" ] || { echo "zig not found at $ZIG" >&2; exit 1; }
command -v meson >/dev/null || { echo "meson not found (pip install meson into ~/opt/zig-venv)" >&2; exit 1; }
JOBS=$(nproc)

OUT=$HERE/out/$ARCH
PREFIX=$OUT/prefix
BIN=$OUT/bin
mkdir -p "$PREFIX" "$BIN" "$OUT/build"
./fetch.sh >/dev/null

# Same wrappers as build.sh, rewritten here so the script also works standalone.
cat > "$BIN/cc" <<EOS
#!/bin/sh
exec "$ZIG" cc -target $TARGET $CPU "\$@"
EOS
cat > "$BIN/c++" <<EOS
#!/bin/sh
exec "$ZIG" c++ -target $TARGET $CPU "\$@"
EOS
printf '#!/bin/sh\nexec "%s" ar "$@"\n' "$ZIG" > "$BIN/ar"
printf '#!/bin/sh\nexec "%s" ranlib "$@"\n' "$ZIG" > "$BIN/ranlib"
chmod +x "$BIN"/*

stamp() { [ -f "$OUT/build/$1.done" ]; }
done_() { touch "$OUT/build/$1.done"; }

# -D_LARGEFILE64_SOURCE: zig's musl hides off64_t otherwise, which abseil's direct_mmap.h uses.
# abseil by cmake, not by the meson wrap: the wrap needs a second download (the
# wrapdb patch zip) and cmake installs the absl_*.pc files meson looks for.
if ! stamp apm-abseil; then
	rm -rf "$OUT/build/apm-abseil"
	cmake -S src/abseil-cpp -B "$OUT/build/apm-abseil" -G Ninja \
		-DCMAKE_SYSTEM_NAME=Linux -DCMAKE_SYSTEM_PROCESSOR="$CPUNAME" \
		-DCMAKE_C_COMPILER="$BIN/cc" -DCMAKE_CXX_COMPILER="$BIN/c++" \
		-DCMAKE_AR="$BIN/ar" -DCMAKE_RANLIB="$BIN/ranlib" \
		-DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$PREFIX" \
		-DCMAKE_POSITION_INDEPENDENT_CODE=ON -DBUILD_SHARED_LIBS=OFF \
		-DCMAKE_CXX_STANDARD=17 -DABSL_PROPAGATE_CXX_STD=ON \
		-DCMAKE_CXX_FLAGS=-D_LARGEFILE64_SOURCE \
		-DABSL_ENABLE_INSTALL=ON -DBUILD_TESTING=OFF -DABSL_BUILD_TESTING=OFF >"$OUT/build/apm-abseil.log" 2>&1
	cmake --build "$OUT/build/apm-abseil" -j"$JOBS" >/dev/null
	cmake --install "$OUT/build/apm-abseil" >/dev/null
	done_ apm-abseil
fi

if ! stamp apm-webrtc; then
	# Meson has no way to ask zig for "ar" via a single token, hence the wrapper scripts.
	cat > "$OUT/build/apm-cross.txt" <<EOS
[binaries]
c = '$BIN/cc'
cpp = '$BIN/c++'
ar = '$BIN/ar'
ranlib = '$BIN/ranlib'
strip = 'true'
pkg-config = 'pkg-config'

[built-in options]
default_library = 'static'
prefer_static = true
# Only the prefix is searched, never the build machine's own .pc files.
pkg_config_path = '$PREFIX/lib/pkgconfig'
cpp_std = 'c++17'

[properties]
needs_exe_wrapper = true

[host_machine]
system = 'linux'
cpu_family = '$CPUFAM'
cpu = '$CPUNAME'
endian = 'little'
EOS
	rm -rf "$OUT/build/apm-webrtc"
	PKG_CONFIG_LIBDIR="$PREFIX/lib/pkgconfig" \
	meson setup "$OUT/build/apm-webrtc" src/webrtc-audio-processing \
		--cross-file "$OUT/build/apm-cross.txt" --prefix "$PREFIX" --libdir lib \
		--buildtype release -Dneon="$NEON" >/dev/null
	ninja -C "$OUT/build/apm-webrtc" -j"$JOBS" >/dev/null
	ninja -C "$OUT/build/apm-webrtc" install >/dev/null
	done_ apm-webrtc
fi
ls -l "$PREFIX"/lib/libwebrtc-audio-processing-1.a
