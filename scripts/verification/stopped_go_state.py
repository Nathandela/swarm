#!/usr/bin/env python3
"""Offline Go → Rust → Go stopped-state rehearsal. Pass pinned Go and Rust binaries."""

import contextlib
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import time

COMPAT = "SWARM_DAEMON_STOPPED_GO_COMPAT"
NATIVE_ID = "00000000-1111-4222-8333-444444444444"
SENTINEL = "fictional-stopped-state-key"


def owner(env, request=None, read_reply=True):
    with socket.socket(socket.AF_UNIX) as client:
        client.settimeout(4)
        client.connect(env["SWARM_DAEMON_SOCK"])

        def write(body):
            frame = b"\1" + json.dumps(body).encode()
            client.sendall(struct.pack(">I", len(frame)) + frame)

        def read():
            with client.makefile("rb") as stream:
                size, = struct.unpack(">I", stream.read(4))
                assert 1 < size <= 1 << 20, size
                frame = stream.read(size)
            assert len(frame) == size and frame[0] == 1
            assert SENTINEL.encode() not in frame
            return json.loads(frame[1:])

        write(dict(op="hello", protocol_version=1, capabilities=["subscribe", "owner-launch", "attach"]))
        hello = read()
        expected = "ep-" + hashlib.sha256(env["SWARM_DAEMON_STATE"].encode()).hexdigest()[:8]
        assert hello["op"] == "hello" and hello["endpoint_id"] == expected, hello
        if request is None:
            return hello
        write(dict(endpoint_id=expected, **request))
        if read_reply:
            return read()


def await_value(test):
    until = time.monotonic() + 15
    while time.monotonic() < until:
        try:
            if value := test():
                return value
        except (ConnectionError, FileNotFoundError):
            pass
        time.sleep(.03)
    raise AssertionError("timed out waiting for fixture state")


def fixture_env(base, state, tools, compat=False):
    env = {
        "HOME": str(base / "home"), "PATH": str(tools) + ":/usr/bin:/bin",
        "XDG_CONFIG_HOME": str(base / "config"), "XDG_STATE_HOME": str(base / "xdg"),
        "SWARM_DAEMON_STATE": str(state), "SWARM_DAEMON_SOCK": str(state / "daemon.sock"),
        "SWARM_DAEMON_LOCK": str(state / "daemon.lock"),
        "SWARM_DAEMON_REMOTE_SOCK": str(state / "remote.sock"),
    }
    if compat:
        env[COMPAT] = "1"
    return env


@contextlib.contextmanager
def daemon(binary, env, log):
    with log.open("ab") as output:
        proc = subprocess.Popen([binary, "daemon"], env=env, stdout=output, stderr=output)
        try:
            def ready():
                if proc.poll() is not None:
                    raise AssertionError(log.read_text())
                return owner(env)

            await_value(ready)
            yield proc
        finally:
            if proc.poll() is None:
                proc.terminate()
                try:
                    proc.wait(6)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait(6)


def listed(env):
    result = owner(env, {"op": "list"})
    assert result["op"] == "list", result
    return result.get("sessions") or []


def disk(path):
    return json.loads(path.read_bytes())


def private(path, value):
    path.write_text(json.dumps(value))
    path.chmod(0o600)


def refused(binary, env, reason):
    done = subprocess.run([binary, "daemon"], env=env, capture_output=True, text=True, timeout=10)
    assert done.returncode == 1 and reason in done.stderr, (done.returncode, done.stderr)


def rehearse(go, rust, base):
    tools = base / "bin"
    tools.mkdir(mode=0o700)
    for path in [base / "home", base / "source"]:
        path.mkdir(mode=0o700)
    agent = tools / "claude"
    agent.write_text(
        '#!/bin/sh\nif [ "$1" = --version ]; then echo "2.1.250 (Claude Code)"; exit; fi\n'
        f"printf 'Session {NATIVE_ID}\\n'\n"
        "exec /bin/sleep 300\n"
    )
    agent.chmod(0o700)

    source = base / "source"
    source_env = fixture_env(base, source, tools)
    with daemon(go, source_env, base / "source.log"):
        reply = owner(source_env, {"op": "launch", "launch": {
            "agent": "claude", "cwd": str(base / "home"), "name": "Go baseline", "cols": 80, "rows": 24,
            "env": [f"PATH={source_env['PATH']}", f"HOME={source_env['HOME']}", f"ANTHROPIC_API_KEY={SENTINEL}"],
        }})
        assert reply["op"] == "launch", reply
        session = reply["session"]["id"]
        local = session.split("/")[1]
        assert re.fullmatch(r"[a-z2-7]{16}", local), local
        meta_source = source / local / "meta.json"
        try:
            await_value(lambda: disk(meta_source)["conversation_id"] == NATIVE_ID)
        finally:
            assert owner(source_env, {"op": "kill", "session_id": session})["op"] == "ok"

        def completed():
            row, = listed(source_env)
            return row if row["status"]["Process"] == "exited" else None

        await_value(completed)
    original = disk(meta_source)
    assert original["conversation_id"] == NATIVE_ID
    assert disk(source / local / "native-writers-stopped.json")["writers_stopped"]

    # A separate canonical path gives a distinct namespace. Capture the Go ID
    # on the copy before cutover, then require that exact identity through rollback.
    copied = base / "copy"
    shutil.copytree(source, copied)
    go_env = fixture_env(base, copied, tools)
    rust_env = fixture_env(base, copied, tools, compat=True)
    with daemon(go, go_env, base / "copy-go.log"):
        baseline, = listed(go_env)
        refused(rust, rust_env, "another instance is already running")
    assert baseline["id"].endswith("/" + local) and baseline["id"] != session
    qualified = copied / local
    meta = qualified / "meta.json"
    fixture_record = disk(meta)
    fixture_record["future_inert_fixture"] = {"opaque": ["label migration treats this as data", 2]}
    private(meta, fixture_record)
    protected = {p.name: p.read_bytes() for p in qualified.iterdir() if p.is_file() and p != meta}
    assert SENTINEL.encode() in protected["shim-launch.json"]
    refused(rust, go_env, "separately qualified migration")
    assert disk(meta) == fixture_record

    corrupt = copied / "broken"
    corrupt.mkdir(mode=0o700)
    corrupt.joinpath("meta.json").touch(mode=0o600)
    wrong = copied / "wrong"
    shutil.copytree(qualified, wrong)
    foreign = copied / "foreign"
    foreign.symlink_to(qualified, target_is_directory=True)

    for change, reason in [
        ({"schema_version": 2}, "requires offline qualification"),
        ({"schema_version": 3}, "requires offline qualification"),
        ({"schema_version": 4}, "requires offline qualification"),
        ({"schema_version": 5}, "requires offline qualification"),
        ({"schema_version": 900}, "requires offline qualification"),
        ({"role": "manager"}, "unqualified Go role"),
        ({"hermes_native": True}, "unqualified Go role"),
        ({"status": {"Process": "running"}}, "live or has uncertain custody"),
        ({"status": {"Process": "lost"}}, "live or has uncertain custody"),
    ]:
        altered = fixture_record | change
        private(meta, altered)
        refused(rust, rust_env, reason)
        assert disk(meta) == altered
    private(meta, fixture_record)
    stopped = qualified / "native-writers-stopped.json"
    stopped.chmod(0o644)
    refused(rust, rust_env, "not a private regular owner file")
    stopped.chmod(0o600)

    with daemon(rust, rust_env, base / "rust.log") as candidate:
        assert owner(rust_env).get("capabilities") == ["subscribe"]
        assert listed(rust_env) == [baseline]
        result = subprocess.run([rust, "ls", "--json"], env=rust_env, capture_output=True, text=True, check=True)
        assert json.loads(result.stdout) == [baseline]
        refused(go, go_env, "another instance is already running")
        assert owner(rust_env, {"op": "launch"})["error_code"] == "op_not_implemented"
        session = baseline["id"]
        assert owner(rust_env, {"op": "rename", "session_id": session, "name": "Renamed by Rust"})["op"] == "ok"
        owner(rust_env, {"op": "set_tag", "session_id": session, "tag": "kept"}, read_reply=False)
        await_value(lambda: disk(meta)["tag"] == "kept")
        candidate.kill()  # The writer committed, but its caller never received an outcome.
        candidate.wait(6)

    revised = disk(meta)
    expected = fixture_record | {"name": "Renamed by Rust", "name_set_at": revised["name_set_at"], "tag": "kept"}
    assert revised == expected and revised["name_set_at"] != fixture_record["name_set_at"]
    with daemon(rust, rust_env, base / "rust.log"):
        ident = baseline["id"]
        assert owner(rust_env, {"op": "set_tag", "session_id": ident, "tag": "kept"})["op"] == "ok"
        assert disk(meta) == revised
        # Swap the original inode with a copied directory bound to the same ID.
        moved = copied / "moved"
        qualified.rename(moved)
        shutil.copytree(moved, qualified)
        try:
            result = owner(rust_env, {"op": "rename", "session_id": ident, "name": "Must refuse"})
            assert result["op"] == "error" and "changed outside" in result["error"], result
            assert disk(meta) == revised and disk(moved / "meta.json") == revised
        finally:
            shutil.rmtree(qualified)
            moved.rename(qualified)

    assert not (copied / "rust-v1").exists()
    assert not (copied / ".rust-state-writer.lock").exists()
    assert protected == {p.name: p.read_bytes() for p in qualified.iterdir() if p.is_file() and p != meta}
    assert disk(meta_source) == original
    with daemon(go, go_env, base / "copy-go.log"):
        assert listed(go_env) == [baseline | {"name": "Renamed by Rust", "tag": "kept"}]
    assert disk(meta) == revised
    print("PASS stopped Claude: Go launch and native capture, copied-state Rust roster/labels, crash/retry, Go rollback")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit("usage: stopped_go_state.py PINNED_GO_BINARY RUST_BINARY")
    go, rust = (str(Path(value).resolve(strict=True)) for value in sys.argv[1:])
    # Short paths keep Darwin's per-session Unix socket path within sockaddr_un.
    with tempfile.TemporaryDirectory(prefix="sg-", dir=os.path.realpath("/tmp")) as scratch:
        rehearse(go, rust, Path(scratch))
