#!/bin/bash
# Build pryonwake with clang and lld only. Links against the device's own libraries,
# so one must be attached: pulled to a temporary directory, never kept.
set -euo pipefail
cd "$(dirname "$0")"
LIBS=$(mktemp -d); trap 'rm -rf "$LIBS"' EXIT
for l in libc.so libdl.so libm.so liblog.so libpryon.so; do
    adb ${ANDROID_SERIAL:+-s "$ANDROID_SERIAL"} pull "/system/lib/$l" "$LIBS/" >/dev/null
done
clang --target=armv7a-linux-androideabi -march=armv7-a -mfloat-abi=softfp -Os -fPIE \
    -ffreestanding -fno-stack-protector -Wall -Wextra -c pryonwake.c -o "$LIBS/pryonwake.o"
/usr/bin/ld.lld-19 -pie --dynamic-linker=/system/bin/linker --hash-style=both -z noexecstack \
    -e _start -o pryonwake "$LIBS/pryonwake.o" "$LIBS/libpryon.so" "$LIBS/libc.so" "$LIBS/libdl.so" \
    "$LIBS/libm.so" "$LIBS/liblog.so" --allow-shlib-undefined
