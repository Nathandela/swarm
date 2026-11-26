#!/usr/bin/env python3
"""Run inside a real terminal to capture its native mode report for --touch-back."""
import argparse
import os
from pathlib import Path
import select
import termios
import time
import tty


def main(output):
    state = termios.tcgetattr(0)
    try:
        tty.setraw(0)
        # Begin with the attach chrome's mouse override, then restore the
        # underlying worker's normal SGR mouse mode before asking about it.
        os.write(1, b"\x1b[?1002h\x1b[?1006h"
                    b"\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l"
                    b"\x1b[?1000h\x1b[?1006h\x1b[?1000$p")
        received = bytearray()
        deadline = time.monotonic() + 3
        while not received.endswith(b"$y"):
            timeout = max(0, deadline - time.monotonic())
            if not select.select([0], [], [], timeout)[0] or len(received) >= 64:
                raise RuntimeError(f"terminal did not report its mouse mode: {received!r}")
            received.extend(os.read(0, 64))
        if received != b"\x1b[?1000;1$y":
            raise RuntimeError(f"unexpected terminal mode report: {received!r}")
        with output.open("xb") as stream:
            stream.write(received)
    finally:
        os.write(1, b"\x1b[?1000l\x1b[?1002l\x1b[?1006l")
        termios.tcsetattr(0, termios.TCSANOW, state)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path)
    main(parser.parse_args().output)
