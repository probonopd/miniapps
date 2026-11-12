#!/bin/bash
# Build micpipe.jar. Uses the Android stubs and D8 fetched by minitv's fetch.sh.
set -euo pipefail
cd "$(dirname "$0")"
# minitv's downloads, next door in the miniapps tree (sh minitv/build.sh fetches them).
LIB=${MINITV_LIBS:-$(cd ../../../minitv && pwd)/build/libs}
[ -d "$LIB" ] || { echo "no $LIB: run minitv/build.sh once (or set MINITV_LIBS)" >&2; exit 1; }
STUBS=$LIB/android-all-11-robolectric-6757853.jar
OUT=$(mktemp -d); trap 'rm -rf "$OUT"' EXIT
javac --release 11 -cp "$STUBS" -d "$OUT" micpipe/MicPipe.java
JDK=$(dirname "$(dirname "$(readlink -f "$(command -v javac)")")")
java -cp "$LIB"/r8-*.jar com.android.tools.r8.D8 --release --min-api 21 --lib "$STUBS" --lib "$JDK" \
    --output "$OUT" "$OUT"/MicPipe*.class
(cd "$OUT" && jar cf micpipe.jar classes.dex)
cp "$OUT/micpipe.jar" micpipe.jar
