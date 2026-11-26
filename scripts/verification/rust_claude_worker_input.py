#!/usr/bin/env python3
"""Rust's native Claude worker input with a real parked permission dialog."""
import argparse
import hashlib
import http.server
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import threading
import time

from native_fixture_sandbox import guarded, check_guards

OPENING = "CLAUDE_NATIVE_WORKER_PERMISSION_WITNESS"
MESSAGE = "CLAUDE_NATIVE_WORKER_INPUT_WITNESS: exact owner-direct message"


def digest(value):
    return hashlib.sha256(json.dumps(value, separators=(",", ":")).encode()).hexdigest()


def save(path, data):
    temp = path.with_suffix(".tmp")
    temp.write_text(json.dumps(data))
    temp.chmod(0o600)
    temp.replace(path)


class Provider(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        if "/messages" not in self.path:
            self.send_error(404)
            return
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        messages = json.dumps(body.get("messages", []))
        is_opening = any(tool.get("name") == "Bash" for tool in body.get("tools", [])) and OPENING in messages
        if is_opening and body.get("stream"):
            self.server.observed.append(messages)
        tool = is_opening and len(self.server.observed) == 1
        model = body.get("model")
        usage = dict(input_tokens=1, output_tokens=1)
        if not body.get("stream"):
            response = dict(id="msg_aux", type="message", role="assistant", model=model,
                            usage=usage, stop_reason="end_turn",
                            content=[dict(type="text", text="Fixture")])
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps(response).encode())
            return
        block = dict(type="tool_use", name="Bash", id="toolu_modal", input={}) if tool else dict(type="text", text="")
        delta = (dict(type="input_json_delta", partial_json=json.dumps(
            dict(command=f'touch "{self.server.sentinel}"', description="Synthetic permission boundary")))
                 if tool else dict(type="text_delta", text="Synthetic native input processed"))
        events = [
            ("message_start", dict(message=dict(id=f"msg_{len(self.server.observed)}", type="message",
              role="assistant", model=model, usage=usage, content=[], stop_reason=None, stop_sequence=None))),
            ("content_block_start", dict(index=0, content_block=block)),
            ("content_block_delta", dict(index=0, delta=delta)),
            ("content_block_stop", dict(index=0)),
            ("message_delta", dict(delta=dict(stop_reason="tool_use" if tool else "end_turn",
                                              stop_sequence=None), usage=usage)),
            ("message_stop", {}),
        ]
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for event, fields in events:
            self.wfile.write(f"event: {event}\ndata: {json.dumps(dict(type=event, **fields))}\n\n".encode())


def verify(swarm, installed, root, model, producer_test, guild):
    home, work, state, tools = [root / name for name in ("home", "work", "s", "bin")]
    place = state / "discord"
    for path in (home, work, state, tools, place, home / ".claude"):
        path.mkdir(mode=0o700)
    claude = tools / "claude"
    shutil.copyfile(installed, claude)
    claude.chmod(0o700)
    daemon_socket = str(root / "d")
    version = subprocess.check_output(guarded([str(claude), "--version"], root, [model.server_port]),
                                      env={"HOME": str(home), "PATH": "/usr/bin:/bin"}, text=True).split()[0]
    credential = root / "synthetic-discord"
    credential.write_text("unused synthetic Discord token")
    credential.chmod(0o600)
    route = dict(guild_ids=["301"], channel_ids=["455"]) if guild else dict(dm_channel_id="456")
    save(place / "config.json", dict(version=1, enabled=True, mode="teammate" if guild else "personal",
         installation_id="fixture-only", name="Private fixture", application_id="234", owner_id="123",
         **route, token_file=str(credential), daemon_socket=daemon_socket,
         work_dir=str(work), manager_cli="codex"))
    save(place / ".rust-offline-owner.json", "fixture-only")
    ledger = dict(version=2, identity=["fixture-only", "234", "123", "" if guild else "456", daemon_socket],
                  consent_floor="400", history_cursor="420", messages={}, outbox={}, worker_inputs={})
    ledger_path = place / "rust-text-v1.json"
    save(ledger_path, ledger)
    env = dict(PATH=f"{tools}:/usr/bin:/bin", HOME=str(home), TMPDIR=str(root), TERM="xterm-256color",
               LANG="en_US.UTF-8", ANTHROPIC_API_KEY="fixture-worker",
               SWARM_DAEMON_STATE=str(state), SWARM_DAEMON_SOCK=daemon_socket,
               SWARM_DAEMON_LOCK=str(root / "lock"))
    save(home / ".claude.json", dict(hasCompletedOnboarding=True, lastOnboardingVersion=version,
         theme="dark", projects={str(work): dict(hasTrustDialogAccepted=True)},
         customApiKeyResponses=dict(approved=[env["ANTHROPIC_API_KEY"]], rejected=[])))
    save(home / ".claude" / "settings.json", dict(
        env=dict(ANTHROPIC_BASE_URL=f"http://127.0.0.1:{model.server_port}",
                 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC="1", DISABLE_AUTOUPDATER="1",
                 CLAUDE_CODE_TMPDIR=str(root), CLAUDE_CODE_MAX_RETRIES="0"),
        permissions=dict(disableAutoMode="disable", ask=["Bash"])))
    caps = ["semantic-target", "semantic-send", "message-origin", "worker-discord-origin-v1"]

    if guild:
        caps.append("worker-discord-guild-origin-v1")

    def wire(body):
        with socket.socket(socket.AF_UNIX) as conn:
            conn.settimeout(12)
            conn.connect(daemon_socket)
            with conn.makefile("rwb") as peer:
                def exchange(req):
                    payload = b"\x01" + json.dumps(req).encode()
                    peer.write(struct.pack(">I", len(payload)) + payload)
                    peer.flush()
                    data = peer.read(struct.unpack(">I", peer.read(4))[0])
                    assert data[:1] == b"\x01", data
                    return json.loads(data[1:])
                hello = exchange(dict(op="hello", protocol_version=1, capabilities=caps))
                return exchange(body | {"endpoint_id": hello["endpoint_id"]})

    def run(*args, ok=True):
        result = subprocess.run([swarm, *args], env=env, cwd=work, capture_output=True, text=True, timeout=12)
        if ok:
            assert result.returncode == 0, (args, result.stderr)
        return result.stdout

    def until(check, condition, seconds=15):
        stop = time.monotonic() + seconds
        while time.monotonic() < stop:
            if value := check():
                return value
            time.sleep(0.12)
        raise AssertionError(condition)

    with (root / "daemon.log").open("w+") as output:
        daemon = subprocess.Popen(guarded([swarm, "daemon"], root, [model.server_port]), env=env,
                                  stdout=output, stderr=output, start_new_session=True)
        sid = None
        try:
            until(Path(daemon_socket).is_socket, "private owner socket")
            sid = run("spawn", "--cli", "claude", "--dir", str(work), "--prompt", OPENING).strip()

            def modal():
                screen = run("peek", sid)
                if "I am using this for local development" in screen:
                    run("send", sid, "--key", "down")
                    run("send", sid, "--key", "enter")
                return "Do you want to proceed?" in screen

            until(modal, "native permission dialog")
            target = lambda: wire(dict(op="semantic_target", session_id=sid)).get("semantic_target_instance")
            instance = until(target, "sender and guard native custody")
            descriptor = next(state.rglob(f"{sid.split('/')[-1]}/claude-input.json"))
            assert json.loads(descriptor.read_text())["provider_version"] == version
            operation = f"semantic:{time.time_ns() // 1_000_000}:{secrets.token_hex(16)}"
            message_id = "401"
            source_text = "synthetic owner source"
            revision = digest(["123", source_text, "456", "", message_id, None, "", None])
            request_id = "da_" + digest(["fixture-only", message_id])[:32]
            origin = dict(kind="owner_direct", installation_id="fixture-only", request_id=request_id,
                          actor_id="123", channel_id="456", message_id=message_id, revision=revision)
            composer = dict(session=sid, session_instance=instance, text=MESSAGE, origin=origin)
            send = dict(op="semantic_send", session_id=sid, operation_id=operation,
                        body_version=1, composer_send=composer)
            assert wire(send)["op"] == "error"  # Caller-provided origin alone has no authority.
            if producer_test is None:
                ledger["messages"][message_id] = dict(operation_id=request_id, revision=revision, text=source_text,
                     route=dict(guild_id="", channel_id="456", message_id=message_id), author=dict(id="123"),
                     authority="owner_direct", supervisor_socket=daemon_socket, state="sending",
                     worker_operation=operation)
                ledger["worker_inputs"][operation] = dict(source_id=message_id, provider="claude", composer=composer)
                save(ledger_path, ledger)
                assert wire(send | {"composer_send": composer | {"text": "fabricated"}})["op"] == "error"
                assert wire(send)["op"] == "ok"
                assert wire(send)["op"] == "ok"
            else:
                witness = subprocess.run(
                    guarded([producer_test, "--ignored", "--exact",
                             "discord::worker::tests::installed_claude_producer", "--nocapture"],
                            root, [model.server_port]),
                    env=env | dict(SWARM_CLAUDE_WORKER_STATE=str(state), SWARM_CLAUDE_WORKER_TARGET=sid,
                                   SWARM_CLAUDE_WORKER_MESSAGE=MESSAGE) | ({"SWARM_CLAUDE_WORKER_REVOKED": "1"} if guild == "revoked" else {}),
                    text=True, capture_output=True, timeout=15)
                assert witness.returncode == 0, (witness.stdout, witness.stderr)
                operation = next(row.split()[1] for row in witness.stdout.splitlines()
                                 if row.startswith("CLAUDE_WORKER_OPERATION "))
                persisted = json.loads(ledger_path.read_text())
                cutover = persisted["worker_inputs"][operation]
                assert cutover["provider"] == "claude"
                origin = cutover["composer"]["origin"]
                assert origin["actor_id"] == "123" and origin["requester_id"] == "123"
                if guild:
                    assert origin["guild_id"] == "301" and origin["channel_id"] == "456"
            time.sleep(0.4)
            assert "Do you want to proceed?" in run("peek", sid)
            assert not model.sentinel.exists()
            assert all(MESSAGE not in req for req in model.observed)
            run("send", sid, "--key", "down")
            run("send", sid, "--key", "enter")
            if guild == "revoked":
                until(lambda: "Do you want to proceed?" not in run("peek", sid), "local denial closed the permission prompt")
                marker = "LOCAL_OPERATOR_AFTER_GUILD_REVOCATION"
                run("send", sid, "--text", marker)
                until(lambda: any(marker in request for request in model.observed), "next actual model turn after revocation")
            else:
                until(lambda: any(MESSAGE in request for request in model.observed), "native message context")
            assert not model.sentinel.exists()
            receipt = json.loads(run("message", sid, "--status", "--operation-id", operation))
            assert receipt["state"] == ("rejected" if guild == "revoked" else "submitted") and receipt["origin"] == origin
            assert receipt["agent"] == "claude" and receipt["native_session_id"]
            run("send", sid, "--text", "/clear")
            until(lambda: not target(), "native readiness revoked on /clear")
            assert json.loads(run("message", sid, "--status", "--operation-id", operation)) == receipt
            assert sum(MESSAGE in request for request in model.observed) == (0 if guild == "revoked" else 1)
            print(json.dumps(dict(version=version, isolated_synthetic_inference=True,
                                  permission_dialog_preserved=True, native_delivery=guild != "revoked",
                                  guild_audience=guild,
                                  exact_replay=True, missing_cutover_refused=True,
                                  revoked_on_clear=True, production_worker_producer=producer_test is not None)))
        except Exception:
            output.flush()
            output.seek(0)
            print(output.read(), file=sys.stderr)
            if sid:
                print(run("peek", sid, ok=False), file=sys.stderr)
            raise
        finally:
            try:
                if sid and daemon.poll() is None:
                    run("kill", sid, ok=False)
                    until(lambda: any(row["id"] == sid and row["status"]["Process"] == "exited"
                                      for row in json.loads(run("ls", "--json").strip())),
                          "owned shim exit", seconds=8)
            finally:
                if daemon.poll() is None:
                    daemon.terminate()
                    daemon.wait(timeout=5)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("swarm", type=Path)
    parser.add_argument("claude", type=Path)
    parser.add_argument("producer", type=Path, nargs="?")
    parser.add_argument("--guild", choices=("accepted", "revoked"))
    args = parser.parse_args()
    if args.guild and not args.producer:
        parser.error("guild tests require the native worker producer test executable")
    producer_test = str(args.producer.resolve()) if args.producer else None
    with tempfile.TemporaryDirectory(prefix="clwi-", dir="/tmp") as tmp, \
            http.server.ThreadingHTTPServer(("127.0.0.1", 0), Provider) as server:
        root = Path(tmp).resolve()
        server.observed = []
        server.sentinel = root / "must-not-run"
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            check_guards(root, [server.server_port])
            verify(str(args.swarm.resolve()), args.claude.resolve(), root, server, producer_test, args.guild)
        finally:
            server.shutdown()
