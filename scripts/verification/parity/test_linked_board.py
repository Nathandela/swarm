#!/usr/bin/env python3
"""Drive two real owner endpoints through the linked Discussions keyboard board."""

import argparse
import json
import os
import re
from pathlib import Path
import shutil
import tempfile
import termios

from linked_owners import linked_owners, owner
from test_board import DRIVER, p


def witness(root, local, remote, candidate, fake_codex):
    (root / "effects").mkdir(mode=0o700)
    with linked_owners(root, local, remote, fake_codex) as fixture:
        local_env = fixture["local_env"]
        remote_env = fixture["remote_env"]
        manager = fixture["manager"]
        unrelated = fixture["unrelated"]
        local_row = fixture["local"]
        worker = fixture["worker"]
        driver = root / "bin/driver"
        shutil.copyfile(DRIVER, driver)
        binary = root / "bin/board"
        shutil.copyfile(candidate, binary)
        binary.chmod(0o700)
        board = p.Board([str(binary)], root, {**local_env, "TERM": "xterm-256color",
                                              "LANG": "en_US.UTF-8", "LC_ALL": "en_US.UTF-8"})

        def step(keys, landmark):
            offset = len(board.capture)
            board.send(keys)
            board.wait_bytes(landmark, offset)
            return offset

        def select(label):
            for key in (b"\x1b[H", b"\x1b[B", b"\x1b[B", b"\x1b[B"):
                offset = len(board.capture)
                board.send(key)

                def focused():
                    board.drain()
                    raw = board.capture[offset:]
                    if b"\x1b[2J" not in raw:
                        return None
                    frame = raw.rsplit(b"\x1b[2J", 1)[-1]
                    rows = re.split(rb"\x1b\[[0-9]+;1H", frame)
                    plain = [re.sub(rb"\x1b\[[0-?]*[ -/]*[@-~]", b"", line) for line in rows]
                    return next((line for line in plain if line.startswith("▌ ".encode())), None)

                if label in p.wait_for(focused, "fresh selected Discussions row"):
                    return
            raise AssertionError(f"could not select {label!r}")

        def state(env, id):
            listed = owner(env, {"op": "list"})["sessions"] or []
            return next((row["status"]["Process"] for row in listed if row["id"] == id), None)

        restore_config = fixture["place"] / "config.json"
        original = restore_config.read_bytes()

        def install(data):
            temp = restore_config.with_name(".board-fixture-config")
            temp.write_bytes(data)
            temp.chmod(0o600)
            os.replace(temp, restore_config)

        try:
            board.wait_bytes(b"Local row")
            board.wait_bytes(b"DISPATCHER")
            board.wait_bytes(b"Fixture dispatcher")
            assert b"Unrelated remote row" not in board.capture

            # A discovery repaint must leave the existing local row selected.
            step(b"\r", b"Local row  ctrl+q returns to swarm")
            step(b"\x11", b"DISPATCHER")
            socket = Path(remote_env["SWARM_DAEMON_SOCK"])
            absent = socket.with_name("board-offline.sock")
            socket.rename(absent)
            try:
                def detached_link():
                    board.drain()
                    frame = board.capture.rsplit(b"\x1b[2J", 1)[-1]
                    return b"Local row" in frame and b"DISPATCHER" not in frame
                p.wait_for(detached_link, "remote socket loss removes the linked rows")
            finally:
                absent.rename(socket)
            board.wait_bytes(b"DISPATCHER", len(board.capture))
            if worker:
                board.wait_bytes(b"Fixture delegated worker")
                offset = len(board.capture)
                board.resize(120, 24)
                board.wait_bytes(b"from Fixture dispatcher", offset)
                board.wait_bytes(b"spawned 1", offset)
                offset = len(board.capture)
                board.resize(80, 24)
                board.wait_bytes(b"Fixture delegated worker", offset)
                select(b"Fixture delegated worker")
                try:
                    step(b"\r", b"Fixture delegated worker  ctrl+q returns to swarm")
                except RuntimeError as error:
                    raise AssertionError((state(remote_env, worker), bytes(board.capture[-2400:]))) from error
                step(b"\x0e\x11", b"DISPATCHER")
            select(b"Fixture dispatcher")
            opened = step(b"m", b"More")
            board.wait_bytes(b"[Back (esc)]", opened)
            menu = board.capture[opened:]
            assert b"Fixture dispatcher" in menu
            assert b"[Kill (ctrl+x)]" in menu
            assert not any(label in menu for label in
                           (b"[Rename (e)]", b"[Tag (t)]", b"[Handoff (h)]", b"Schedule in this directory"))
            start = step(b"\r", b"Fixture dispatcher  ctrl+q returns to swarm")
            assert b"ctrl+p/ctrl+n switch" not in board.capture[start:]
            step(b"\x11", b"DISPATCHER")

            step(b"\x18", b"kill? y/n")
            wrong = json.loads(original)
            wrong["supervisor"]["state_dir"] = str(root / "wrong-owner")
            install(json.dumps(wrong).encode())
            refused = len(board.capture)
            board.send(b"y")
            def refusal():
                board.drain()
                return any(text in board.capture[refused:] for text in
                           (b"dispatcher supervisor identity changed", b"Target changed; reopen confirmation"))
            p.wait_for(refusal, "linked confirmation rejects the wrong endpoint")
            assert state(remote_env, manager) == state(remote_env, unrelated) == "running"
            assert state(local_env, local_row) == "running"

            install(original)
            step(b"\x1b[H\x18", b"kill? y/n")
            board.send(b"y")
            p.wait_for(lambda: state(remote_env, manager) == "exited", "linked manager killed on its own owner")
            board.wait_bytes(b"[Delete (ctrl+x)]")
            step(b"\x18", b"delete? y/n")
            board.send(b"y")
            p.wait_for(lambda: state(remote_env, manager) is None, "linked manager deleted on its own owner")
            assert state(local_env, local_row) == state(remote_env, unrelated) == "running"
            assert b"Unrelated remote row" not in board.capture
            board.send(b"\x1b[27u")
            def exited():
                board.drain()
                return board.child.poll() is not None
            p.wait_for(exited, "board exit after linked actions")
            assert board.child.returncode == 0
            assert termios.tcgetattr(board.master) == board.original_mode
        finally:
            install(original)
            board.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for flag in ("local", "remote", "candidate", "fake-codex"):
        parser.add_argument(f"--{flag}", required=True, type=p.binary_path)
    parser.add_argument("--scratch", type=Path, default=Path("/tmp"),
                        help="executable scratch filesystem, such as a dedicated Linux tmpfs")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="dp-", dir=args.scratch.resolve(strict=True)) as root:
        witness(Path(root), args.local, args.remote, args.candidate, args.fake_codex)
    print("Linked Discussions selected owner, attach, refusal, kill and delete passed")
