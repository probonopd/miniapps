#!/bin/sh
# Host tests of the voice matching and the spoken station names (plain Java, no Android needed). Usage: sh test.sh
set -eu
here=$(cd "$(dirname "$0")" && pwd)
out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT
javac -encoding UTF-8 -d "$out" "$here/java/org/minitv/ChannelMatcher.java" "$here/test/ChannelMatcherTest.java"
java -cp "$out" org.minitv.ChannelMatcherTest
javac -encoding UTF-8 -d "$out" "$here/java/org/minitv/SpokenName.java" "$here/test/SpokenNameTest.java"
java -cp "$out" org.minitv.SpokenNameTest
