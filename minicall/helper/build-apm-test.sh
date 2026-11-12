#!/bin/bash
#
# Copyright (c) 2026 Simon Peter
#
# SPDX-License-Identifier: MIT
#
# Links test/apm-test.cc statically against out/<arch>/prefix; runs it for host.
set -euo pipefail
cd "$(dirname "$0")"
ARCH=${1:?usage: build-apm-test.sh host|armv7|arm64}
P=$PWD/out/$ARCH/prefix
# pkg-config --static supplies the abseil libs in dependency order.
PKG_CONFIG_LIBDIR=$P/lib/pkgconfig pkg-config --cflags --libs --static webrtc-audio-processing-1 > out/$ARCH/build/apm-flags.txt
# shellcheck disable=SC2046
out/$ARCH/bin/c++ -std=c++17 -O2 -Wall -Wextra -DWEBRTC_LINUX test/apm-test.cc -o out/$ARCH/apm-test $(cat out/$ARCH/build/apm-flags.txt) -static
file out/$ARCH/apm-test
[ "$ARCH" = host ] && out/$ARCH/apm-test
exit 0
