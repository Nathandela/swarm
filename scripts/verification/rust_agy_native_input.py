#!/usr/bin/env python3
"""Exercise native registration and delivery through Rust spawn, shim and message."""
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
from pathlib import Path
import secrets
import socket
import struct
import subprocess
import sys
import tempfile
import threading
import time

from parity_agy import wait_for

CONVERSATION = "11111111-1111-4111-8111-111111111111"
FIXTURE = r'''
import hashlib, json, os, re, subprocess, sys, time
from pathlib import Path

args = sys.argv[1:]
conversation = "$CONVERSATION"
if args == ["--version"]:
    print("1.4.7")
    raise SystemExit(0)
if args == ["--help"]:
    raise SystemExit(0)
if args[:1] == ["agentapi"]:
    assert args[2] == os.environ["ANTIGRAVITY_CONVERSATION_ID"] == conversation
    assert os.environ["ANTIGRAVITY_CSRF_TOKEN"] == "fixture-private-csrf"
    if args[1] == "get-conversation-metadata":
        revocation = Path("revoke-before-effect")
        if revocation.exists():
            trial = json.loads(revocation.read_text())
            operation = trial["operation"]
            operation_hash = hashlib.sha256(operation.encode()).hexdigest()
            root = Path.cwd().parent / "s"
            if (root / "rust-v1/operations" / f"semantic-{operation_hash}.json").exists():
                trial["remaining"] -= 1
                if trial["remaining"] == 0:
                    ledger = root / "discord/rust-text-v1.json"
                    state = json.loads(ledger.read_text())
                    id = state["worker_inputs"][operation]["source_id"]
                    state["messages"][id]["state"] = "withdrawn"
                    replacement = ledger.with_suffix(".next")
                    replacement.write_text(json.dumps(state))
                    replacement.chmod(0o600)
                    replacement.replace(ledger)
                    revocation.unlink()
                else:
                    revocation.write_text(json.dumps(trial))
        result = {"conversationMetadata": {"metadata": {
            "rootConversationId": conversation, "projectId": "fixture-project",
        }}}
    else:
        assert args[1] == "send-message"
        if Path("hold-native-effect").exists():
            Path("native-effect-entered").touch()
            deadline = time.monotonic() + 4
            while not Path("release-native-effect").exists():
                assert time.monotonic() < deadline
                time.sleep(.025)
        with Path("deliveries").open("a") as stream:
            stream.write(json.dumps(args[3]) + "\n")
        result = {"sendMessage":{"recipientId":conversation, "content":args[3]}}
        if args[3] == "uncertain native reply":
            result["sendMessage"]["content"] = "incorrect echo"
    print(json.dumps({"response":result}))
    raise SystemExit(0)

prompt = args[args.index("--prompt-interactive") + 1]
assert prompt.endswith("\n\nOwner request:\nordinary launch")
command = re.search(r"running this exact command in your current workspace, using normal tool permissions: (.+)", prompt)[1]
deadline = time.monotonic() + 8
Path("worker-live").touch()
while not Path("allow-registration").exists():
    assert time.monotonic() < deadline
    time.sleep(.05)
provider_env = {
    **os.environ,
    "ANTIGRAVITY_CONVERSATION_ID": conversation, "ANTIGRAVITY_PROJECT_ID":"fixture-project",
    "ANTIGRAVITY_TRAJECTORY_ID":"fixture-trajectory", "ANTIGRAVITY_LS_ADDRESS":"127.0.0.1:41999",
    "ANTIGRAVITY_CSRF_TOKEN":"fixture-private-csrf", "ANTIGRAVITY_AGENTAPI_EXE":__file__,
    "ANTIGRAVITY_SOURCE_METADATA":json.dumps({
        "tool":{"conversationId":conversation,"stepIndex":2,
                "toolCall":{"name":"run_command","argumentsJson":json.dumps({"CommandLine":command})}},
    }),
}
for key, value in [
    ("ANTIGRAVITY_LS_ADDRESS","192.0.2.1:41999"),
    ("ANTIGRAVITY_PROJECT_ID","forged-project"),
    ("ANTIGRAVITY_SOURCE_METADATA","{}"),
]:
    assert subprocess.run(command, shell=True, env={**provider_env, key:value}).returncode == 1
assert subprocess.run(command, shell=True, env=provider_env, cwd=provider_env["HOME"]).returncode == 1
assert subprocess.run(command, shell=True, env=provider_env).returncode == 0
Path("registration-probes-complete").touch()
print("Fixture permission request. The terminal must receive no bytes.", flush=True)
for line in sys.stdin:
    Path("unexpected-terminal-input").write_text(line)
'''


def run(binary, guild=False, ambiguous_proof=False):
    with tempfile.TemporaryDirectory(prefix="agn-", dir=Path("/tmp").resolve()) as tmp:
        root = Path(tmp)
        home, work, tools, state = (root / key for key in ("home", "work", "bin", "s"))
        for path in (home, work, tools, state):
            path.mkdir(mode=0o700)
        connector = state / "discord"
        connector.mkdir(mode=0o700)
        credentials = root / "fake-token"
        credentials.write_text("synthetic credential unused by this test")
        credentials.chmod(0o600)
        daemon_socket = state / "d"
        config = connector / "config.json"
        config.write_text(json.dumps({
            "version": 1, "enabled": True, "mode": "teammate" if guild else "personal",
            "installation_id": "fixture-only", "name": "Local fixture",
            "application_id": "234", "owner_id": "123",
            **({"guild_ids":["301"],"channel_ids":["455"]} if guild else {"dm_channel_id": "456"}),
            "token_file": str(credentials), "daemon_socket": str(daemon_socket),
            "work_dir": str(work), "manager_cli": "codex",
        }))
        config.chmod(0o600)
        ownership = connector / ".rust-offline-owner.json"
        ownership.write_text(json.dumps("fixture-only"))
        ownership.chmod(0o600)
        ledger_file = connector / "rust-text-v1.json"
        snowflake_base = (time.time_ns() // 1_000_000 - 1_420_070_400_000) << 22
        source_key = lambda key: str(snowflake_base + int(key)) if guild else key
        ledger = {"version":2, "identity":["fixture-only","234","123","" if guild else "456",str(daemon_socket)],
                  "consent_floor":source_key("400"),"history_cursor":source_key("420"),"messages":{},"outbox":{},"worker_inputs":{}}
        def save():
            updated = ledger_file.with_suffix(".update")
            updated.write_text(json.dumps(ledger))
            updated.chmod(0o600)
            updated.replace(ledger_file)
        save()
        revoked_audiences = set()
        epoch_changes = set()
        guild_probes = []
        guild_stop = threading.Event()
        guild_listener = None
        if guild:
            epoch = connector / "guild-worker-epoch.json"
            epoch.write_text(json.dumps({"epoch":"a" * 24,"cancelled":{}}))
            epoch.chmod(0o600)
            guild_path = connector / "g"
            guild_listener = socket.socket(socket.AF_UNIX)
            guild_listener.bind(str(guild_path))
            guild_path.chmod(0o600)
            guild_listener.listen()
            guild_listener.settimeout(.1)

            def serve_fixture_audience():
                while not guild_stop.is_set():
                    try:
                        conn, _ = guild_listener.accept()
                    except TimeoutError:
                        continue
                    with conn, conn.makefile("rwb") as stream:
                        conn.settimeout(2)
                        frame = stream.read(4)
                        if len(frame) != 4:
                            continue
                        data = stream.read(struct.unpack(">I", frame)[0])
                        assert data[0] == 1
                        request = json.loads(data[1:])
                        operation = request["operation"]
                        guild_probes.append(operation)
                        latest = json.loads(ledger_file.read_text())["worker_inputs"][operation]
                        assert latest["composer"] == request["composer"]
                        assert latest["provider"] == request["provider"] == "agy"
                        if operation in epoch_changes:
                            changed = epoch.with_suffix(".next")
                            changed.write_text(json.dumps({"epoch":"b" * 24,"cancelled":{}}))
                            changed.chmod(0o600)
                            changed.replace(epoch)
                        receipt = b"\x01" + json.dumps({"epoch":request["epoch"],"challenge":request["challenge"],
                            "authorized":operation not in revoked_audiences}).encode()
                        stream.write(struct.pack(">I",len(receipt)) + receipt)
                        stream.flush()

            guild_thread = threading.Thread(target=serve_fixture_audience)
            guild_thread.start()
        provider = tools / "agy"
        provider.write_text(f"#!{sys.executable}\n" + FIXTURE.replace("$CONVERSATION", CONVERSATION))
        provider.chmod(0o700)
        env = {
            "HOME": str(home), "PATH": f"{tools}:/usr/bin:/bin", "TERM": "xterm-256color",
            "SHELL": "/bin/sh", "TMPDIR": str(root),
            "SWARM_DAEMON_STATE": str(state), "SWARM_DAEMON_SOCK": str(daemon_socket),
        }

        def cli(*args, ok=True):
            result = subprocess.run([binary, *args], cwd=work, env=env, stdin=subprocess.DEVNULL,
                                    capture_output=True, timeout=15)
            if ok:
                assert result.returncode == 0, (args, result.stderr.decode())
            return result

        def start():
            daemon = subprocess.Popen([binary, "daemon"], cwd=work, env=env,
                                      stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
            try:
                wait_for(lambda: cli("ls", "--json", ok=False).returncode == 0, "daemon")
            except BaseException:
                daemon.terminate()
                daemon.wait(timeout=5)
                raise
            return daemon

        def deliveries():
            path = work / "deliveries"
            return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

        def request(body, caps, before_request=None):
            def exchange(peer, message):
                payload = b"\x01" + json.dumps(message).encode()
                peer.write(struct.pack(">I", len(payload)) + payload)
                peer.flush()
                data = peer.read(struct.unpack(">I", peer.read(4))[0])
                assert data[0] == 1
                return json.loads(data[1:])

            with socket.socket(socket.AF_UNIX) as conn:
                conn.settimeout(15)
                conn.connect(str(daemon_socket))
                with conn.makefile("rwb") as peer:
                    hello = exchange(peer, {"op":"hello","protocol_version":1,"capabilities":caps})
                    if before_request:
                        before_request(hello)
                    return exchange(peer, body | {"endpoint_id":hello["endpoint_id"]})

        def digest(value):
            raw = json.dumps(value, separators=(",", ":"), ensure_ascii=False).replace("&", r"\u0026").replace("<", r"\u003c").replace(">", r"\u003e")
            return hashlib.sha256(raw.encode()).hexdigest()

        def seal_audience(source):
            route = source["route"]
            source["guild_audience_digest"] = digest(["234", source["author"]["id"], route["guild_id"],
                route["channel_id"], route.get("parent_id"), route["message_id"], source["revision"],
                source["audience_verified_at_ms"],source.get("audience_roles")])

        def prepare(text, source_id, mint=None):
            operation = mint or f"semantic:{time.time_ns() // 1_000_000}:{secrets.token_hex(16)}"
            instance = request({"op":"semantic_target","session_id":sid},
                               ["semantic-target"])["semantic_target_instance"]
            source_id = source_key(source_id)
            source = f"<@234> {sid} {text}" if guild else f"owner original for message {source_id}"
            revision = digest(["123",source,"456","301" if guild else "",source_id,None,"",None])
            request_id = "da_" + digest(["fixture-only",source_id])[:32]
            origin = {"kind":"owner_direct","installation_id":"fixture-only","request_id":request_id,
                      "actor_id":"123","channel_id":"456","message_id":source_id,"revision":revision}
            if guild:
                origin["guild_id"] = "301"
            composer = {"session":sid,"session_instance":instance,"text":text,"origin":origin}
            receipt = {
                "operation_id":request_id,"revision":revision,"text":source,
                "route":{"guild_id":"301" if guild else "","channel_id":"456","message_id":source_id,
                         **({"parent_id":"455"} if guild else {})},
                **({"addressed_text":f"{sid} {text}", "audience_verified_at_ms":time.time_ns() // 1_000_000} if guild else {}),
                "author":{"id":"123"},"authority":"owner_direct","supervisor_socket":str(daemon_socket),
                "state":"sending","worker_operation":operation,
            }

            if guild:
                seal_audience(receipt)
            return operation, composer, receipt

        def persist(operation, composer, source):
            source_id = composer["origin"]["message_id"]
            ledger["messages"][source_id] = source
            ledger["worker_inputs"][operation] = {"source_id":source_id,"provider":"agy","composer":composer}
            save()

        def send(operation, composer, *, allow_origin=True, worker_origin=True, guild_origin=True, before_request=None, ok=True):
            capabilities = ["semantic-send"] + (["message-origin"] if allow_origin else [])
            if worker_origin:
                capabilities.append("worker-discord-origin-v1")
            if guild and guild_origin:
                capabilities.append("worker-discord-guild-origin-v1")
            response = request({"op":"semantic_send","session_id":sid,"operation_id":operation,
                                "body_version":1,"composer_send":composer}, capabilities, before_request)
            if ok:
                assert response["op"] == "ok", response
            else:
                assert response["op"] == "error", response
            return response

        daemon = start()
        sid = None
        try:
            sid = cli("spawn", "--cli", "agy", "--dir", work, "--prompt", "ordinary launch").stdout.decode().strip()
            wait_for(lambda: (work / "worker-live").exists(), "live native fixture")
            assert cli("message", sid, "--text", "before registration", ok=False).returncode == 1
            assert deliveries() == []
            (work / "allow-registration").touch()
            wait_for(lambda: (work / "registration-probes-complete").exists(), "native registration")
            assert b"Fixture permission request" in cli("peek", sid).stdout
            text = "Native exact input.\n第二行 <angle>"
            assert cli("message", sid, "--text", "no origin", ok=False).returncode == 1
            if guild:
                revoked, revoked_composer, revoked_source = prepare("permission revoked at last recheck","408")
                persist(revoked, revoked_composer, revoked_source)
                revoked_audiences.add(revoked)
                send(revoked, revoked_composer, ok=False)
                assert guild_probes == [revoked]
                revoked_audiences.remove(revoked)
                send(revoked, revoked_composer, ok=False)
                assert guild_probes == [revoked]
                assert deliveries() == []
            operation, composer, source = prepare(text, "401")
            # A fully formed origin without a persisted worker-specific cutover grants nothing.
            send(operation, composer, ok=False)
            ledger["manager"] = {"input":{"operation":operation,"composer":composer,"source_id":source_key("401")}}
            ledger["messages"][source_key("401")] = source.copy()
            save()
            send(operation, composer, ok=False)
            persist(operation, composer, source)
            for override in [
                {"kind":"external","actor_id":"999"},
                {"kind":"owner_direct","actor_id":"999"},
                {"channel_id":"999"}, {"message_id":"499"}, {"revision":"stale"},
                {"installation_id":"other"},
            ]:
                send(operation, composer | {"origin": composer["origin"] | override}, ok=False)
            send(operation, composer | {"text":"forged rendered body"}, ok=False)
            send(operation, composer | {"session_instance":"stale-instance"}, ok=False)
            send(operation, composer | {"session":sid + "-different"}, ok=False)
            expired_id = f"semantic:{time.time_ns() // 1_000_000 - 3_601_000}:{secrets.token_hex(16)}"
            expired, expired_composer, expired_source = prepare(text, "407", expired_id)
            persist(expired, expired_composer, expired_source)
            send(expired, expired_composer, ok=False)
            outside, outside_composer, outside_source = prepare(text, "400")
            persist(outside, outside_composer, outside_source)
            send(outside, outside_composer, ok=False)
            if guild:
                send(operation, composer, guild_origin=False, ok=False)
                ledger["messages"][source_key("401")]["route"]["parent_id"] = "999"
                save()
                send(operation, composer, ok=False)
                ledger["messages"][source_key("401")]["route"]["parent_id"] = "455"
                ledger["messages"][source_key("401")]["audience_verified_at_ms"] -= 31_000
                seal_audience(ledger["messages"][source_key("401")])
                save()
                send(operation, composer, ok=False)
                ledger["messages"][source_key("401")]["audience_verified_at_ms"] = time.time_ns() // 1_000_000
                seal_audience(ledger["messages"][source_key("401")])
                save()
            send(operation, composer, allow_origin=False, ok=False)
            ledger["worker_excluded"] = {sid:True}
            save()
            send(operation, composer, ok=False)
            assert deliveries() == []
            ledger["worker_excluded"].pop(sid)
            ledger["consent_floor"] = ""
            ledger["history_cursor"] = ""
            save()
            send(operation, composer, worker_origin=False, ok=False)
            assert deliveries() == []
            (work / "hold-native-effect").touch()
            with ThreadPoolExecutor(max_workers=1) as tasks:
                pending = tasks.submit(send, operation, composer)
                try:
                    wait_for(lambda: (work / "native-effect-entered").exists(), "guarded native effect")
                    refused = cli("discord", "disconnect", ok=False)
                    assert refused.returncode != 0 and b"configuration change is in progress" in refused.stderr
                    assert json.loads(config.read_text())["enabled"] is True
                finally:
                    (work / "release-native-effect").touch()
                pending.result(timeout=10)
            (work / "hold-native-effect").unlink()
            saved_enrollment = config.read_text()
            late, late_composer, late_source = prepare("late cutover after disconnect", "406")
            def revoke_after_negotiation(hello):
                assert "worker-discord-origin-v1" in hello["capabilities"]
                cli("discord", "disconnect")
                assert json.loads(config.read_text())["enabled"] is False
                persist(late, late_composer, late_source)
            send(late, late_composer, before_request=revoke_after_negotiation, ok=False)
            assert deliveries() == [text]
            config.write_text(saved_enrollment)  # Re-enable the private fixture for independent controls.

            def status(op):
                return json.loads(cli("message", sid, "--status", "--operation-id", op).stdout)

            first = status(operation)
            assert first["state"] == "submitted" and first["native_session_id"] == CONVERSATION
            assert first["agent"] == "agy" and first["native_generation"]
            assert first["origin"] == composer["origin"]
            send(operation, composer)
            saved_config = config.read_text()
            config.write_text(json.dumps(json.loads(saved_config) | {"installation_id":"replacement"}))
            try:
                send(operation, composer, ok=False)
                assert status(operation) == first
            finally:
                config.write_text(saved_config)
            assert deliveries() == [text]

            daemon.kill()
            daemon.wait(timeout=5)
            daemon = start()
            assert status(operation) == first
            send(operation, composer)

            stale, stale_composer, stale_source = prepare("edited source", "402")
            persist(stale, stale_composer, stale_source)
            ledger["messages"][source_key("402")] = stale_source | {"text":"new source revision"}
            save()
            send(stale, stale_composer, ok=False)

            revoked, revoked_composer, revoked_source = prepare("withdrew before effect", "403")
            persist(revoked, revoked_composer, revoked_source)
            (work / "revoke-before-effect").write_text(json.dumps({"operation":revoked,"remaining":2}))
            send(revoked, revoked_composer, ok=False)
            assert status(revoked)["state"] == "rejected"
            assert not (work / "revoke-before-effect").exists()
            ledger["messages"][source_key("403")] = revoked_source | {"state":"withdrawn"}
            save()

            ambiguous, ambiguous_composer, ambiguous_source = prepare("uncertain native reply", "404")
            persist(ambiguous, ambiguous_composer, ambiguous_source)
            if guild and ambiguous_proof:
                epoch_changes.add(ambiguous)
            send(ambiguous, ambiguous_composer, ok=False)
            assert status(ambiguous)["state"] == "outcome_unknown"
            send(ambiguous, ambiguous_composer, ok=False)
            blocked, blocked_composer, blocked_source = prepare("blocked by earlier operation", "405")
            persist(blocked, blocked_composer, blocked_source)
            send(blocked, blocked_composer, ok=False)
            assert deliveries() == ([text] if guild and ambiguous_proof else [text, "uncertain native reply"])
            assert not (work / "unexpected-terminal-input").exists()
        finally:
            if guild_listener is not None:
                guild_stop.set()
                guild_thread.join(timeout=3)
                guild_listener.close()
            if sid:
                cli("kill", sid, ok=False)
            daemon.terminate()
            try:
                daemon.wait(timeout=5)
            except subprocess.TimeoutExpired:
                daemon.kill()
                daemon.wait()


if __name__ == "__main__":
    binary = Path(sys.argv[1]).resolve(strict=True)
    guild = "--guild" in sys.argv[2:]
    run(binary, guild=guild, ambiguous_proof="--ambiguous-proof" in sys.argv[2:])
    scope = "guild thread" if guild else "personal DM"
    print(f"{scope}: agy registration, exact native delivery, daemon recovery and ambiguous non-replay passed.")
