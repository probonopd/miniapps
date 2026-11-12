#!/bin/bash
# Full backup of an Echo Show 5 (2nd gen, cronos) while booted into TWRP.
#
#   ./backup-emmc.sh [--second-dump] [output-dir]
#
# Produces (never inside an existing backup):
#   raw/mmcblk0-full.img     whole eMMC, verified against the device MD5
#   raw/mmcblk0boot0/1.img   eMMC boot areas
#   images/pNN-*.img         GPT partitions (+ preloader/GPT area, backup GPT)
#   data-files/root/         /data file tree extracted with debugfs (convenience)
#   meta/                    identity, GPT, md5s, dmesg, comparison report
#   SHA256SUMS               hashes of every file in the backup
#
# The device is left with /data and /cache remounted, as TWRP had them.
source "$(dirname "$0")/lib.sh"

SECOND=0
OUT=
while [ $# -gt 0 ]; do
    case "$1" in
        --second-dump) SECOND=1; shift;;
        -h|--help) sed -n '2,20p' "$0"; exit 0;;
        *) OUT=$1; shift;;
    esac
done
OUT=${OUT:-$ROOT_DIR/backup-$(date +%Y%m%d-%H%M%S)}

[ -e "$OUT" ] && [ -n "$(ls -A "$OUT" 2>/dev/null || true)" ] \
    && die "output directory is not empty: $OUT"
mkdir -p "$OUT"/raw "$OUT"/meta "$OUT"/images "$OUT"/data-files

require_device
require_root
[ "$(adbs get-state 2>/dev/null)" = recovery ] || die "boot TWRP first (recovery state expected)"

SECTORS=$(dsh 'cat /sys/block/mmcblk0/size')
[ -n "$SECTORS" ] || die "cannot read eMMC size"
BYTES=$((SECTORS * 512))
FREE_KB=$(df -Pk "$(dirname "$OUT")" | awk 'NR==2 {print $4}')
NEED_KB=$(( BYTES / 1024 * 2 + 4 * 1024 * 1024 ))
[ "$SECOND" = 1 ] && NEED_KB=$(( NEED_KB + BYTES / 1024 ))
[ "$FREE_KB" -gt "$NEED_KB" ] \
    || die "not enough free space on $(dirname "$OUT"): need ~$((NEED_KB/1024/1024)) GB, have $((FREE_KB/1024/1024)) GB"

note "backup of $SERIAL -> $OUT"
note "eMMC: $BYTES bytes, free space: $((FREE_KB/1024/1024)) GB, need: $((NEED_KB/1024/1024)) GB"
confirm "This only reads from the device (except unmount/remount of /data,/cache)."

# ---------------------------------------------------------------- metadata
note "collecting metadata"
dsh getprop > "$OUT/meta/getprop.txt" 2>&1 || true
dsh 'cat /proc/cmdline' > "$OUT/meta/cmdline.txt" 2>&1 || true
dsh 'cat /proc/partitions' > "$OUT/meta/partitions.txt" 2>&1 || true
dsh 'ls -l /dev/block/by-name/' > "$OUT/meta/by-name.txt" 2>&1 || true
dsh 'cat /etc/recovery.fstab 2>/dev/null; cat /etc/twrp.fstab 2>/dev/null' > "$OUT/meta/recovery.fstab" 2>&1 || true
dsh 'dmesg' > "$OUT/meta/dmesg.txt" 2>&1 || true
{
    for f in cid csd name serial manfid oemid fwrev hwrev date life_time pre_eol_info; do
        printf '%-14s: ' "$f"
        dsh "cat /sys/block/mmcblk0/device/$f 2>/dev/null" || true
    done
} > "$OUT/meta/mmc-sysfs.txt"

# ------------------------------------------------------ unmount and md5s
note "unmounting /data /cache /sdcard for a consistent raw read"
dsh 'sync; umount /sdcard 2>/dev/null; umount /data 2>/dev/null; umount /cache 2>/dev/null; true'

note "device MD5 sums"
: > "$OUT/meta/device-md5.txt"
for dev in mmcblk0 mmcblk0boot0 mmcblk0boot1 $(seq -f 'mmcblk0p%g' 1 16) mmcblk0rpmb; do
    dsh "md5sum /dev/block/$dev 2>/dev/null" >> "$OUT/meta/device-md5.txt" || true
done

# ------------------------------------------------------------- dump 1
note "dump 1/1: whole eMMC ($BYTES bytes, ~$((BYTES/1024/1024/9)) s at 9 MB/s)"
adbs exec-out 'dd if=/dev/block/mmcblk0 bs=1048576 2>/dev/null' \
    | dd of="$OUT/raw/mmcblk0-full.img" bs=1048576 status=progress
[ "$(stat -c%s "$OUT/raw/mmcblk0-full.img")" = "$BYTES" ] || die "short dump"

DEVMD5=$(awk '/\/dev\/block\/mmcblk0$/ {print $1}' "$OUT/meta/device-md5.txt")
HOSTMD5=$(md5sum "$OUT/raw/mmcblk0-full.img" | awk '{print $1}')
[ -n "$DEVMD5" ] || die "device MD5 of mmcblk0 missing"
[ "$DEVMD5" = "$HOSTMD5" ] || die "dump mismatch: device $DEVMD5 host $HOSTMD5"
note "dump 1 verified (md5 $HOSTMD5)"

note "dumping eMMC boot areas"
for b in boot0 boot1; do
    adbs exec-out "dd if=/dev/block/mmcblk0$b bs=1048576 2>/dev/null" \
        > "$OUT/raw/mmcblk0$b.img"
    D=$(awk -v d="/dev/block/mmcblk0$b" '$2==d {print $1}' "$OUT/meta/device-md5.txt")
    H=$(md5sum "$OUT/raw/mmcblk0$b.img" | awk '{print $1}')
    [ -z "$D" ] || [ "$D" = "$H" ] || die "mmcblk0$b mismatch"
done
note "boot areas verified"

# ------------------------------------------------------------- dump 2
if [ "$SECOND" = 1 ]; then
    note "dump 2: whole eMMC (verification)"
    adbs exec-out 'dd if=/dev/block/mmcblk0 bs=1048576 2>/dev/null' \
        | dd of="$OUT/raw/mmcblk0-full-dump2.img" bs=1048576 status=progress
    if cmp -s "$OUT/raw/mmcblk0-full.img" "$OUT/raw/mmcblk0-full-dump2.img"; then
        SAME=identical
    else
        SAME=DIFFERENT
    fi
    {
        echo "Second dump comparison"
        echo "======================"
        echo "dump 1 md5: $(md5sum "$OUT/raw/mmcblk0-full.img" | awk '{print $1}')"
        echo "dump 2 md5: $(md5sum "$OUT/raw/mmcblk0-full-dump2.img" | awk '{print $1}')"
        echo "cmp: $SAME"
        [ "$SAME" = identical ] || echo "WARNING: the two dumps differ; keep dump 1 and investigate"
    } > "$OUT/meta/dump2-comparison.txt"
    note "dump 1 vs dump 2: $SAME"
fi

# ------------------------------------------------------------ remount
note "remounting /data /cache /sdcard"
dsh 'mount -t ext4 /dev/block/mmcblk0p16 /data 2>/dev/null
     mount -t ext4 /dev/block/mmcblk0p16 /sdcard 2>/dev/null
     mount -t ext4 /dev/block/mmcblk0p13 /cache 2>/dev/null; true'

# -------------------------------------------------------------- split
note "splitting the GPT into per-partition images"
python3 "$ROOT_DIR/scripts/split-partitions.py" "$OUT/raw/mmcblk0-full.img" "$OUT/images"

python3 - "$OUT" <<'PY'
import glob, hashlib, os, re, sys
out = sys.argv[1]
dev = {}
for line in open(os.path.join(out, 'meta', 'device-md5.txt')):
    m = re.match(r'([0-9a-f]{32})\s+/dev/block/mmcblk0p(\d+)', line)
    if m:
        dev[int(m.group(2))] = m.group(1)
bad = 0
for n, want in sorted(dev.items()):
    files = glob.glob(os.path.join(out, 'images', f'p{n:02d}-*.img'))
    if not files:
        print(f'  p{n:02d}: image missing'); bad += 1; continue
    h = hashlib.md5(open(files[0], 'rb').read()).hexdigest()
    if h != want:
        print(f'  p{n:02d}: MISMATCH {h} != {want}'); bad += 1
print(f'  {len(dev)} partitions checked, {bad} problem(s)')
sys.exit(1 if bad else 0)
PY

# ---------------------------------------------------------- data files
if command -v debugfs >/dev/null 2>&1; then
    note "extracting /data with debugfs (ownership is not preserved)"
    mkdir -p "$OUT/data-files/root"
    debugfs -R "rdump / $OUT/data-files/root" "$OUT/images/p16-userdata.img" \
        >/dev/null 2>&1 || note "debugfs reported errors (usually ownership); raw image is authoritative"
fi

# ------------------------------------------------------------- manifest
note "writing device-info and SHA256SUMS"
{
    echo "Backup of Echo Show 5 (2nd gen, cronos)"
    echo "date            : $(date -Is)"
    echo "serial          : $(dsh getprop ro.serialno)"
    echo "build           : $(dsh getprop ro.build.version.name)"
    echo "fingerprint     : $(dsh getprop ro.build.fingerprint)"
    echo "recovery        : $(dsh getprop ro.twrp.version)"
    echo "emmc bytes      : $BYTES"
    echo "device md5      : $DEVMD5"
} > "$OUT/meta/device-info.txt"

( cd "$OUT" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS )
note "backup complete: $OUT ($(du -sh "$OUT" | awk '{print $1}'))"
note "verify later with: (cd \"$OUT\" && sha256sum -c SHA256SUMS)"
