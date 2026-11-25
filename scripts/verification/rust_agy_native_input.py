#!/usr/bin/env python3
"""Exercise native registration and delivery through Rust spawn, shim and message."""
import json
import os
from pathlib import Path
import secrets
import socket
import struct
import subprocess
import sys
import tempfile
import time

from parity_agy import wait_for

CONVERSATION = "11111111-1111-4111-8111-111111111111"
FIXTURE = r'''
import json, os, re, subprocess, sys, time
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
        result = {"conversationMetadata": {"metadata": {
            "rootConversationId": conversation, "projectId": "fixture-project",
        }}}
    else:
        assert args[1] == "send-message"
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


def run(binary):
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
            "version": 1, "enabled": True, "mode": "personal",
            "installation_id": "fixture-only", "name": "Local fixture",
            "application_id": "234", "owner_id": "123", "dm_channel_id": "456",
            "token_file": str(credentials), "daemon_socket": str(daemon_socket),
            "work_dir": str(work), "manager_cli": "codex",
        }))
        config.chmod(0o600)
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

        def request(body, caps):
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
                    return exchange(peer, body | {"endpoint_id":hello["endpoint_id"]})

        def send(text, operation=None, *, origin=None, allow_origin=True, ok=True):
            origin = ({
                "kind":"external", "installation_id":"fixture-only",
                "request_id":"fixture-request", "actor_id":"456",
            } if origin is None else origin)
            instance = request({"op":"semantic_target","session_id":sid},
                               ["semantic-target"])["semantic_target_instance"]
            operation = operation or f"semantic:{time.time_ns() // 1_000_000}:{secrets.token_hex(16)}"
            capabilities = ["semantic-send"] + (["message-origin"] if allow_origin else [])
            response = request({"op":"semantic_send","session_id":sid,"operation_id":operation,
                                "body_version":1,"composer_send":{
                                    "session":sid,"session_instance":instance,"text":text,"origin":origin,
                                }}, capabilities)
            if ok:
                assert response["op"] == "ok", response
            else:
                assert response["op"] == "error", response
            return operation

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
            send(text, origin={"kind":"external","installation_id":"other",
                               "request_id":"fixture-request","actor_id":"456"}, ok=False)
            send(text, allow_origin=False, ok=False)
            assert deliveries() == []
            operation = send(text)
            def status(op):
                return json.loads(cli("message", sid, "--status", "--operation-id", op).stdout)

            first = status(operation)
            assert first["state"] == "submitted" and first["native_session_id"] == CONVERSATION
            assert first["agent"] == "agy" and first["native_generation"]
            assert first["origin"]["request_id"] == "fixture-request"
            send(text, operation)
            send("conflicting text", operation, ok=False)
            send(text, operation, origin=first["origin"] | {"request_id":"different"}, ok=False)
            saved_config = config.read_text()
            config.write_text(json.dumps(json.loads(saved_config) | {"installation_id":"replacement"}))
            try:
                send(text, origin=first["origin"] | {"installation_id":"replacement"}, ok=False)
                assert status(operation) == first
            finally:
                config.write_text(saved_config)
            assert deliveries() == [text]

            daemon.kill()
            daemon.wait(timeout=5)
            daemon = start()
            assert status(operation) == first
            send(text, operation)
            ambiguous = send("uncertain native reply", ok=False)
            assert status(ambiguous)["state"] == "outcome_unknown"
            send("uncertain native reply", ambiguous, ok=False)
            send("blocked by earlier operation", ok=False)
            assert deliveries() == [text, "uncertain native reply"]
            assert not (work / "unexpected-terminal-input").exists()
        finally:
            if sid:
                cli("kill", sid, ok=False)
            daemon.terminate()
            try:
                daemon.wait(timeout=5)
            except subprocess.TimeoutExpired:
                daemon.kill()
                daemon.wait()


if __name__ == "__main__":
    run(Path(sys.argv[1]).resolve(strict=True))
    print("agy registration, exact native delivery, daemon recovery and ambiguous non-replay passed.")
