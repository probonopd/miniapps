#!/bin/bash
# Build echocap for ARMv7 with only clang and lld (apt: lld-19). No NDK needed.
set -euo pipefail
cd "$(dirname "$0")"
clang --target=armv7a-linux-eabi -march=armv7-a -mfloat-abi=soft -Os -static \
    -nostdlib -ffreestanding -fno-stack-protector --ld-path=/usr/bin/ld.lld-19 \
    -Wall -Wextra -Icompat -I/usr/include -I/usr/include/x86_64-linux-gnu \
    -o echocap echocap.c
