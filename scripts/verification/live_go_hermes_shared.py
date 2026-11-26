#!/usr/bin/env python3
"""Qualify original Go's shared-native Hermes viewer in the guarded native harness."""

import argparse
import hashlib
import json
import os
import sqlite3
import subprocess
import time
from pathlib import Path
from urllib.request import urlopen
from uuid import uuid4

from live_go_shim import LIVE, attach, connect, grid_text, snapshot
from stopped_go_state import await_value, daemon, disk, fixture_env, listed, owner, private, refused


def rehearse(root, go, rust, stats):
    state, work = root / "s", root / "work"
    state.mkdir(mode=0o700)
    work.mkdir(mode=0o700)
    go_env = dict(os.environ) | fixture_env(root, state, root / "bin")
    # Keep the harness's disposable user home rather than the Hermes profile path.
    go_env["HOME"] = os.environ["HOME"]
    rust_env = go_env | {"SWARM_DAEMON_STOPPED_GO_COMPAT": "1", LIVE: "1"}
    writer_env = rust_env | {"SWARM_DAEMON_GO_HERMES_SEMANTIC_COMPAT": "1"}
    discord = state / "discord"
    discord.mkdir(mode=0o700)
    install = "local-hermes-native-witness"
    private(discord / "config.json", {
        "version": 1, "enabled": True, "mode": "legacy", "installation_id": install,
        "name": "Offline Hermes native fixture", "application_id": "123", "owner_id": "124",
        "token_file": str(root / "nonexistent-token"), "daemon_socket": go_env["SWARM_DAEMON_SOCK"],
        "work_dir": str(work),
    })
    private(discord / ".rust-offline-owner.json", install)
    workers = []
    active = None

    def proof():
        m = disk(session / "meta.json")
        raw = (session / "hermes-input.json").read_bytes()
        d = json.loads(raw)
        b = d["binding"]
        assert m["schema_version"] == 5 and m["hermes_native"] and m["status"]["Process"] == "running", m
        assert d["worker_id"] == m["id"] and d["shim_pid"] == m["shim_pid"]
        assert d["shim_start_time"] == m["shim_start_time"] and d["provider_version"] == "0.22.5"
        assert m["conversation_id"] == b["session_id"] and b["directory"] == str(work)
        assert b["home"] == os.environ["HERMES_HOME"] and b["control_home"] == b["home"]
        assert b["owner_pid"] != d["shim_pid"]
        return raw, (session / "shim.sock").stat().st_ino, d

    def launch_worker(name):
        launched = owner(go_env, {"op":"launch", "launch":{
            "agent":"hermes", "name":name, "cwd":str(work),
            "env":[f"{key}={value}" for key,value in go_env.items()], "cols":110, "rows":36,
        }}, timeout=105)
        assert launched["op"] == "launch", launched
        worker = launched["session"]["id"]
        workers.append(worker)
        return worker, state / worker.split("/")[-1]

    def request_for(text):
        operation = f"semantic:{time.time_ns() // 1000000}:{uuid4().hex}"
        return {"op":"semantic_send", "operation_id":operation, "session_id":active, "body_version":1,
                "composer_send":{"session":active, "session_instance":disk(session / "capabilities.json")["session_instance"],
                                 "text":text}}

    def send_semantic(text, thread):
        send = request_for(text)
        options = {"caps":("semantic-send",), "timeout":35}
        reply = owner(go_env, send, **options)
        assert reply["op"] == "ok", reply
        reply = owner(go_env, {"op":"semantic_status", "session_id":active, "operation_id":send["operation_id"]}, **options)
        receipt = reply["semantic_receipt"]
        assert receipt["state"] == "submitted" and receipt["native_session_id"] == thread, receipt
        return send, receipt

    def native_turns():
        database = Path(os.environ["HERMES_HOME"]) / "state.db"
        with sqlite3.connect(database.as_uri() + "?mode=ro", uri=True) as connection:
            return connection.execute("SELECT session_id, role, content FROM messages ORDER BY id").fetchall()

    def view(env, read_only=False):
        def ready():
            grid = "\n".join(snapshot(env, active)["lines"]).lower()
            return "hermes" in grid or "swarm-native-fixture" in grid
        await_value(ready)
        with connect(env) as connection:
            generation, grid = attach(connection, active)
            text = grid_text(grid).lower()
            assert "hermes" in text or "swarm-native-fixture" in text, text
            if read_only:
                send, receive, control, _, endpoint = connection
                send(3, b"RUST_SHARED_VIEW_MUST_NOT_SEND\r")
                scope = {"endpoint_id":endpoint, "session_id":active, "generation":generation}
                control("resize", **scope, cols=131, rows=48)
                control("detach", **scope)
                while True:
                    tag, body = receive()
                    if tag == 1:
                        assert json.loads(body)["op"] == "detach"
                        break
        row = next(row for row in listed(env) if row["id"] == active)
        assert row["status"]["Process"] == "running", row
        if read_only:
            assert row["summary"].startswith("Read-only Hermes")
            assert row["conversation_id"] == original[2]["binding"]["session_id"]
            unchanged = snapshot(env, active)
            assert (unchanged["cols"], unchanged["rows"]) == (grid["cols"], grid["rows"])
            assert "RUST_SHARED_VIEW_MUST_NOT_SEND" not in "\n".join(unchanged["lines"])

    try:
        with daemon(go, go_env, root / "go.log"):
            active, session = launch_worker("Original Go shared native")
            _, sibling = launch_worker("Second Go native viewer")
            original = proof()
            thread = original[2]["binding"]["session_id"]
            other = disk(sibling / "hermes-input.json")["binding"]
            assert other["session_id"] != thread
            assert {k:v for k,v in other.items() if k != "session_id"} == {
                k:v for k,v in original[2]["binding"].items() if k != "session_id"}
            view(go_env)
            first, receipt = send_semantic("GO_SHARED_BEFORE_RUST", thread)
            await_value(lambda: (thread, "assistant", "SYNTHETIC_NATIVE_ACK") in native_turns())
            print("original Go mounted actual shared-native Hermes viewer and sent a proved turn", flush=True)
        descriptor = session / "hermes-input.json"
        different = original[2] | {"binding": other}
        private(descriptor, different)
        try:
            refused(rust, rust_env, "Go Hermes descriptor disagrees with its original viewer")
        finally:
            descriptor.write_bytes(original[0])

        files = ("meta.json", "shim-launch.json", "session-instance", "capabilities.json", "hermes-input.json")
        source_evidence = {name: (session / name).read_bytes() for name in files}
        with daemon(rust, rust_env, root / "rust-viewer.log"):
            view(rust_env, read_only=True)
            assert proof() == original
            withheld = owner(rust_env, first, caps=("semantic-send",))
            assert withheld["op"] == "error", withheld
            for action in ("kill", "delete"):
                refused_action = owner(rust_env, {"op":action,"session_id":active})
                assert refused_action["op"] == "error", refused_action
            assert proof() == original
            assert not any("RUST_SHARED_VIEW_MUST_NOT_SEND" in str(row) for row in native_turns())
        status_request = {"op":"semantic_status", "session_id":active, "operation_id":first["operation_id"]}
        semantic_caps = ("semantic-send", "semantic-target")
        with daemon(rust, writer_env, root / "rust-writer.log"):
            view(writer_env, read_only=True)
            recovered = owner(writer_env, status_request, caps=semantic_caps)
            assert recovered["semantic_receipt"] == receipt, recovered
            target = owner(writer_env, {"op":"semantic_target","session_id":active}, caps=semantic_caps)
            assert target["semantic_target_instance"] == first["composer_send"]["session_instance"], target
            rust_send = request_for("RUST_SHARED_DURING_HANDOFF")
            wrong_instance = rust_send | {"composer_send":rust_send["composer_send"] | {"session_instance":"1" * 32}}
            assert owner(writer_env, wrong_instance, caps=semantic_caps)["op"] == "error"
            rust_receipt = json.loads(subprocess.check_output([
                rust, "message", active, "--text", rust_send["composer_send"]["text"],
                "--operation-id", rust_send["operation_id"],
            ], env=writer_env, timeout=50))
            assert rust_receipt["state"] == "submitted" and rust_receipt["native_session_id"] == thread, rust_receipt
            assert owner(writer_env, rust_send, caps=semantic_caps)["op"] == "ok"
            different = rust_send | {"composer_send":rust_send["composer_send"] | {"text":"DO_NOT_RESEND"}}
            assert owner(writer_env, different, caps=semantic_caps)["op"] == "error"
            await_value(lambda: native_turns().count((thread, "assistant", "SYNTHETIC_NATIVE_ACK")) == 2)
            assert proof() == original
        log = state / "idempotency" / "idempotency.log"
        rows = [json.loads(line) for line in log.read_text().splitlines()]
        executing = next(row for row in rows if row["operation_id"] == rust_send["operation_id"]
                         and row["phase"] == "executing")
        with daemon(rust, writer_env, root / "rust-restarted.log"):
            assert owner(writer_env, rust_send, caps=semantic_caps)["op"] == "ok"
        assert all((session / name).read_bytes() == data for name, data in source_evidence.items())
        with daemon(go, go_env, root / "go-restored.log"):
            view(go_env)
            assert proof() == original
            replay = owner(go_env, first, caps=("semantic-send",), timeout=35)
            assert replay["op"] == "ok"
            reread = owner(go_env, {"op":"semantic_status", "session_id":active,
                                   "operation_id":first["operation_id"]}, caps=("semantic-send",), timeout=35)
            assert reread["semantic_receipt"] == receipt, reread
            assert owner(go_env, rust_send, caps=semantic_caps)["op"] == "ok"
            reread_rust = owner(go_env, status_request | {"operation_id":rust_send["operation_id"]}, caps=semantic_caps)
            assert reread_rust["semantic_receipt"] == rust_receipt, reread_rust
            assert owner(go_env, different, caps=semantic_caps)["op"] == "error"
            send_semantic("GO_SHARED_AFTER_RUST", thread)
            await_value(lambda: native_turns().count((thread, "assistant", "SYNTHETIC_NATIVE_ACK")) == 3)
            assert [row for row in native_turns() if row[1] == "user"] == [
                (thread, "user", "GO_SHARED_BEFORE_RUST"), (thread, "user", "RUST_SHARED_DURING_HANDOFF"),
                (thread, "user", "GO_SHARED_AFTER_RUST")]
        # Reuse the on-disk intent shape while reserving an operation that never reached Hermes.
        abandoned = request_for("RUST_SHARED_ABANDONED")
        abandoned_body = abandoned["composer_send"]
        fields = [active, abandoned_body["session_instance"], "", abandoned_body["text"]]
        hashed = b"".join(len(field.encode()).to_bytes(4, "big") + field.encode() for field in fields)
        executing["operation_id"] = abandoned["operation_id"]
        executing["request_hash"] = hashlib.sha256(hashed).hexdigest()
        executing["request"]["hermes_input"]["hash"] = hashlib.sha256(abandoned_body["text"].encode()).hexdigest()
        with log.open("a") as stream:
            stream.write(json.dumps(executing) + "\n")
            stream.flush()
            os.fsync(stream.fileno())
        for binary, env, name in ((rust, writer_env, "rust-uncertain"), (go, go_env, "go-uncertain")):
            with daemon(binary, env, root / (name + ".log")):
                followup = request_for("MUST_NOT_CROSS_UNKNOWN_ADMISSION")
                for send in (abandoned, followup):
                    result = owner(env, send, caps=semantic_caps, timeout=35)
                    assert result["op"] == "error" and result["error_code"] == "outcome_unknown", result
        calls = json.load(urlopen(stats, timeout=5))["main_calls"]
        assert calls == 3, calls
        result = {"go_shared_native": True, "original_go_to_rust_to_go": True,
                  "native_thread_and_custody_retained": True,
                  "same_scope_forged_thread_refused": True, "rust_input_withheld": True,
                  "rust_lifecycle_withheld": True, "go_receipt_replayed": True, "rust_receipt_replayed_in_go": True, "unknown_admission_fenced_on_both_owners": True,
                  "model_calls": calls}
        (root / "result.json").write_text(json.dumps(result))
    finally:
        if workers:
            with daemon(go, go_env, root / "go-cleanup.log"):
                for worker in workers:
                    owner(go_env, {"op":"kill", "session_id":worker}, timeout=40)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("root", "swarm", "candidate"):
        parser.add_argument("--" + name, required=True, type=Path)
    parser.add_argument("--stats", required=True)
    args = parser.parse_args()
    os.umask(0o077)
    rehearse(args.root, args.swarm, args.candidate, args.stats)
