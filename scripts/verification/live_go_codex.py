#!/usr/bin/env python3
"""Keep an actual managed Codex TUI and app-server through Go → Rust → Go."""

import json
import os
from pathlib import Path
import socket
import struct
import subprocess
import sys
import time

from live_go_shim import LIVE, attach, connect, grid_text, snapshot
from parity_codex import model_fixture
from stopped_go_state import await_value, daemon, disk, fixture_env, listed, owner, refused


def rehearse(go, rust, root):
    root = root.resolve()
    tools, home, state, work = (root / name for name in ("bin", "home", "state", "work"))
    assert (tools / "codex").is_file(), "stage the qualified native executable before running"
    for directory in (home, state, work, home / ".codex"):
        directory.mkdir(mode=0o700)
    assert subprocess.check_output([tools / "codex", "--version"], env={"HOME":str(home)}).strip() == b"codex-cli 0.151.0"
    prompts = {
        "Go owner warmup": "The original Go turn succeeded.",
        "Rust owner continued the thread": "The Rust bridge turn succeeded.",
        "Restored Go owner": "The restored Go turn succeeded.",
    }
    observed_prompts = []

    def answer_for_input(items):
        last_user = next((item for item in reversed(items) if item.get("role") == "user"), {})
        for content in last_user.get("content", []):
            text = content.get("text")
            if text in prompts:
                observed_prompts.append(text)
                return prompts[text]
        return None

    model, _ = model_fixture(root, False, answer_for_input=answer_for_input)
    go_env = fixture_env(root, state, tools) | {"TERM":"xterm-256color"}
    rust_env = fixture_env(root, state, tools, compat=True) | {LIVE:"1", "TERM":"xterm-256color"}
    active = None

    def until(test, message):
        try:
            return await_value(test)
        except AssertionError:
            raise AssertionError(message) from None

    def turn(env, prompt):
        with connect(env) as connection:
            _, grid = attach(connection, active)
            assert "OpenAI Codex" in grid_text(grid)
            send, _, _, _, _ = connection
            send(3, prompt.encode())
            time.sleep(.3)
            send(3, b"\r")
            until(lambda: prompt in observed_prompts, f"native request: {prompt}")
            until(lambda: prompts[prompt] in "\n".join(snapshot(env, active)["lines"]), f"native reply: {prompt}")

    def identities():
        m = disk(session / "meta.json")
        n = disk(session / "native-process.json")
        b = disk(session / "backend.json")
        observed = []
        for pid, birth in [(m["shim_pid"],m["shim_start_time"]), (n["pid"],n["start_time"]),
                           (n["backend_pid"],n["backend_start_time"])]:
            stat = (Path("/proc") / str(pid) / "stat").read_text()
            assert int(stat.rsplit(")", 1)[1].split()[19]) == birth
            observed.append((pid,birth))
        endpoint = Path(b["socket_path"])
        with socket.socket(socket.AF_UNIX) as probe:
            probe.connect(str(endpoint))
            peer, uid, _ = struct.unpack("3i",probe.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
            assert peer == b["pid"] == n["backend_pid"] and uid == os.geteuid()
        return observed, endpoint.stat().st_ino

    try:
        with daemon(go, go_env, root / "go.log"):
            launch = {"agent":"codex", "cwd":str(work), "name":"Go native Codex", "rows":30, "cols":100,
                      "env":[f"{key}={value}" for key,value in go_env.items() if key in ("HOME","PATH","TERM")]}
            result = owner(go_env, {"op":"launch", "launch":launch}, timeout=55)
            assert result["op"] == "launch", result
            active = result["session"]["id"]
            session = state / active.split("/")[1]
            until(lambda: "fixture" in "\n".join(snapshot(go_env, active)["lines"]).lower(), "native prompt")
            turn(go_env, "Go owner warmup")
            thread = until(lambda: disk(session / "meta.json").get("conversation_id"), "Go bound conversation")
            identity = identities()
            print("initial Go owner recorded thread and three native process identities", flush=True)
        metadata = session / "meta.json"
        saved = metadata.read_bytes()
        forged = json.loads(saved)
        forged["conversation_id"] = "00000000-1111-4222-8333-444444444444"
        try:
            metadata.write_text(json.dumps(forged))
            refused(rust, rust_env, "Go Codex loaded conversation disagrees")
            assert identities() == identity, (identities(), identity)
            assert observed_prompts == ["Go owner warmup"], observed_prompts
        finally:
            metadata.write_bytes(saved)

        with daemon(rust, rust_env, root / "rust.log"):
            assert listed(rust_env)[0]["id"] == active
            assert identities() == identity
            assert disk(session / "meta.json")["conversation_id"] == thread
            turn(rust_env, "Rust owner continued the thread")
            print("Rust owner interacted with the same native thread and PTY", flush=True)
        with daemon(go, go_env, root / "go-rollback.log"):
            assert listed(go_env)[0]["id"] == active
            assert identities() == identity
            assert disk(session / "meta.json")["conversation_id"] == thread
            turn(go_env, "Restored Go owner")
            transcripts = list((home / ".codex/sessions").rglob(f"*-{thread}.jsonl"))
            assert len(transcripts) == 1
            until(lambda: "Restored Go owner" in transcripts[0].read_text(), "restored thread's newest prompt")
            contents = transcripts[0].read_text()
            assert all(text in contents for text in prompts)
            assert observed_prompts == list(prompts), observed_prompts
        print("same session, native thread, shim, TUI and app-server across Go → Rust → Go")
    finally:
        if active:
            with daemon(go, go_env, root / "cleanup.log"):
                result = subprocess.run([go, "kill", active], env=go_env, capture_output=True, timeout=10)
                assert result.returncode == 0, result.stderr
                until(lambda: (session / "exit.json").is_file(), "stopped live fixture")
        model.shutdown()
        model.server_close()


if __name__ == "__main__":
    assert sys.platform == "linux", "run in an isolated Linux process and network namespace"
    os.umask(0o077)
    go, rust, root = (Path(value).resolve() for value in sys.argv[1:])
    rehearse(go, rust, root)
