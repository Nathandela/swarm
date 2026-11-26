#!/usr/bin/env python3
"""Offline Go → Rust → Go stopped-state rehearsal. Pass pinned Go and Rust binaries."""

import contextlib
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
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
RAW_MARKER = "$serde_json::private::RawValue"


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
    fixture_record["future_inert_fixture"] = {
        "opaque": ["label migration treats this as data", 2,
                   {"$serde_json::private::Number": "47"}, {RAW_MARKER: "null"}],
    }
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
        ({"role": {RAW_MARKER: "null"}}, "unqualified Go role"),
        ({"shim_pid": {RAW_MARKER: str(fixture_record["shim_pid"])}}, "missing Go shim birth identity"),
        ({"hermes_native": True}, "unqualified Go role"),
        ({"status": {"Process": "running"}}, "live or has uncertain custody"),
        ({"status": {"Process": "lost"}}, "live or has uncertain custody"),
    ]:
        altered = fixture_record | change
        private(meta, altered)
        refused(rust, rust_env, reason)
        assert disk(meta) == altered
    private(meta, fixture_record)
    for filename, key, encoded, reason in [
        ("exit.json", "exit_code", str(fixture_record["exit_code"]), "exit evidence disagrees"),
        ("shim-launch.json", "input_embargo", '""', "launch binding or native writer is unqualified"),
    ]:
        sidefile = qualified / filename
        original_sidefile = sidefile.read_bytes()
        altered_sidefile = disk(sidefile) | {key: {RAW_MARKER: encoded}}
        private(sidefile, altered_sidefile)
        refused(rust, rust_env, reason)
        assert disk(sidefile) == altered_sidefile
        sidefile.write_bytes(original_sidefile)
    stopped = qualified / "native-writers-stopped.json"
    stopped.chmod(0o644)
    refused(rust, rust_env, "not a private regular owner file")
    stopped.chmod(0o600)

    with daemon(rust, rust_env, base / "rust.log") as candidate:
        assert owner(rust_env).get("capabilities") == ["subscribe", "attach"]
        assert listed(rust_env) == [baseline]
        result = subprocess.run([rust, "ls", "--json"], env=rust_env, capture_output=True, text=True, check=True)
        assert json.loads(result.stdout) == [baseline]
        refused(go, go_env, "another instance is already running")
        assert owner(rust_env, {"op": "delete", "session_id": baseline["id"]})["error_code"] == "op_not_implemented"
        assert owner(rust_env, {"op": "launch", "launch": {"agent": "opencode"}})["error_code"] == "invalid_field"
        session = baseline["id"]
        spoofed = owner(rust_env, {"op": "rename", "session_id": session, "name": {RAW_MARKER: '"spoofed"'}})
        assert spoofed["op"] == "error" and "invalid session label type" in spoofed["error"]
        assert disk(meta) == fixture_record
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


    new_attempt(go, rust, base, tools, copied, go_env, rust_env)


def new_attempt(go, rust, base, tools, copied, go_env, rust_env):
    native_id = "11111111-2222-4333-8444-555555555555"
    agent = tools / "claude"
    argv_log = base / "agent-argv"
    hook = json.dumps({"session_id": native_id, "cwd": str(base / "home")})
    agent.write_text(
        '#!/bin/sh\nif [ "$1" = --version ]; then echo "2.1.250 (Claude Code)"; exit; fi\n'
        f'printf "%s\\n" "$@" > {shlex.quote(str(argv_log))}\n'
        'printf "Rust fixture worker\\n"\n'
        'if [ -n "$SWARM_RUST_HOOK_SOCKET" ]; then\n'
        f"  printf '%s\\n' {shlex.quote(hook)} | {shlex.quote(rust)} hook UserPromptSubmit\n"
        "fi\nexec /bin/sleep 300\n"
    )
    launch = {"agent": "claude", "cwd": str(base / "home"), "name": "Rust launch",
              "cols": 80, "rows": 24, "env": [f"PATH={rust_env['PATH']}", f"HOME={rust_env['HOME']}",
                                                   f"ANTHROPIC_API_KEY={SENTINEL}"]}
    with daemon(rust, rust_env, base / "rust-mixed.log"):
        reply = owner(rust_env, {"op": "launch", "launch": launch})
        assert reply["op"] == "launch", reply
        session = reply["session"]["id"]
        local = session.split("/")[1]
        source = copied / "rust-v1" / local
        target = copied / local

        def capture():
            record = disk(source / "observation.json")
            return record if record["conversation_id"] == native_id else None

        try:
            await_value(capture)
            view = subprocess.run([rust, "peek", session], env=rust_env, capture_output=True, text=True, check=True)
            assert "Rust fixture worker" in view.stdout
            assert not target.exists()
        except BaseException:
            owner(rust_env, {"op": "kill", "session_id": session})
            raise

    def export() -> subprocess.CompletedProcess:
        return subprocess.run([rust, "daemon", "export-stopped-go"], env=rust_env, capture_output=True,
                              text=True, timeout=10)

    try:
        blocked = export()
        assert blocked.returncode == 1 and "live or uncertain" in blocked.stderr, blocked.stderr
        assert not target.exists()
        with daemon(rust, rust_env, base / "rust-mixed.log"):
            rows = {row["id"]: row for row in listed(rust_env)}
            assert len(rows) == 2 and rows[session]["status"]["Process"] == "running", rows
            assert owner(rust_env, {"op":"rename", "session_id":session, "name":"Rust after cutover"})["op"] == "ok"
            assert owner(rust_env, {"op":"set_tag", "session_id":session, "tag":"after-cutover"})["op"] == "ok"
            done = subprocess.run([rust, "kill", session], env=rust_env, capture_output=True, text=True)
            assert done.returncode == 0, done.stderr
            await_value(lambda: (source / "go-stop-proof.json").is_file())
            await_value(lambda: next(row for row in listed(rust_env) if row["id"] == session)["status"]["Process"] == "exited")
    except BaseException:
        with daemon(rust, rust_env, base / "rust-mixed.log"):
            owner(rust_env, {"op": "kill", "session_id":session})
        raise

    target.mkdir(mode=0o700)
    foreign_entry = target / "foreign-data"
    foreign_entry.write_text("independent fixture data")
    try:
        collision = export()
        assert collision.returncode == 1 and "already occupied" in collision.stderr, collision.stderr
        assert foreign_entry.read_text() == "independent fixture data"
    finally:
        foreign_entry.unlink()
        target.rmdir()
    await_value(lambda: export().returncode == 0)
    copied_meta = target / "meta.json"
    before = copied_meta.read_bytes()
    published = source / "go-rollback-published.json"
    published.unlink()  # Recreate the durable state between directory commit and receipt.
    assert export().returncode == 0 and copied_meta.read_bytes() == before and published.is_file()
    assert export().returncode == 0 and copied_meta.read_bytes() == before
    record = disk(copied_meta)
    rust_token = disk(source / "session.json")["token"].encode()
    assert rust_token not in before and rust_token not in (target / "rust-origin.json").read_bytes()
    assert f"ANTHROPIC_API_KEY={SENTINEL}" in record["env"]
    assert copied_meta.stat().st_mode & 0o777 == 0o600 and target.stat().st_mode & 0o777 == 0o700
    assert record["conversation_id"] == native_id and record["name"] == "Rust after cutover"
    assert record["tag"] == "after-cutover" and record["status"]["Process"] == "exited"
    assert not (target / "shim-launch.json").exists()
    with daemon(go, go_env, base / "copy-go.log"):
        rows = {row["id"]: row for row in listed(go_env)}
        assert rows[session]["name"] == "Rust after cutover" and len(rows) == 2
        assert owner(go_env, {"op":"rename", "session_id":session, "name":"Renamed after rollback"})["op"] == "ok"

    with daemon(rust, rust_env, base / "rust-mixed.log"):
        rows = {row["id"]: row for row in listed(rust_env)}
        assert rows[session]["name"] == "Renamed after rollback" and len(rows) == 2

    with daemon(go, go_env, base / "copy-go.log"):
        reply = owner(go_env, {"op":"launch", "launch": launch | {"options":{"resume_from":session}}})
        assert reply["op"] == "launch", reply
        resumed = reply["session"]["id"]
        try:
            argv_log_text = await_value(lambda: "--resume\n" + native_id in argv_log.read_text())
            assert argv_log_text
            resumed_meta = copied / resumed.split("/")[1] / "meta.json"
            assert disk(resumed_meta)["conversation_id"] == native_id
        finally:
            assert owner(go_env, {"op":"kill", "session_id":resumed})["op"] == "ok"
        await_value(lambda: next(row for row in listed(go_env) if row["id"] == resumed)["status"]["Process"] == "exited")
    print("PASS classic Rust start: preserved live Rust restart, stopped export, Go rollback, native Go resume")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit("usage: stopped_go_state.py PINNED_GO_BINARY RUST_BINARY")
    go, rust = (str(Path(value).resolve(strict=True)) for value in sys.argv[1:])
    # Short paths keep Darwin's per-session Unix socket path within sockaddr_un.
    with tempfile.TemporaryDirectory(prefix="sg-", dir=os.path.realpath("/tmp")) as scratch:
        rehearse(go, rust, Path(scratch))
