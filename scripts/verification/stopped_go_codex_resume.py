#!/usr/bin/env python3
"""Send three real Codex turns through a stopped Go → Rust → Go round trip."""

import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time

import macos_codex_fixture as fixture
from live_go_shim import attach, connect, grid_text, snapshot
from parity_codex import model_fixture
from stopped_go_state import await_value, daemon, disk, fixture_env, listed, owner, refused


def exercise(go, rust, root):
    tools, state, home, work = (root / name for name in ("bin", "state", "home", "work"))
    for directory in (state, home, home / ".codex", work):
        directory.mkdir(mode=0o700)
    prompts = ["Original Go owner turn", "Rust resumed owner turn", "Restored Go owner turn"]
    replies = ["Original Go response", "Resumed Rust response", "Restored Go response"]
    delivered = []

    def answer(items):
        last_user = next((item for item in reversed(items) if item.get("role") == "user"), {})
        for part in last_user.get("content", []):
            prompt = part.get("text")
            if prompt in prompts:
                delivered.append(prompt)
                return replies[prompts.index(prompt)]
        return None

    model, _ = model_fixture(root, False, answer_for_input=answer)
    go_env = fixture_env(root, state, tools) | {"TERM":"xterm-256color"}
    rust_env = fixture_env(root, state, tools, compat=True) | {"TERM":"xterm-256color"}
    guard = None
    try:
        vendor = tools / "codex"
        if sys.platform == "darwin":
            guard = fixture.Guard(root, state, model.server_port, True)
            go, rust = guard.wrap(go, "go"), guard.wrap(rust, "rust")
            vendor = guard.wrap(vendor, "version-probe")
            go_env["SWARM_FIXTURE_SESSION_ID"] = fixture.GO_ID
            rust_env["SWARM_FIXTURE_SESSION_ID"] = fixture.RUST_ID
        assert subprocess.check_output([vendor, "--version"], env=go_env).strip() == b"codex-cli 0.172.0"
        launch = {"agent":"codex", "cwd":str(work), "name":"Stopped Go Codex", "cols":100, "rows":30,
                  "env":[f"{key}={go_env[key]}" for key in ("HOME", "PATH", "TERM")],
                  "options":{"sandbox":"workspace-write"}}

        def open_session(env, settings):
            result = owner(env, {"op":"launch", "launch":settings}, timeout=85)
            assert result["op"] == "launch", result
            return result["session"]["id"]

        def turn(env, identifier, index):
            await_value(lambda: "Ask Codex" in "\n".join(snapshot(env, identifier)["lines"]))
            with connect(env) as connection:
                _, grid = attach(connection, identifier)
                assert "OpenAI Codex" in grid_text(grid)
                send, *_ = connection
                send(3, prompts[index].encode())
                time.sleep(.3)
                send(3, b"\r")
                await_value(lambda: prompts[index] in delivered)
                await_value(lambda: replies[index] in "\n".join(snapshot(env, identifier)["lines"]))

        def kill(env, identifier):
            assert owner(env, {"op":"kill", "session_id":identifier})["op"] == "ok"
            await_value(lambda: any(row["id"] == identifier and row["status"]["Process"] == "exited"
                                    for row in listed(env)))

        with daemon(go, go_env, root / "go.log"):
            source = open_session(go_env, launch)
            original = state / source.split("/")[-1]
            try:
                turn(go_env, source, 0)
                thread = await_value(lambda: disk(original / "meta.json")["conversation_id"])
            finally:
                kill(go_env, source)
        original_meta = disk(original / "meta.json")
        assert original_meta["auth_identity"] and original_meta["launch_options"] == launch["options"]
        [rollout] = list((home / ".codex/sessions").rglob(f"*-{thread}.jsonl"))
        rollout_identity = rollout.stat().st_ino
        source_files = {entry.name: entry.read_bytes() for entry in original.iterdir() if entry.is_file()}

        # A dead vendor alias is outside stopped compatibility until its exact
        # fixture-owned listener and lock have been retired.
        endpoint = original / "codex.sock"
        namespace = Path("/tmp").resolve() / f"codex-daemon-{os.geteuid()}"
        alias = namespace / hashlib.sha256(os.fsencode(endpoint)).hexdigest()
        if endpoint.is_symlink():
            expected = endpoint.readlink()
            try:
                endpoint.unlink()
                endpoint.symlink_to(expected.with_name("f" * 64))
                refused(rust, rust_env, "Go Codex stopped socket alias is unproved")
            finally:
                endpoint.unlink()
                endpoint.symlink_to(expected)
        if alias.is_socket():
            refused(rust, rust_env, "Go Codex short listener custody remains")
            fixture.retire_listener(alias, alias.with_suffix(".lock"))
            print("joined source retained a stale vendor listener; retired exact fixture alias", flush=True)

        settings = launch | {"options":{"resume_from":source},
                             "env":[f"HOME={home}", "PATH=/usr/bin:/bin"]}
        auth = home / ".codex/auth.json"
        saved_auth = auth.read_bytes()
        with daemon(rust, rust_env, root / "rust.log"):
            def reject(changes, detail):
                reply = owner(rust_env, {"op":"launch", "launch": settings | changes})
                assert reply["op"] == "error" and detail in json.dumps(reply), reply

            reject({"cwd":str(home)}, "original checkout")
            reject({"options":{"resume_from":"foreign/" + original.name}}, "ordinary local source")
            try:
                auth.write_text(json.dumps({"auth_mode":"apikey","OPENAI_API_KEY":"other-fixture-account"}))
                reject({}, "saved account")
            finally:
                auth.write_bytes(saved_auth)
            assert {row["id"] for row in listed(rust_env)} == {source}
            resumed = open_session(rust_env, settings)
            local = resumed.split("/")[-1]
            candidate = state / "rust-v1" / local
            try:
                assert disk(candidate / "observation.json")["conversation_id"] == thread
                assert disk(candidate / "session.json")["codex_resume"]["resumed_from"] == source
                assert disk(candidate / "codex-profile.json")["profile"]["options"] == launch["options"]
                reject({}, "live or unconfirmed writer")
                turn(rust_env, resumed, 1)
            finally:
                kill(rust_env, resumed)
                await_value(lambda: (candidate / "go-stop-proof.json").is_file())
        assert {entry.name: entry.read_bytes() for entry in original.iterdir() if entry.is_file()} == source_files
        export = [rust, "daemon", "export-stopped-go"]
        subprocess.run(export, env=rust_env, check=True, timeout=12)
        exported = disk(state / local / "meta.json")
        for key in ("conversation_id", "auth_identity", "cli_identity", "launch_options", "env"):
            assert exported[key] == original_meta[key], key

        if guard:
            go_env["SWARM_FIXTURE_SESSION_ID"] = fixture.RESUMED_GO_ID
        with daemon(go, go_env, root / "go-rollback.log"):
            restored = open_session(go_env, launch | {"options":{"resume_from":resumed}})
            try:
                turn(go_env, restored, 2)
                assert disk(state / restored.split("/")[-1] / "meta.json")["conversation_id"] == thread
            finally:
                kill(go_env, restored)
        assert delivered == prompts, delivered
        assert rollout.stat().st_ino == rollout_identity
        events = [json.loads(line) for line in rollout.read_text().splitlines()]
        sandbox_modes = [event["payload"]["sandbox_policy"]["type"]
                         for event in events if event["type"] == "turn_context"]
        assert sandbox_modes == ["workspace-write"] * len(prompts), sandbox_modes
        assert all(prompt in rollout.read_text() for prompt in prompts)
        print("PASS stopped Go → Rust → Go: three native turns on the original rollout")
    finally:
        model.shutdown()
        model.server_close()
        if guard:
            guard.close()


if __name__ == "__main__":
    if len(sys.argv) != 4:
        sys.exit("usage: stopped_go_codex_resume.py PINNED_GO RUST SCRATCH")
    os.umask(0o077)
    exercise(*(Path(value).resolve(strict=True) for value in sys.argv[1:]))
