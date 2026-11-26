#!/usr/bin/env python3
"""Render real Go and Rust boards in matching XTerms on a private Linux Xvfb display."""
import argparse
import ctypes as c
import ctypes.util
import json
import os
from pathlib import Path
import shutil
import struct
import subprocess
import sys
import tempfile
import termios
import time
import tty
import zlib

import test_board

p = test_board.p
ROOT_WIDTH, ROOT_HEIGHT = 1552, 470
LEFT, RIGHT, TOP = 8, 780, 8
FONT = "DejaVu Sans Mono"
GEOMETRY = "80x24"


def render_window(root, member):
    board = p.Board([*p.guard(root), str(root / "bin" / member)], root, p.environment(root))
    sent = 0
    attributes = termios.tcgetattr(1)
    tty.setraw(1)

    def pump():
        nonlocal sent
        board.drain()
        while sent < len(board.capture):
            sent += os.write(1, board.capture[sent:])

    try:
        board.wait_bytes(b"More (m)")
        pump()
        board.send(b"m")
        board.wait_bytes(b"[Down (")
        pump()
        (root / "effects" / f"ready-{member}").touch()
        deadline = time.monotonic() + 15
        while not (root / "effects" / "stop").exists() and time.monotonic() < deadline:
            pump()
            time.sleep(0.05)
        board.send(b"\x1b[27u\x1b[27u")
        p.wait_for(lambda: board.child.poll() is not None, "terminal board close")
    finally:
        try:
            board.close()
        finally:
            termios.tcsetattr(1, termios.TCSANOW, attributes)


class XImage(c.Structure):
    _fields_ = [(name, c.c_int) for name in ("width", "height", "xoffset", "format")] + [
        ("data", c.c_void_p),
    ] + [(name, c.c_int) for name in (
        "byte_order", "bitmap_unit", "bitmap_bit_order", "bitmap_pad",
        "depth", "bytes_per_line", "bits_per_pixel",
    )] + [(name, c.c_ulong) for name in ("red_mask", "green_mask", "blue_mask")]


def screenshot(destination):
    x = c.CDLL(ctypes.util.find_library("X11"))
    declarations = {
        "XOpenDisplay": ([c.c_char_p], c.c_void_p),
        "XDefaultRootWindow": ([c.c_void_p], c.c_ulong),
        "XGetImage": ([c.c_void_p, c.c_ulong, c.c_int, c.c_int, c.c_uint, c.c_uint,
                       c.c_ulong, c.c_int], c.POINTER(XImage)),
        "XDestroyImage": ([c.POINTER(XImage)], c.c_int),
        "XCloseDisplay": ([c.c_void_p], c.c_int),
    }
    for name, (args, result) in declarations.items():
        fn = getattr(x, name)
        fn.argtypes, fn.restype = args, result
    display = x.XOpenDisplay(None)
    assert display, "use a dedicated xvfb-run display"
    try:
        image = x.XGetImage(display, x.XDefaultRootWindow(display), 0, 0, ROOT_WIDTH,
                            ROOT_HEIGHT, -1, 2)
        assert image, "Xvfb screen is smaller than the capture"
        try:
            frame = image.contents
            assert (frame.width, frame.height, frame.byte_order, frame.bits_per_pixel) == (
                ROOT_WIDTH, ROOT_HEIGHT, 0, 32)
            assert (frame.red_mask, frame.green_mask, frame.blue_mask) == (0xff0000, 0xff00, 0xff)
            data = c.string_at(frame.data, frame.bytes_per_line * frame.height)
            scanlines = bytearray()
            for row in range(frame.height):
                first = row * frame.bytes_per_line
                pixels = data[first:first + 4 * frame.width]
                rgb = bytearray(3 * frame.width)
                for channel, offset in enumerate((2, 1, 0)):
                    rgb[channel::3] = pixels[offset::4]
                scanlines.extend(b"\x00")
                scanlines.extend(rgb)
        finally:
            x.XDestroyImage(image)
    finally:
        x.XCloseDisplay(display)

    def chunk(kind, value):
        payload = kind + value
        return struct.pack(">I", len(value)) + payload + struct.pack(">I", zlib.crc32(payload))

    with destination.open("xb") as file:
        file.write(b"\x89PNG\r\n\x1a\n")
        file.write(chunk(b"IHDR", struct.pack(">IIBBBBB", ROOT_WIDTH, ROOT_HEIGHT, 8, 2, 0, 0, 0)))
        file.write(chunk(b"IDAT", zlib.compress(scanlines, 9)))
        file.write(chunk(b"IEND", b""))
    stride = ROOT_WIDTH * 3 + 1
    pane_width = RIGHT - LEFT
    differences = 0
    for y in range(TOP, ROOT_HEIGHT):
        start = y * stride + 1
        left = scanlines[start + LEFT * 3:start + RIGHT * 3]
        right = scanlines[start + RIGHT * 3:start + (RIGHT + pane_width) * 3]
        differences += sum(left[i:i+3] != right[i:i+3] for i in range(0, pane_width * 3, 3))
    return differences


def launch(go, rust, out):
    assert sys.platform == "linux" and os.environ.get("DISPLAY", "").startswith(":")
    authority = Path(os.environ["XAUTHORITY"])
    assert authority.is_file() and authority.parent.name.startswith("xvfb-run.")
    root = Path(tempfile.mkdtemp(prefix="px-", dir="/tmp"))
    children = []
    known = {}
    driver = (os.getpid(), p.generation(os.getpid()))
    try:
        for name in ("bin", "tmp", "home", "work", "runtime", "effects", "state"):
            (root / name).mkdir(mode=0o700)
        for source, name in ((go, "swarm"), (go, "reference-board"), (rust, "board"),
                             (test_board.DRIVER, "driver"), (p.FIXTURE, "claude")):
            target = root / "bin" / name
            shutil.copyfile(source, target)
            target.chmod(0o700)

        env = p.environment(root)
        sandbox = p.guard(root)
        daemon = subprocess.Popen([*sandbox, root / "bin/swarm", "daemon"], cwd=root / "work",
                                  env=env, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                                  stderr=subprocess.DEVNULL)
        children.append(daemon)

        def cli(*args):
            return subprocess.run([*sandbox, root / "bin/swarm", *args],
                                  cwd=root / "work", env=env, capture_output=True, timeout=5)

        def rows():
            p.owned(root, driver, known)
            got = cli("ls", "--json")
            return (json.loads(got.stdout),) if got.returncode == 0 else None

        p.wait_for(rows, "throwaway XTerm fixture owner")
        (root / "effects/board-events").touch()
        spawned = cli("spawn", "--cli", "claude", "--name", "amber", "--dir", root / "work",
                      "--prompt", "Synthetic isolated visual fixture")
        assert spawned.returncode == 0, spawned.stderr
        identity = spawned.stdout.decode().strip()
        assert cli("send", identity, "--text", "fixture permission").returncode == 0
        p.wait_for(lambda: any(row["id"] == identity and row["group"] == "needs_input"
                              for row in (rows() or ([],))[0]), "fixture approval")
        xenv = {**env, "DISPLAY":os.environ["DISPLAY"], "XAUTHORITY":os.environ["XAUTHORITY"]}
        for member, offset in (("reference-board", LEFT), ("board", RIGHT)):
            child = subprocess.Popen(
                ["/usr/bin/xterm", "-u8", "-fa", FONT, "-fs", "11", "-fg", "#e4e4ee",
                 "-bg", "#141520", "-geometry", f"{GEOMETRY}+{offset}+{TOP}", "+sb",
                 "-e", sys.executable, Path(__file__).resolve(), "_window", root, member],
                env=xenv, cwd=root / "work",
            )
            children.append(child)
        p.wait_for(lambda: (p.owned(root, driver, known),
                           all((root / "effects" / f"ready-{item}").exists()
                               for item in ("reference-board", "board")))[1],
                   "both real XTerm More menus")
        time.sleep(.25)
        differences = screenshot(out)
        assert cli("kill", identity).returncode == 0
        (root / "effects/stop").touch()
        for child in children[1:]:
            assert child.wait(timeout=5) == 0
        daemon.terminate()
        daemon.wait(timeout=5)
        print(json.dumps({"geometry":GEOMETRY, "font":FONT, "fixture_id":identity,
                          "go_sha256":p.hash_file(go), "rust_sha256":p.hash_file(rust),
                          "png_sha256":p.hash_file(out), "different_pixels":differences}, indent=2))
    finally:
        (root / "effects/stop").touch(exist_ok=True)
        for child in reversed(children):
            if child.poll() is None:
                child.terminate()
                try:
                    child.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait(timeout=3)
        known.pop(os.getpid(), None)
        cleanup = p.reap(root, None, known)
        if cleanup["remaining"]:
            raise RuntimeError(f"fixture survivors in {root}: {cleanup}")
        shutil.rmtree(root)


if __name__ == "__main__":
    if sys.argv[1:2] == ["_window"]:
        render_window(Path(sys.argv[2]), sys.argv[3])
    elif sys.argv[1:2] == ["_display"]:
        go, rust, out = map(Path, sys.argv[2:])
        launch(go, rust, out)
    else:
        parser = argparse.ArgumentParser(description=__doc__)
        parser.add_argument("--reference", required=True, type=p.binary_path)
        parser.add_argument("--candidate", required=True, type=p.binary_path)
        parser.add_argument("--out", required=True, type=Path)
        opts = parser.parse_args()
        # The public command always creates and authenticates a separate display.
        subprocess.run(
            ["/usr/bin/xvfb-run", "-a", "-s", f"-screen 0 {ROOT_WIDTH}x{ROOT_HEIGHT}x24 -nolisten tcp",
             sys.executable, Path(__file__).resolve(), "_display", opts.reference,
             opts.candidate, opts.out.resolve()],
            check=True, timeout=35,
        )
