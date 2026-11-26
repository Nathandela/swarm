#!/usr/bin/env python3
"""Compare dashboard build skew in isolated PTYs and verify live-worker restart custody."""

import argparse
import base64
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import termios
import time

from test_board import DRIVER, compare_projection, owner_request, p, resolve_cells, TMUX_MODES


def run_inside(root, mode, tmux=None):
    env = p.environment(root)
    work = root / "work"
    owner = root / "bin/swarm"

    def cli(*args):
        return subprocess.run([owner, *args], cwd=work, env=env, capture_output=True, timeout=5)

    def hello():
        try:
            return owner_request(env["SWARM_DAEMON_SOCK"], "owner-daemon-restart", None)
        except (OSError, AssertionError):
            return None

    daemon_log = root / "owner-console.log"
    with daemon_log.open("wb") as log:
        daemon = subprocess.Popen([owner, "daemon"], cwd=work, env=env,
                                  stdin=subprocess.DEVNULL, stdout=log, stderr=log)
    try:
        state = p.wait_for(hello, "verified fixture owner hello")
        receipt = {"mode": mode, "initial_version": state["build_version"], "first_divergence": None}
        if mode == "notice":
            command = [tmux, "-S", root / "tmux.sock", "-f", "/dev/null"]

            def run(*args):
                result = subprocess.run([*command, *args], cwd=work, env=env, capture_output=True, timeout=5)
                assert result.returncode == 0, (args, result.stderr)
                return result.stdout

            run("new-session", "-d", "-s", "anchor", "-c", work, "/bin/sleep", "25")
            captures = []
            for cols, rows in ((80, 24), (40, 8), (24, 4)):
                views = []
                for source in ("reference-board", "board"):
                    run("new-session", "-d", "-s", "view", "-x", str(cols), "-y", str(rows),
                        "-c", work, root / "bin" / source)

                    def paint():
                        raw = run("capture-pane", "-pe", "-S", "0", "-E", str(rows - 1), "-t", "view:0.0")
                        if b"More (m)" not in raw or b"daemon " not in raw:
                            return None
                        pattern = "|".join(f"#{{{name}}}" for name in TMUX_MODES)
                        modes = run("display-message", "-p", "-t", "view:0.0", pattern)
                        return {
                            "ansi_base64": base64.b64encode(raw).decode(),
                            "cells": resolve_cells(raw, cols, rows),
                            "terminal_modes": dict(zip(TMUX_MODES, modes.decode().strip().split("|"), strict=True)),
                        }

                    views.append(p.wait_for(paint, f"{source} passive {cols}x{rows} notice"))
                    run("kill-session", "-t", "view")
                captures.append({"cols": cols, "rows": rows, "reference": views[0], "candidate": views[1]})
                first = compare_projection(*views)
                if first is not None and receipt["first_divergence"] is None:
                    receipt["first_divergence"] = {"cols": cols, "rows": rows, **first}
            receipt["frames"] = captures
            assert hello()["build_version"] == state["build_version"] and daemon.poll() is None
            run("kill-session", "-t", "anchor")
        else:
            spawned = cli("spawn", "--cli", "claude", "--name", "restart-fixture", "--dir", work,
                          "--prompt", "Offline restart fixture")
            assert spawned.returncode == 0, spawned.stderr
            session = spawned.stdout.decode().strip()
            pid_path = root / "effects/worker"
            pid = p.wait_for(lambda: int(pid_path.read_text()) if pid_path.exists() else None, "worker birth")
            birth = p.generation(pid)
            assert birth is not None
            board_env = env
            if mode == "foreign":
                other = root / "different-owner"
                other.mkdir(mode=0o700)
                board_env = {**env, "SWARM_DAEMON_STATE": str(other)}
            board = p.Board([str(root / "bin/board")], root, board_env)
            try:
                board.wait_bytes(b"upgrading daemon")
                if mode in ("upgrade", "quit"):
                    if mode == "quit":
                        board.send(b"\x03")
                        p.wait_for(lambda: (board.drain(), board.child.poll())[1] is not None, "quit without abandoning restart")
                        assert board.child.returncode == 0 and termios.tcgetattr(board.master) == board.original_mode
                    else:
                        board.wait_bytes(b"daemon upgraded")
                    assert daemon.poll() == 0
                    replacement = hello()
                    assert replacement["build_version"] != state["build_version"]
                    assert replacement["endpoint_id"] == state["endpoint_id"]
                    receipt["replacement_version"] = replacement["build_version"]
                else:
                    board.wait_bytes(b"daemon upgrade failed")
                    assert hello() == state and daemon.poll() is None
                assert p.generation(pid) == birth and int(pid_path.read_text()) == pid
                if mode == "quit":
                    board.close()
                    board = p.Board([str(root / "bin/board")], root, env)
                board.wait_bytes(b"restart-fixture")
                board.send(b"\r")
                board.wait_bytes(b"fixture ready")
                board.send(b"alive-after-skew\r")
                board.wait_bytes(b"fixture received alive-after-skew")
                offset = len(board.capture)
                board.send(b"\x11")
                board.wait_bytes(b"More (m)", offset)
                board.send(b"\x1b[27u")
                p.wait_for(lambda: (board.drain(), board.child.poll())[1] is not None, "exit after restart")
                assert board.child.returncode == 0 and termios.tcgetattr(board.master) == board.original_mode
                receipt["same_worker"] = True
                receipt["capture_base64"] = base64.b64encode(board.capture).decode()
            finally:
                board.close()
            assert cli("kill", session).returncode == 0
    except Exception:
        print(daemon_log.read_text(errors="replace"), file=sys.stderr)
        raise
    finally:
        if daemon.poll() is None:
            daemon.terminate()
        daemon.wait(timeout=5)
    p.write_json(root / "result.json", receipt)


def main(args):
    root = Path(tempfile.mkdtemp(prefix="ver-", dir=args.scratch.resolve(strict=True)))
    previous = {}
    driver = None
    child = None
    try:
        for name in ("bin", "tmp", "home", "work", "runtime", "effects", "state"):
            (root / name).mkdir(mode=0o700)
        for source, name in ((args.supervisor, "swarm"), (args.board, "board"),
                             (args.reference, "reference-board"), (DRIVER, "driver"), (DRIVER, "swarm-parity"),
                             (p.FIXTURE, "claude"), (Path(__file__), "test_version_board.py"),
                             (Path(__file__).with_name("test_board.py"), "test_board.py")):
            if source is not None:
                target = root / "bin" / name
                shutil.copyfile(source, target)
                target.chmod(0o700)
        command = [*p.guard(root), sys.executable, root / "bin/test_version_board.py", "_inside", root, args.mode]
        if args.tmux is not None:
            command.append(args.tmux)
        child = subprocess.Popen(command, cwd=root / "work", env=p.environment(root), start_new_session=True)
        driver = (child.pid, p.generation(child.pid))
        deadline = time.monotonic() + 40
        while child.poll() is None:
            p.owned(root, driver, previous)
            if time.monotonic() >= deadline:
                raise RuntimeError("version fixture timed out")
            time.sleep(.05)
        assert child.returncode == 0, child.returncode
        receipt = json.loads((root / "result.json").read_bytes())
        receipt["board_sha256"] = p.hash_file(args.board)
        receipt["owner_sha256"] = p.hash_file(args.supervisor)
        if args.reference:
            receipt["reference_sha256"] = p.hash_file(args.reference)
            receipt["tmux"] = subprocess.check_output([args.tmux, "-V"], text=True).strip()
        p.write_json(args.evidence, receipt, replace=False)
        assert receipt["first_divergence"] is None, receipt["first_divergence"]
    finally:
        cleanup = p.reap(root, driver, previous)
        assert not cleanup["remaining"], cleanup
        if child is not None:
            child.wait(timeout=2)
        shutil.rmtree(root)
    print(f"{args.mode} passed, cleanup {cleanup}")


if __name__ == "__main__":
    if sys.argv[1:2] == ["_inside"]:
        run_inside(Path(sys.argv[2]), sys.argv[3], Path(sys.argv[4]) if len(sys.argv) == 5 else None)
    else:
        parser = argparse.ArgumentParser(description=__doc__)
        parser.add_argument("--mode", required=True, choices=("notice", "upgrade", "refuse", "foreign", "quit"))
        parser.add_argument("--supervisor", required=True, type=p.binary_path)
        parser.add_argument("--board", required=True, type=p.binary_path)
        parser.add_argument("--reference", type=p.binary_path)
        parser.add_argument("--tmux", type=p.binary_path)
        parser.add_argument("--scratch", type=Path, default=Path("/tmp"))
        parser.add_argument("--evidence", required=True, type=Path)
        args = parser.parse_args()
        if args.mode == "notice" and not (args.reference and args.tmux):
            parser.error("--mode notice requires --reference and --tmux")
        main(args)
