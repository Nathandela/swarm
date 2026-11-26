#!/usr/bin/env python3
"""Exercise the classic, Discord-disabled Hermes PTY through Go, Rust and Go."""

import argparse
from contextlib import closing
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import shlex
import sqlite3
import subprocess
import threading
import time

from live_go_shim import LIVE, attach, connect, snapshot
from native_fixture_sandbox import check_guards, guarded
from stopped_go_state import COMPAT, daemon, disk, fixture_env, listed, owner, refused


class Model(BaseHTTPRequestHandler):
    def log_message(self, *unused):
        pass

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        messages = request["messages"]
        marker = next((m["content"] for m in reversed(messages) if m["role"] == "user"), "")
        if marker in PROMPTS:
            self.server.turns.append((marker, messages))
        message = {"role": "assistant", "content": "ACK " + str(marker)}
        if request.get("stream"):
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            for delta, reason in [(message, None), ({}, "stop")]:
                frame = {"id": "local", "object": "chat.completion.chunk", "created": 1,
                         "model": "swarm-fixture", "choices": [
                             {"index": 0, "delta": delta, "finish_reason": reason}]}
                self.wfile.write(("data: " + json.dumps(frame) + "\n\n").encode())
            self.wfile.write(b"data: [DONE]\n\n")
        else:
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"id": "local", "object": "chat.completion",
                "created": 1, "model": "swarm-fixture", "choices": [
                    {"index": 0, "message": message, "finish_reason": "stop"}],
                "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}}).encode())


PROMPTS = ("SWARM_GO_HERMES_FIRST", "SWARM_RUST_HERMES_CONTINUED", "SWARM_GO_HERMES_RESTORED")


def wait_for(check, label):
    end = time.monotonic() + 30
    while time.monotonic() < end:
        if result := check():
            return result
        time.sleep(.1)
    raise AssertionError(f"timed out waiting for {label}")


def rehearse(go, rust, hermes, root):
    home, work, tools, state, hermes_home = (root / n for n in ("home", "work", "bin", "state", "hermes"))
    for path in (home, work, tools, state, hermes_home):
        path.mkdir(mode=0o700)
    model = ThreadingHTTPServer(("127.0.0.1", 0), Model)
    model.daemon_threads, model.turns = True, []
    threading.Thread(target=model.serve_forever, daemon=True).start()
    port = model.server_port
    check_guards(root, [port])
    url = f"http://127.0.0.1:{port}/v1"
    (hermes_home / "config.yaml").write_text(json.dumps({
        "model": {"provider": "custom", "default": "swarm-fixture", "base_url": url, "context_length": 131072},
        "auxiliary": {"title_generation": {"enabled": False}},
        "telemetry": {"shared_metrics": {"enabled": False, "send": False, "offer_version": 2}},
    }))
    go_env = fixture_env(root, state, tools) | {
        "TERM": "xterm-256color", "LANG": "C.UTF-8", "TMPDIR": str(root), "SHELL": "/bin/sh",
        "HERMES_HOME": str(hermes_home), "OPENAI_API_KEY": "synthetic-local", "OPENAI_BASE_URL": url,
        "PYTHONUNBUFFERED": "1",
    }
    rust_env = go_env | {COMPAT: "1", LIVE: "1"}

    def wrapper(target, name, pid_file=None):
        path = tools / name
        command = guarded([str(target)], root, [port]) if name != "hermes" else [str(target)]
        prefix = ("printf '%s\\n' \"$$\" > " + shlex.quote(str(pid_file)) + "\n") if pid_file else ""
        if pid_file:
            prefix += "".join("export " + key + "=" + shlex.quote(go_env[key]) + "\n"
                              for key in ("HERMES_HOME", "OPENAI_BASE_URL", "TMPDIR", "PYTHONUNBUFFERED"))
        path.write_text("#!/bin/sh\n" + prefix + "exec " + shlex.join(command) + ' "$@"\n')
        path.chmod(0o700)
        return path

    assert b"Hermes Agent v0.20.6 " in subprocess.check_output(
        guarded([str(hermes), "--version"], root, [port]), env=go_env, cwd=work, timeout=20)
    pid_file = root / "worker"
    wrapper(hermes, "hermes", pid_file)
    go, rust = wrapper(go, "go"), wrapper(rust, "rust")
    active = None

    def send(env, text, completed):
        with connect(env) as connection:
            attach(connection, active)
            connection[0](3, text.encode() + b"\r")
            return completed()

    def turns():
        if not database.exists():
            return []
        with closing(sqlite3.connect(database.as_uri() + "?mode=ro", uri=True)) as read:
            try:
                return read.execute(
                    "SELECT session_id, role, content FROM messages WHERE role IN ('user','assistant') ORDER BY id"
                ).fetchall()
            except sqlite3.OperationalError as err:
                if "no such table" not in str(err):
                    raise
                return []

    def response(marker):
        def matching():
            first = [session for session, role, content in turns() if role == "assistant" and content == "ACK " + marker]
            return first[0] if len(first) == 1 else None
        return wait_for(matching, marker)

    def identity():
        shim = disk(session / "meta.json")["shim_pid"]
        pid = int(pid_file.read_text())
        result = subprocess.check_output(["/bin/ps", "-p", f"{shim},{pid}",
                                         "-o", "pid=,ppid=,pgid=,lstart="], text=True)
        entries = {int(row[0]): row[1:] for row in map(str.split, result.splitlines())}
        assert set(entries) == {shim, pid} and entries[pid][:2] == [str(shim), str(pid)], entries
        sockets = [(p.stat().st_dev, p.stat().st_ino) for p in
                   [session / "shim.sock", session / "hook.sock", database]]
        return shim, pid, entries[shim][1:], entries[pid], sockets

    database = hermes_home / "state.db"
    try:
        with daemon(go, go_env, root / "go.log"):
            request = {"agent": "hermes", "cwd": str(work), "name": "Go classic Hermes",
                       "rows": 40, "cols": 120, "env": [f"{key}={value}" for key, value in go_env.items()]}
            launched = owner(go_env, {"op": "launch", "launch": request}, timeout=30)
            assert launched["op"] == "launch", launched
            active = launched["session"]["id"]
            session = state / active.split("/")[-1]
            wait_for(lambda: any("❯" in line for line in snapshot(go_env, active)["lines"]), "classic Hermes prompt")
            thread = send(go_env, PROMPTS[0], lambda: response(PROMPTS[0]))
            baseline = identity()
            assert disk(session / "meta.json")["conversation_id"] in ("", thread)

        launch = session / "shim-launch.json"
        original = launch.read_bytes()
        original_meta = (session / "meta.json").read_bytes()
        forged = session / "hermes-input.json"
        assert not forged.exists()
        forged.write_text("{}")
        try:
            refused(rust, rust_env, "Go shim or backend custody remains")
            assert identity() == baseline
        finally:
            forged.unlink()

        with daemon(rust, rust_env, root / "rust.log"):
            assert listed(rust_env)[0]["id"] == active and identity() == baseline
            assert send(rust_env, PROMPTS[1], lambda: response(PROMPTS[1])) == thread
            assert identity() == baseline and launch.read_bytes() == original
            assert (session / "meta.json").read_bytes() == original_meta

        with daemon(go, go_env, root / "go-rollback.log"):
            assert listed(go_env)[0]["id"] == active and identity() == baseline
            assert send(go_env, PROMPTS[2], lambda: response(PROMPTS[2])) == thread
            assert identity() == baseline and launch.read_bytes() == original
            assert [(session_id, role, content) for session_id, role, content in turns() if content in
                    {*PROMPTS, *("ACK " + p for p in PROMPTS)}] == [
                        row for p in PROMPTS for row in [(thread, "user", p), (thread, "assistant", "ACK " + p)]]
            assert [marker for marker, _ in model.turns] == list(PROMPTS)
            for index, (_, messages) in enumerate(model.turns):
                assert [m["content"] for m in messages if m["role"] == "user"][-index-1:] == list(PROMPTS[:index+1])
            send(go_env, "/exit", lambda: wait_for(
                lambda: (session / "exit.json").exists(), "classic Hermes graceful exit"))
            wait_for(lambda: disk(session / "meta.json")["status"]["Process"] == "exited"
                     and disk(session / "meta.json")["conversation_id"] == thread, "Go-captured terminal identity")
        assert not (hermes_home / "gateway.sock").exists()
        with daemon(rust, rust_env, root / "rust-stopped.log"):
            assert listed(rust_env)[0]["status"]["Process"] == "exited"
        print("Pinned Go → Rust → pinned Go retained the Hermes classic PTY and completed three turns in one native conversation.")
    finally:
        if active and not (session / "exit.json").exists():
            with daemon(go, go_env, root / "cleanup.log"):
                assert owner(go_env, {"op": "kill", "session_id": active})["op"] == "ok"
                wait_for(lambda: (session / "exit.json").exists(), "fixture cleanup")
        model.shutdown()
        model.server_close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("go", "rust", "hermes", "root"):
        parser.add_argument("--" + name, required=True, type=Path)
    args = parser.parse_args()
    os.umask(0o077)
    rehearse(*(p.resolve(strict=True) for p in (args.go, args.rust, args.hermes, args.root)))
