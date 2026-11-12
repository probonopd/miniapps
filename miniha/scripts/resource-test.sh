#!/bin/sh
# Measures a MiniHA process against the SPEC section 10 budget: binary size,
# idle and peak RSS, average CPU and bytes written while cmd/loadgen drives
# 300 sensors at 10 messages per second.
#
# Requires go and curl. Linux reads /proc; other systems fall back to ps(1).
# MB in the gate means MiB (1048576 bytes), because /proc reports KiB.
#
# Environment:
#   RESOURCE_TEST_DURATION  load duration in seconds (default 60)
#   RESOURCE_TEST_NO_GATE   set to 1 to print the table without failing
set -eu

HTTP_PORT=18123
MQTT_LISTEN=127.0.0.1:11883
DURATION=${RESOURCE_TEST_DURATION:-60}
MAX_RSS_KB=40960        # 40 MiB, SPEC section 10
MAX_BIN_BYTES=12582912  # 12 MiB, SPEC section 10
SAMPLE_SECONDS=5

cd "$(dirname "$0")/.."

MINIHA_PID=""
LG_PID=""
GATE_FAIL=0
BIN_DIR=$(mktemp -d)
DATA=$(mktemp -d)
LOG=$(mktemp)

die() {
  echo "resource-test: $*" >&2
  exit 1
}

cleanup() {
  status=$?
  if [ -n "$LG_PID" ] && kill -0 "$LG_PID" 2>/dev/null; then
    kill -TERM "$LG_PID" 2>/dev/null || true
  fi
  if [ -n "$MINIHA_PID" ] && kill -0 "$MINIHA_PID" 2>/dev/null; then
    kill -TERM "$MINIHA_PID" 2>/dev/null || true
    wait "$MINIHA_PID" 2>/dev/null || true
  fi
  if [ "$status" -ne 0 ] && [ -s "$LOG" ]; then
    echo "--- miniha log, last 20 lines ---" >&2
    tail -n 20 "$LOG" >&2
  fi
  rm -rf "$BIN_DIR" "$DATA" "$LOG"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# Each query returns one number, so the caller never has to know which
# source it came from.
proc_rss_kb() {
  if [ -r "/proc/$MINIHA_PID/status" ]; then
    awk '/^VmRSS:/ { print $2 }' "/proc/$MINIHA_PID/status"
  else
    ps -o rss= -p "$MINIHA_PID" | tr -d ' '
  fi
}

# Seconds of CPU time used so far, from utime+stime in clock ticks on Linux.
proc_cpu_seconds() {
  if [ -r "/proc/$MINIHA_PID/stat" ]; then
    # Strip "pid (comm) " first: the command name may contain spaces.
    sed 's/.*) //' "/proc/$MINIHA_PID/stat" | awk -v hz="$HZ" '{ print ($12 + $13) / hz }'
  else
    # ps prints [[dd-]hh:]mm:ss[.ss]; a day field would be ambiguous here, so refuse it.
    ps -o time= -p "$MINIHA_PID" | awk '{
      if (index($1, "-")) { exit 1 }
      n = split($1, p, ":"); s = 0
      for (i = 1; i <= n; i++) { s = s * 60 + p[i] }
      print s
    }'
  fi
}

proc_write_bytes() {
  if [ -r "/proc/$MINIHA_PID/io" ]; then
    awk '/^write_bytes:/ { print $2 }' "/proc/$MINIHA_PID/io"
  else
    echo n/a
  fi
}

for tool in go curl; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool not found in PATH"
done
HZ=$(getconf CLK_TCK)

echo "building miniha and loadgen"
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$BIN_DIR/miniha" ./cmd/miniha
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$BIN_DIR/loadgen" ./cmd/loadgen
BIN_BYTES=$(wc -c < "$BIN_DIR/miniha" | tr -d ' ')

cat > "$DATA/configuration.yaml" <<CONF
homeassistant:
  name: Resource test
  unit_system: metric
mqtt:
  listen: $MQTT_LISTEN
  discovery_prefix: homeassistant
http:
  server_host: 127.0.0.1
  server_port: $HTTP_PORT
CONF

"$BIN_DIR/miniha" -data "$DATA" run >"$LOG" 2>&1 &
MINIHA_PID=$!

# curl exits 0 on any HTTP answer, which is what "API ready" means here.
i=0
until curl -s -o /dev/null -m 2 "http://127.0.0.1:$HTTP_PORT/"; do
  i=$((i + 1))
  [ "$i" -lt 60 ] || die "HTTP port $HTTP_PORT not answering after 60 s"
  kill -0 "$MINIHA_PID" 2>/dev/null || die "miniha exited during startup"
  sleep 1
done

# Let startup work (discovery store, recorder) settle so the idle figure is idle.
sleep 2
IDLE_RSS_KB=$(proc_rss_kb)
MAX_RSS_KB_SEEN=$IDLE_RSS_KB

T0=$(date +%s)
CPU0=$(proc_cpu_seconds)
IO0=$(proc_write_bytes)

"$BIN_DIR/loadgen" -broker "$MQTT_LISTEN" -duration "${DURATION}s" &
LG_PID=$!

while kill -0 "$LG_PID" 2>/dev/null; do
  sleep "$SAMPLE_SECONDS"
  kill -0 "$MINIHA_PID" 2>/dev/null || die "miniha died during the load"
  rss=$(proc_rss_kb)
  if [ "$rss" -gt "$MAX_RSS_KB_SEEN" ]; then
    MAX_RSS_KB_SEEN=$rss
  fi
done
LG_STATUS=0
wait "$LG_PID" || LG_STATUS=$?
LG_PID=""
[ "$LG_STATUS" -eq 0 ] || die "loadgen failed with status $LG_STATUS"

T1=$(date +%s)
CPU1=$(proc_cpu_seconds)
IO1=$(proc_write_bytes)
rss=$(proc_rss_kb)
if [ "$rss" -gt "$MAX_RSS_KB_SEEN" ]; then
  MAX_RSS_KB_SEEN=$rss
fi

kill -TERM "$MINIHA_PID"
MINIHA_STATUS=0
wait "$MINIHA_PID" || MINIHA_STATUS=$?
MINIHA_PID=""
[ "$MINIHA_STATUS" -eq 0 ] || die "miniha exited with status $MINIHA_STATUS after TERM"

ELAPSED=$((T1 - T0))
[ "$ELAPSED" -gt 0 ] || die "load window too short to measure"
CPU_PCT=$(awk -v a="$CPU0" -v b="$CPU1" -v s="$ELAPSED" 'BEGIN { printf "%.1f", (b - a) / s * 100 }')
if [ "$IO0" = n/a ] || [ "$IO1" = n/a ]; then
  BYTES_WRITTEN=n/a
else
  BYTES_WRITTEN=$((IO1 - IO0))
fi

printf '%-34s %s\n' "Binary size (stripped)" \
  "$(awk -v b="$BIN_BYTES" 'BEGIN { printf "%d bytes (%.2f MB)", b, b / 1048576 }')"
printf '%-34s %s kB\n' "Idle RSS (before load)" "$IDLE_RSS_KB"
printf '%-34s %s kB\n' "Max RSS (during load)" "$MAX_RSS_KB_SEEN"
printf '%-34s %s %% of one core over %ss\n' "Average CPU" "$CPU_PCT" "$ELAPSED"
printf '%-34s %s bytes over %ss\n' "Bytes written (write_bytes)" "$BYTES_WRITTEN" "$ELAPSED"

if [ "$BIN_BYTES" -gt "$MAX_BIN_BYTES" ]; then
  echo "FAIL: binary is larger than 12 MB" >&2
  GATE_FAIL=1
fi
if [ "$MAX_RSS_KB_SEEN" -gt "$MAX_RSS_KB" ]; then
  echo "FAIL: max RSS is above 40 MB" >&2
  GATE_FAIL=1
fi

if [ "$GATE_FAIL" -ne 0 ] && [ "${RESOURCE_TEST_NO_GATE:-0}" != 1 ]; then
  exit 1
fi
