#!/usr/bin/env python3
"""Offline frame-budget reference. Optional inputs are synthetic raw 24 kHz mono PCM16LE."""
import argparse
import array
import statistics
import sys
from pathlib import Path


def detect(samples, offset):
    signal = [0] * offset + list(samples)
    stops = [None, None, None]
    packets = active_ms = 0
    for start in range(0, len(signal), 480):
        hot = [abs(s) > 420 for s in signal[start:start + 480]]
        if sum(hot) < 24:
            packets = active_ms = 0
            continue
        packets += 1
        active_ms += sum(any(hot[i:i + 24]) for i in range(0, len(hot), 24))
        for j, condition in enumerate((True, packets >= 3, active_ms > 21)):
            if condition and stops[j] is None:
                stops[j] = start // 24
        if all(stop is not None for stop in stops):
            break
    return stops


def summarize(label, rows):
    for i, name in enumerate(("single", "three-packet", "occupied-ms")):
        delays = [row[i] - row[0] for row in rows if row[i] is not None]
        print(label, name, "detections", len(delays), "/", len(rows),
              "added_ms_median_max", (statistics.median(delays), max(delays)) if delays else None)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("pcm", type=Path, nargs="*")
    args = p.parse_args()
    for ms in (20, 40, 80, 240):
        rows = [detect([1000] * (ms * 24), offset) for offset in range(480)]
        summarize(str(ms) + "ms", rows)
        assert all(row[2] is None if ms == 20 else row[2] is not None and row[2] - row[0] <= 20 for row in rows)
    for path in args.pcm:
        audio = array.array("h")
        audio.frombytes(path.read_bytes())
        if sys.byteorder != "little":
            audio.byteswap()
        summarize(path.stem, [detect(audio, offset) for offset in range(0, 480, 24)])


if __name__ == "__main__":
    main()
