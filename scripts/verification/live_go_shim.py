#!/usr/bin/env python3
"""Keep the real Go shim alive across a Rust supervisor and a Go rollback."""

import contextlib
import fcntl
import hashlib
import json
import os
from pathlib import Path
import pty
import select
import shlex
import socket
import struct
import subprocess
import sys
import tempfile
import termios
import time

from stopped_go_state import await_value, daemon, disk, fixture_env, listed, owner, private, refused

LIVE = "SWARM_DAEMON_LIVE_GO_COMPAT"
NATIVE = "aaaa0000-bbbb-4ccc-8ddd-eeeeeeeeeeee"


@contextlib.contextmanager
def connect(env):
    with socket.socket(socket.AF_UNIX) as client:
        client.settimeout(5)
        client.connect(env["SWARM_DAEMON_SOCK"])
        with client.makefile("rb") as reader:
            def send(tag, data):
                client.sendall(struct.pack(">IB", len(data) + 1, tag) + data)

            def recv():
                size, = struct.unpack(">I", reader.read(4))
                assert 1 <= size <= (1 << 20), size
                record = reader.read(size)
                assert len(record) == size
                return record[0], record[1:]

            def control(op, **fields):
                send(1, json.dumps(dict(op=op, **fields)).encode())

            def reply():
                tag, body = recv()
                assert tag == 1, (tag, body)
                return json.loads(body)

            control("hello", protocol_version=1, capabilities=["attach", "subscribe"])
            hello = reply()
            endpoint = "ep-" + hashlib.sha256(env["SWARM_DAEMON_STATE"].encode()).hexdigest()[:8]
            assert hello["op"] == "hello" and hello["endpoint_id"] == endpoint
            yield send, recv, control, reply, endpoint


def attach(connection, identifier):
    _, recv, control, reply, endpoint = connection
    control("attach", endpoint_id=endpoint, session_id=identifier)
    lease = reply()
    assert lease["op"] == "lease" and lease["session_id"] == identifier, lease
    data = bytearray()
    while len(data) < lease["snapshot_len"]:
        tag, chunk = recv()
        assert tag == 4 and chunk
        data.extend(chunk)
    assert len(data) == lease["snapshot_len"]
    return lease["generation"], json.loads(data)


def read_content(connection, wanted):
    _, recv, _, _, _ = connection
    data = bytearray()
    while wanted not in data:
        code, payload = recv()
        assert code == 2, (code, payload)
        data.extend(payload)


def grid_text(grid):
    return "\n".join("".join(run["text"] for run in line["runs"]) for line in grid["lines"])


def snapshot(env, session):
    with connect(env) as (_, _, send, reply, endpoint):
        send("terminal_subscribe", endpoint_id=endpoint, session_id=session)
        assert reply()["op"] == "ok"
        body = reply()
        assert body["op"] == "terminal_snapshot"
        return body["terminal"]


def board_attach(binary, env, label):
    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 100, 0, 0))
    try:
        child = subprocess.Popen([binary], env=env | {"TERM": "xterm-256color"}, stdin=slave, stdout=slave, stderr=slave)
        try:
            def see(token):
                received = bytearray()
                deadline = time.monotonic() + 8
                while token not in received and time.monotonic() < deadline:
                    if select.select([master], [], [], .2)[0]:
                        received.extend(os.read(master, 8192))
                assert token in received, received[-600:]

            see(label.encode())
            os.write(master, b"\r")
            see(b"ctrl+q returns to swarm")
            os.write(master, b"board-path\r")
            see(b"REPLY=board-path")
            os.write(master, b"\x11")
            see("swarm ·".encode())
            os.write(master, b"\x1b")
            assert child.wait(8) == 0
        finally:
            if child.poll() is None:
                child.kill()
                child.wait(6)
    finally:
        os.close(master)
        os.close(slave)


def rehearse(go, rust, base):
    tools, home, state = (base / name for name in ("bin", "home", "state"))
    for directory in (tools, home, state):
        directory.mkdir(mode=0o700)
    emitted = base / "worker-input"
    agent = tools / "claude"
    hook = json.dumps({"session_id": NATIVE, "last_assistant_message": "fixture finished"})
    agent.write_text(
        '#!/bin/sh\nif [ "$1" = --version ]; then echo "2.1.250 (Claude Code)"; exit; fi\n'
        'stty -echo\n'
        f"printf 'Session {NATIVE}\\nWORKER-READY\\n'\n"
        'while IFS= read -r line; do\n'
        f'  printf "%s\\n" "$line" >> {shlex.quote(str(emitted))}\n'
        '  if [ "$line" = post-hook ]; then\n'
        f"    printf '%s\\n' {shlex.quote(hook)} | {shlex.quote(go)} hook Stop\n"
        '  fi\n'
        '  printf "REPLY=%s\\n" "$line"\n'
        '  if [ "$line" = exit-worker ]; then exit 0; fi\n'
        'done\n'
    )
    agent.chmod(0o700)
    go_env = fixture_env(base, state, tools)
    rust_env = fixture_env(base, state, tools, compat=True) | {LIVE: "1"}
    active = None
    prior = None
    try:
        with daemon(go, go_env, base / "go.log"):
            launch = {"agent":"claude", "cwd":str(home), "name":"Existing Go Claude", "rows":24, "cols":80,
                      "env":[f"HOME={go_env['HOME']}", f"PATH={go_env['PATH']}"]}
            result = owner(go_env, {"op":"launch", "launch":launch})
            assert result["op"] == "launch", result
            active = result["session"]["id"]
            session = state / active.split("/")[1]
            metadata = session / "meta.json"
            await_value(lambda: disk(metadata)["conversation_id"] == NATIVE)
            refused(rust, rust_env, "another instance is already running")
        original = disk(metadata)
        worker = disk(session / "native-process.json")
        assert original["status"]["Process"] == "running"
        fold = session / "hook.fold"
        original_fold = fold.read_bytes() if fold.exists() else None
        prior = metadata.read_bytes()
        private(metadata, original | {"shim_wire_version": 1})
        refused(rust, rust_env, "unsupported live Go shim state or wire version")
        metadata.write_bytes(prior)
        native_path = session / "native-process.json"
        native_before = native_path.read_bytes()
        private(native_path, worker | {"start_time": worker["start_time"] + 1})
        try:
            refused(rust, rust_env, "Go process birth identity changed")
        finally:
            native_path.write_bytes(native_before)
        with daemon(rust, rust_env, base / "rust.log"):
            row, = listed(rust_env)
            assert row["id"] == active and row["status"]["Process"] == "running"
            peek = subprocess.check_output([rust, "peek", active], env=rust_env, text=True)
            assert "WORKER-READY" in peek
            with connect(rust_env) as first:
                _, first_grid = attach(first, active)
                assert "WORKER-READY" in grid_text(first_grid)
                assert snapshot(rust_env, active)["cols"] == 80
                first[0](3, b"first\r")
                read_content(first, b"REPLY=first")
                with connect(rust_env) as second:
                    generation, grid = attach(second, active)
                    assert "REPLY=first" in grid_text(grid)
                    try:
                        first[0](3, b"stale-must-not-arrive\r")
                    except (BrokenPipeError, ConnectionResetError):
                        pass
                    second[2]("resize", endpoint_id=second[4], session_id=active,
                              generation=generation, cols=92, rows=20)
                    await_value(lambda: snapshot(rust_env, active)["cols"] == 92)
                    second[0](3, b"second\r")
                    read_content(second, b"REPLY=second")
                    second[0](3, b"post-hook\r")
                    read_content(second, b"REPLY=post-hook")
                    second[2]("detach", endpoint_id=second[4], session_id=active, generation=generation)
                    assert second[3]()["op"] == "detach"
            board_attach(rust, rust_env, "Existing Go Claude")
            assert emitted.read_bytes() == b"first\nsecond\npost-hook\nboard-path\n", emitted.read_bytes()
            assert metadata.read_bytes() == prior
            assert disk(session / "native-process.json") == worker
            assert (session / "hooks.spool").stat().st_size > 0
            assert (fold.read_bytes() if fold.exists() else None) == original_fold

        with daemon(go, go_env, base / "rollback-go.log"):
            row, = listed(go_env)
            assert row["id"] == active and row["status"]["Process"] == "running"
            assert disk(metadata)["shim_pid"] == original["shim_pid"]
            assert disk(metadata)["shim_start_time"] == original["shim_start_time"]
            assert disk(metadata)["conversation_id"] == NATIVE
            assert disk(session / "native-process.json") == worker
            peek = subprocess.check_output([go, "peek", active], env=go_env, text=True)
            assert "REPLY=board-path" in peek
            await_value(lambda: fold.is_file() and fold.read_bytes() != original_fold)

        prior = metadata.read_bytes()
        with daemon(rust, rust_env, base / "rust-exit.log"):
            with connect(rust_env) as watch:
                watch[2]("subscribe", endpoint_id=watch[4])
                assert watch[3]()["op"] == "ok"
                with connect(rust_env) as control:
                    attach(control, active)
                    control[0](3, b"exit-worker\r")
                    change = watch[3]()
                assert change["op"] == "event" and change["session"]["status"]["Process"] == "exited", change
            assert listed(rust_env)[0]["status"]["Process"] == "exited"
            assert metadata.read_bytes() == prior
        with daemon(rust, rust_env, base / "rust-exit.log"):
            assert listed(rust_env)[0]["status"]["Process"] == "exited"
            assert metadata.read_bytes() == prior
        with daemon(go, go_env, base / "rollback-go-exit.log"):
            row, = listed(go_env)
            assert row["id"] == active and row["status"]["Process"] == "exited", row
            assert disk(metadata)["shim_pid"] == original["shim_pid"]
            assert disk(metadata)["conversation_id"] == NATIVE
            active = None
    finally:
        if active:
            if prior is not None and metadata.read_bytes() != prior:
                metadata.write_bytes(prior)
            with daemon(go, go_env, base / "cleanup-go.log"):
                owner(go_env, {"op":"kill", "session_id":active})
                await_value(lambda: listed(go_env)[0]["status"]["Process"] == "exited")
    print("PASS Go → Rust → Go: same live native process and PTY, Rust board attach, Go hook replay")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit("usage: live_go_shim.py PINNED_GO_BINARY RUST_BINARY")
    go, rust = (str(Path(value).resolve(strict=True)) for value in sys.argv[1:])
    with tempfile.TemporaryDirectory(prefix="lg-", dir=os.path.realpath("/tmp")) as tmp:
        rehearse(go, rust, Path(tmp))
