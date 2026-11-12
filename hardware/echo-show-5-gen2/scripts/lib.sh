#!/bin/bash
# Common helpers for the Echo Show 5 (2nd gen, cronos) scripts.
# Source this file; do not execute it.
# shellcheck shell=bash

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

die()  { echo "ERROR: $*" >&2; exit 1; }
note() { echo "==> $*"; }

if [ -n "${ANDROID_SERIAL:-}" ]; then
    SERIAL=$ANDROID_SERIAL
else
    command -v adb >/dev/null 2>&1 || die "adb not found in PATH"
    SERIAL=$(adb get-serialno 2>/dev/null | tr -d '\r') || die "no adb device"
    case "$SERIAL" in
        ""|unknown) die "no adb device (or adb not authorized)";;
    esac
fi

adbs() { adb -s "$SERIAL" "$@" </dev/null; }
dsh()  { adb -s "$SERIAL" shell "$@" </dev/null | tr -d '\r'; }

require_device() {
    local dev
    dev=$(dsh getprop ro.product.device)
    [ "$dev" = cronos ] || die "expected ro.product.device=cronos, got '$dev'"
}

require_root() {
    local id
    id=$(dsh id)
    case "$id" in
        *"uid=0(root)"*) ;;
        *) die "adb shell is not root: $id";;
    esac
}

require_booted() {
    local b
    b=$(dsh getprop sys.boot_completed)
    [ "$b" = 1 ] || die "device is not booted into Android (sys.boot_completed=$b)"
}

require_recovery() {
    local state
    state=$(adbs get-state 2>/dev/null || true)
    [ "$state" = recovery ] || die "device is not in recovery/TWRP (state=$state)"
}

confirm() {
    if [ "${ASSUME_YES:-0}" = 1 ]; then return 0; fi
    local ans
    read -r -p "$1 Type YES to continue: " ans
    [ "$ans" = YES ] || { echo "aborted"; exit 1; }
}

remount_rw() { dsh 'mount -o rw,remount /system' >/dev/null 2>&1 || true; }
remount_ro() { dsh 'mount -o ro,remount /system' >/dev/null 2>&1 || true; }

# Only for hosts that allow it (SUDO_ASKPASS is configured on this machine).
sudo_ask() { SUDO_ASKPASS=${SUDO_ASKPASS:-/System/Library/Tools/SudoAskPass} sudo -A -E "$@"; }
