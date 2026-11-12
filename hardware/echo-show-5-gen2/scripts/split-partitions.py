#!/usr/bin/env python3
"""Split a full eMMC image into per-partition images using its GPT.

Usage: split-partitions.py <full-disk.img> <output-dir>

Reads the primary GPT at LBA 1 of the image, verifies both CRCs, then copies
every partition entry plus the preloader area (LBA 0..first-usable-1) and the
backup GPT into <output-dir>.  Read-only with respect to the source image.
"""
import os
import struct
import sys
import uuid
import zlib

SECTOR = 512
CHUNK = 8 * 1024 * 1024


def crc_ok(header: bytes, crc: int) -> bool:
    h = bytearray(header[: struct.unpack_from("<I", header, 12)[0]])
    h[16:20] = b"\0\0\0\0"
    return (zlib.crc32(h) & 0xFFFFFFFF) == crc


def copy_range(src, dst_path, offset, length):
    with open(dst_path, "wb") as out:
        src.seek(offset)
        left = length
        while left:
            buf = src.read(min(CHUNK, left))
            if not buf:
                raise RuntimeError(f"short read at {src.tell()} for {dst_path}")
            out.write(buf)
            left -= len(buf)


def main():
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    img, outdir = sys.argv[1], sys.argv[2]
    os.makedirs(outdir, exist_ok=True)
    size = os.path.getsize(img)

    with open(img, "rb") as f:
        f.seek(SECTOR)
        hdr = f.read(92)
        if hdr[:8] != b"EFI PART":
            sys.exit("no GPT header at LBA 1")
        hsz = struct.unpack_from("<I", hdr, 12)[0]
        hcrc = struct.unpack_from("<I", hdr, 16)[0]
        if not crc_ok(hdr, hcrc):
            sys.exit("GPT header CRC mismatch (image damaged?)")
        backup_lba, first_usable, last_usable = struct.unpack_from("<QQQ", hdr, 32)
        entries_lba = struct.unpack_from("<Q", hdr, 72)[0]
        nentries, entsz = struct.unpack_from("<II", hdr, 80)
        ecrc = struct.unpack_from("<I", hdr, 88)[0]

        f.seek(entries_lba * SECTOR)
        ents = f.read(nentries * entsz)
        if (zlib.crc32(ents) & 0xFFFFFFFF) != ecrc:
            sys.exit("GPT entry-array CRC mismatch (image damaged?)")

        print(f"image: {img} ({size} bytes, {size // SECTOR} sectors)")
        print(f"backup GPT LBA {backup_lba}, usable {first_usable}..{last_usable}")

        # Region before the first partition (protective MBR, GPT, preloader area)
        pre_len = first_usable * SECTOR
        if pre_len:
            copy_range(f, os.path.join(outdir, "p00-preloader-gpt-0-1MiB.img"), 0, pre_len)
            print(f"p00 preloader/gpt area  {pre_len:>12d} bytes")

        for i in range(nentries):
            e = ents[i * entsz:(i + 1) * entsz]
            if e[:16] == b"\0" * 16:
                continue
            start, last = struct.unpack_from("<QQ", e, 32)
            length = last - start + 1
            name = e[56:entsz].decode("utf-16-le").rstrip("\0")
            if not name:
                name = "unnamed"
            safe = "".join(c if c.isalnum() or c in "-_" else "_" for c in name)
            path = os.path.join(outdir, f"p{i + 1:02d}-{safe}.img")
            copy_range(f, path, start * SECTOR, length * SECTOR)
            print(f"p{i + 1:02d} {name:<10s} {length * SECTOR:>12d} bytes -> {os.path.basename(path)}")

        # Backup GPT at the end of the disk (entry array + header, 33 sectors)
        if backup_lba:
            tail_start = max(0, backup_lba - 32) * SECTOR
            copy_range(f, os.path.join(outdir, "p99-gpt-backup.img"), tail_start, size - tail_start)
            print(f"p99 backup GPT          {size - tail_start:>12d} bytes")


if __name__ == "__main__":
    main()
