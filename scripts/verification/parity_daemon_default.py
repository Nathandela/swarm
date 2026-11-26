#!/usr/bin/env python3
"""Verify unconfigured daemon refusal and actual dashboard auto-start under HOME and XDG."""

import argparse
from importlib.machinery import SourceFileLoader
import importlib.util
import json
from pathlib import Path
import shutil
import subprocess
import tempfile

driver = Path(__file__).resolve().parent / "parity/swarm-parity"
spec = importlib.util.spec_from_loader(
    "parity", SourceFileLoader("parity", str(driver))
)
p = importlib.util.module_from_spec(spec)
spec.loader.exec_module(p)


def observe(binary, base, use_xdg):
    for path in ("home", "work", "bin"):
        (base / path).mkdir(mode=0o700, parents=True)
    home = base / "home"
    copy = base / "bin/swarm"
    shutil.copy2(binary, copy)
    shutil.copy2(driver, base / "bin/driver")
    env = {
        "HOME": str(home),
        "PATH": "/usr/bin:/bin",
        "LANG": "C",
        "TERM": "xterm-256color",
    }
    if use_xdg:
        env["XDG_STATE_HOME"] = str(home / "state")
        root = home / "state/swarm/sessions"
    else:
        root = home / ".local/state/swarm/sessions"
    bare = subprocess.run([copy, "daemon"], env=env, capture_output=True, timeout=4)
    assert (bare.returncode, bare.stderr) == (
        1,
        b"daemon: not implemented\n",
    ) and not root.exists()
    socket = root / "daemon.sock"
    board = p.Board([copy], base, env)
    previous = {}
    try:
        try:
            board.wait_bytes(b"swarm")
        except RuntimeError as error:
            raise AssertionError(
                (
                    str(binary),
                    use_xdg,
                    board.child.poll(),
                    socket.is_socket(),
                    bytes(board.capture[-1200:]),
                )
            ) from error
        p.wait_for(socket.is_socket, "default owner socket")
        result = subprocess.run(
            [copy, "ls", "--json"], env=env, capture_output=True, timeout=4
        )
        assert result.returncode == 0 and json.loads(result.stdout) in (None, [])
        original = socket.stat()
        other = subprocess.run(
            [copy, "daemon"],
            env=env | {"SWARM_DAEMON_STATE": str(root)},
            capture_output=True,
            timeout=4,
        )
        assert other.returncode != 0 and socket.stat().st_ino == original.st_ino
        assert (root / "daemon.lock").is_file()
        p.owned(base, None, previous)
        board.send(b"\x1b[27u")
        p.wait_for(
            lambda: (board.drain(), board.child.poll())[1] is not None,
            "quit dashboard with Escape",
        )
        assert board.child.returncode == 0 and socket.is_socket()
    finally:
        try:
            p.reap(base, None, previous)
        finally:
            board.close()
        assert not p.owned(base, None, previous)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("reference", type=Path)
    parser.add_argument("candidate", type=Path)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(
        prefix="ds-", dir=Path("/tmp").resolve()
    ) as scratch:
        for i, binary in enumerate((args.reference, args.candidate)):
            for xdg in (False, True):
                observe(binary.resolve(), Path(scratch) / f"{i}{int(xdg)}", xdg)
    print(
        "Go and Rust refused bare daemons, auto-started via the real board under HOME/XDG, and retained singleton custody after board quit"
    )
