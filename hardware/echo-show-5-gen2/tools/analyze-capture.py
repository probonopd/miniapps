#!/usr/bin/env python3
"""Per-channel statistics of a raw S24_3LE capture: analyze-capture.py file [channels] [rate]."""
import sys
import numpy as np

path = sys.argv[1]
ch = int(sys.argv[2]) if len(sys.argv) > 2 else 4
rate = int(sys.argv[3]) if len(sys.argv) > 3 else 16000
b = np.frombuffer(open(path, "rb").read(), dtype=np.uint8)
b = b[: len(b) // (3 * ch) * 3 * ch].reshape(-1, ch, 3).astype(np.int32)
x = ((b[:, :, 0] | (b[:, :, 1] << 8) | (b[:, :, 2] << 16)) << 8) >> 8
x = x[rate // 2:].astype(np.float64) / 8388608.0
print(f"frames {len(x)}, seconds {len(x) / rate:.2f}")
for c in range(ch):
    s = x[:, c]
    r = np.sqrt(np.mean((s - s.mean()) ** 2))
    spec = np.abs(np.fft.rfft((s - s.mean()) * np.hanning(len(s))))
    pk = np.argmax(spec[10:]) + 10
    db = 20 * np.log10(r) if r > 0 else float("-inf")
    print(f"ch{c}: rms {db:7.1f} dBFS  peak {20 * np.log10(max(np.abs(s).max(), 1e-9)):7.1f} dBFS"
          f"  dc {s.mean():+.5f}  top {pk * rate / len(s):7.1f} Hz  zero {np.mean(s == 0) * 100:5.1f}%")
for a in range(ch):
    for c in range(a + 1, ch):
        u, v = x[:, a] - x[:, a].mean(), x[:, c] - x[:, c].mean()
        n = np.sqrt((u * u).sum() * (v * v).sum())
        if n == 0:
            print(f"ch{a}-ch{c}: one is silent")
            continue
        m = min(len(u), 32768)
        xc = np.fft.irfft(np.fft.rfft(u[:m], 2 * m) * np.conj(np.fft.rfft(v[:m], 2 * m)))
        lag = int(np.argmax(np.abs(xc)))
        lag = lag - 2 * m if lag > m else lag
        print(f"ch{a}-ch{c}: corr {(u * v).sum() / n:+.4f}  best lag {lag} samples"
              f"  identical {np.mean(x[:, a] == x[:, c]) * 100:.1f}%")
