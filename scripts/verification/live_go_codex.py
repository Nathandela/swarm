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

if sys.platform == "darwin":
    import macos_codex_fixture as macos


def rehearse(go, rust, root, version, cold=False):
    root = root.resolve()
    if sys.platform == "darwin":
        assert root.parent == Path("/tmp").resolve() and root.stat().st_mode & 0o777 == 0o700
    tools, home, state, work = (root / name for name in ("bin", "home", "state", "work"))
    assert (tools / "codex").is_file(), "stage the qualified native executable before running"
    for directory in (home, state, work, home / ".codex"):
        directory.mkdir(mode=0o700)
    prompts = {
        "Go owner warmup": "The original Go turn succeeded.",
        "Rust owner continued the thread": "The Rust bridge turn succeeded.",
        "Restored Go owner": "The restored Go turn succeeded.",
        "New Rust owner conversation": "The Rust-created conversation succeeded.",
        "Go cold resume of the Rust conversation": "The Go cold resume succeeded.",
        "Rust operation ledger fixture": "The Rust operation was accepted.",
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
    guard = None
    vendor = tools / "codex"
    if sys.platform == "darwin":
        assert version == "0.172.0", "the Darwin fixture qualifies the signed alias runtime"
        guard = macos.Guard(root, state, model.server_port, cold)
        go, rust = guard.wrap(go, "go"), guard.wrap(rust, "rust")
        vendor = guard.wrap(vendor, "version-probe")
        go_env["SWARM_FIXTURE_SESSION_ID"] = macos.GO_ID
        rust_env["SWARM_FIXTURE_SESSION_ID"] = macos.RUST_ID
    assert subprocess.check_output([vendor, "--version"], env=go_env).strip() == f"codex-cli {version}".encode()
    active = None
    rust_held = None
    go_held = None

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

    def process_birth(pid):
        if guard:
            return macos.process_birth(pid)
        stat = (Path("/proc") / str(pid) / "stat").read_text()
        return int(stat.rsplit(")", 1)[1].split()[19])

    def has_stopped(pid, birth):
        try:
            return process_birth(pid) != birth
        except FileNotFoundError:
            return True

    def socket_peer(endpoint):
        with socket.socket(socket.AF_UNIX) as probe:
            probe.connect(str(endpoint))
            if guard:
                pid, uid = macos.socket_peer(probe)
            else:
                pid, uid, _ = struct.unpack("3i", probe.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
            assert uid == os.geteuid()
            return pid

    def identities():
        m = disk(session / "meta.json")
        n = disk(session / "native-process.json")
        b = disk(session / "backend.json")
        observed = []
        for pid, birth in [(m["shim_pid"],m["shim_start_time"]), (n["pid"],n["start_time"]),
                           (n["backend_pid"],n["backend_start_time"])]:
            assert process_birth(pid) == birth
            observed.append((pid,birth))
        endpoint = Path(b["socket_path"])
        assert socket_peer(endpoint) == b["pid"] == n["backend_pid"]
        return observed, endpoint.stat().st_ino

    try:
        with daemon(go, go_env, root / "go.log"):
            launch = {"agent":"codex", "cwd":str(work), "name":"Go native Codex", "rows":30, "cols":100,
                      "env":[f"{key}={value}" for key,value in go_env.items() if key in ("HOME","PATH","TERM")]}
            result = owner(go_env, {"op":"launch", "launch":launch}, timeout=55)
            assert result["op"] == "launch", result
            active = result["session"]["id"]
            go_held = active
            session = state / active.split("/")[1]
            until(lambda: "fixture" in "\n".join(snapshot(go_env, active)["lines"]).lower(), "native prompt")
            turn(go_env, "Go owner warmup")
            thread = until(lambda: disk(session / "meta.json").get("conversation_id"), "Go bound conversation")
            identity = identities()
            print("initial Go owner recorded thread and three native process identities", flush=True)
        if version == "0.172.0":
            alias = session / "codex.sock"
            target = alias.readlink()
            alias.unlink()
            try:
                alias.symlink_to(target.with_name("f" * 64))
                refused(rust, rust_env, "unproven Go Codex backend socket")
                assert observed_prompts == ["Go owner warmup"], observed_prompts
            finally:
                alias.unlink()
                alias.symlink_to(target)
            assert identities() == identity

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
            assert all(text in contents for text in list(prompts)[:3])
            assert observed_prompts == list(prompts)[:3], observed_prompts
        print("same session, native thread, shim, TUI and app-server across Go → Rust → Go")
        if cold:
            with daemon(rust, rust_env, root / "rust-create.log"):
                created = owner(rust_env, {"op":"launch", "launch":launch | {"name":"Rust cold Codex"}}, timeout=55)
                assert created["op"] == "launch", created
                rust_held = active = created["session"]["id"]
                local = active.split("/")[-1]
                candidate_state = state / "rust-v1" / local
                until(lambda: "Ask Codex" in "\n".join(snapshot(rust_env, active)["lines"]), "Rust managed TUI")
                turn(rust_env, "New Rust owner conversation")
                native_thread = until(lambda: disk(candidate_state / "observation.json").get("conversation_id"),
                                      "Rust-bound native conversation")
                assert native_thread != thread
                assert {row["id"] for row in listed(rust_env)} == {go_held, active}
                assert identities() == identity
                backend_pid = socket_peer(candidate_state / "b")
                backend_birth = process_birth(backend_pid)
                refused_operation = owner(rust_env, {"op":"semantic_send","session_id":active,
                    "composer_send":{"text":"Rust operation ledger fixture"}}, caps=("semantic-send",))
                assert refused_operation["error_code"] == "op_not_implemented", refused_operation
                assert "Rust operation ledger fixture" not in observed_prompts
            export = [rust, "daemon", "export-stopped-go"]
            refused_while_live = subprocess.run(export, env=rust_env, capture_output=True, timeout=12)
            assert refused_while_live.returncode and b"live or uncertain Rust shim" in refused_while_live.stderr, refused_while_live
            assert identities() == identity and not (state / local).exists()
            with daemon(rust, rust_env, root / "rust-drain.log"):
                assert owner(rust_env, {"op":"kill","session_id":active})["op"] == "ok"
                until(lambda: (candidate_state / "go-stop-proof.json").is_file(), "Rust stop proof")
                assert has_stopped(backend_pid, backend_birth), "original Rust app-server survived stop"
            rust_held = None
            active = None

            hermes = tools / "hermes"
            hermes.write_text('#!/bin/sh\n[ "$*" = "chat --cli" ] || exit 2\nexec /bin/sleep 120\n')
            hermes.chmod(0o700)
            with daemon(go, go_env, root / "go-hermes.log"):
                created_hermes = owner(go_env, {"op":"launch", "launch":launch | {
                    "agent":"hermes", "name":"Retained Go Hermes terminal fixture",
                }}, timeout=30)
                assert created_hermes["op"] == "launch", created_hermes
                hermes_id = created_hermes["session"]["id"]
                hermes_state = state / hermes_id.split("/")[-1]
                hermes_meta = hermes_state / "meta.json"
                assert owner(go_env, {"op":"kill","session_id":hermes_id})["op"] == "ok"
                until(lambda: disk(hermes_meta)["status"]["Process"] == "exited", "stopped Go Hermes fixture")
                assert (hermes_state / "exit.json").is_file()
                assert not disk(hermes_meta).get("cli_identity") and not disk(hermes_meta).get("hermes_native")
                assert identities() == identity

            marker = candidate_state / "go-rollback-published.json"
            try:
                assert not (state / local).exists() and not marker.exists()
                refusal = subprocess.run(export, env=rust_env, capture_output=True, timeout=12)
                assert refusal.returncode and b"use the original Go supervisor" in refusal.stderr, refusal
                assert not (state / local).exists() and not marker.exists()
                assert identities() == identity
            finally:
                with daemon(go, go_env, root / "go-hermes-cleanup.log"):
                    assert owner(go_env, {"op":"delete","session_id":hermes_id})["op"] == "ok"
                    assert {row["id"] for row in listed(go_env)} == {go_held}
                assert not hermes_state.exists()
            print("retained Go Hermes blocked mixed-root export before publication", flush=True)

            auth = home / ".codex/auth.json"
            saved_auth = auth.read_bytes()
            try:
                auth.write_text(json.dumps({"auth_mode":"apikey", "OPENAI_API_KEY":"changed-offline-fixture"}))
                refused_for_account = subprocess.run(export, env=rust_env, capture_output=True, timeout=12)
                assert refused_for_account.returncode and b"authority changed" in refused_for_account.stderr, refused_for_account
                assert not (state / local).exists()
            finally:
                auth.write_bytes(saved_auth)
            unknown_ledger = candidate_state / "semantic"
            unknown_ledger.mkdir(mode=0o700)
            try:
                refusal = subprocess.run(export, env=rust_env, capture_output=True, timeout=12)
                assert refusal.returncode and b"cannot transfer Rust-native operation" in refusal.stderr, refusal
                assert not (state / local).exists()
            finally:
                unknown_ledger.rmdir()
            subprocess.run(export, env=rust_env, check=True, timeout=12)
            assert identities() == identity
            export_meta = disk(state / local / "meta.json")
            assert export_meta["conversation_id"] == native_thread
            assert export_meta["cli_identity"] == disk(candidate_state / "session.json")["go_rollback"]["cli_identity"]
            assert not (state / local / "shim-launch.json").exists()
            if guard:
                go_env["SWARM_FIXTURE_SESSION_ID"] = macos.RESUMED_GO_ID
            with daemon(go, go_env, root / "go-cold-resume.log"):
                assert {row["id"] for row in listed(go_env)} == {go_held, created["session"]["id"]}
                assert identities() == identity
                resumed = owner(go_env, {"op":"launch", "launch":launch | {
                    "name":"Go cold Codex resume", "options":{"resume_from":created["session"]["id"]}
                }}, timeout=55)
                assert resumed["op"] == "launch", resumed
                active = resumed["session"]["id"]
                session = state / active.split("/")[-1]
                until(lambda: "New Rust owner conversation" in "\n".join(snapshot(go_env, active)["lines"]),
                      "Go-loaded Rust-created native conversation")
                turn(go_env, "Go cold resume of the Rust conversation")
                assert disk(session / "meta.json")["conversation_id"] == native_thread
                assert disk(session / "meta.json")["cli_identity"] == export_meta["cli_identity"]
                [rollout] = list((home / ".codex/sessions").rglob(f"*-{native_thread}.jsonl"))
                until(lambda: "Go cold resume of the Rust conversation" in rollout.read_text(),
                      "Go action on original Rust native rollout")
                assert owner(go_env, {"op":"kill","session_id":active})["op"] == "ok"
                until(lambda: disk(session / "meta.json")["status"]["Process"] == "exited", "drained Go resume")
                active = None
                assert owner(go_env, {"op":"kill","session_id":go_held})["op"] == "ok"
                until(lambda: all(has_stopped(pid, birth) for pid, birth in identity[0]),
                      "original Go native processes joined")
                go_held = None
            if guard and guard.paths[0][0].is_socket():
                refused(rust, rust_env, "Go Codex short listener custody remains")
            print("pinned Go resumed the stopped Rust-created conversation with the original Go PTY still live", flush=True)

    finally:
        if rust_held:
            with daemon(rust, rust_env, root / "rust-cleanup.log"):
                assert owner(rust_env, {"op":"kill","session_id":rust_held})["op"] == "ok"
        if go_held or active:
            with daemon(go, go_env, root / "cleanup.log"):
                running = {row["id"] for row in listed(go_env) if row["status"]["Process"] == "running"}
                for identifier in {go_held, active} & running:
                    result = subprocess.run([go, "kill", identifier], env=go_env, capture_output=True, timeout=10)
                    assert result.returncode == 0, result.stderr
                    path = state / identifier.split("/")[-1] / "exit.json"
                    until(lambda: path.is_file(), "stopped live fixture")
        model.shutdown()
        model.server_close()
        if guard:
            guard.close()


if __name__ == "__main__":
    assert sys.platform in ("linux", "darwin")
    os.umask(0o077)
    go, rust, root = (Path(value).resolve() for value in sys.argv[1:4])
    version = sys.argv[4] if len(sys.argv) >= 5 else "0.151.0"
    assert version in ("0.151.0", "0.172.0"), version
    cold = "--cold" in sys.argv[5:]
    assert not cold or version == "0.172.0"
    rehearse(go, rust, root, version, cold=cold)
