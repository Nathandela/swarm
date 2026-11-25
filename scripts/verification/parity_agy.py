#!/usr/bin/env python3
"""Drive agy fixtures through the real supervisor, CLI and terminal dashboard."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import pty
import select
import socket
import struct
import subprocess
import sys
import tempfile
import termios
import time

CONVERSATION = "11111111-1111-4111-8111-111111111111"
FIXTURE = r'''
import json, os, socket, struct, subprocess, sys, time
from pathlib import Path

args = sys.argv[1:]
if args == ["--version"]:
    print("$VERSION")
    raise SystemExit(0)
if args == ["--help"]:
    if $SETTINGS:
        print("  --settings         Inline JSON settings", file=sys.stderr)
    raise SystemExit(0)
assert os.isatty(0) and os.isatty(1)
Path(f"argv-{os.getpid()}").write_text(json.dumps(args))
Path("fixture-api-keys").write_text(json.dumps([
    key for key in ("ANTHROPIC_API_KEY", "OPENAI_API_KEY") if key in os.environ
]))
if args[:1] == ["--settings"]:
    setting = json.loads(args[1])["statusLine"]
    def status(state, conversation, title):
        payload = {"agent_state": state, "conversation_id": conversation, "conversation_title": title}
        subprocess.run(setting["command"], shell=True, input=json.dumps(payload).encode(), check=True)
    if hook_socket := os.environ.get("SWARM_RUST_HOOK_SOCKET"):
        def frame(conversation, state, title, stamp):
            body = b"" + json.dumps({
                "type":"hook", "event":"AgyStatusLine", "token":os.environ["SWARM_HOOK_TOKEN"],
                "payload":{"conversation_id":conversation, "agent_state":state,
                           "conversation_title":title, "observed_ns":stamp},
            }).encode()
            return struct.pack(">I",len(body)) + body

        stamp = time.time_ns()
        first = frame("$CONVERSATION", "reviewing", "Provider title", stamp)
        later = frame("22222222-2222-4222-8222-222222222222", "working", "Overtook first frame", stamp + 1)
        with socket.socket(socket.AF_UNIX) as a, socket.socket(socket.AF_UNIX) as b:
            for peer in (a, b):
                peer.settimeout(2)
                peer.connect(hook_socket)
            a.sendall(first[:5])
            b.sendall(later)
            time.sleep(.15)
            a.sendall(first[5:])
            with a.makefile("rb") as probe:
                reply = probe.read(struct.unpack(">I",probe.read(4))[0])
                assert reply[:1] == b"" and json.loads(reply[1:])["type"] == "ok"
            assert b.recv(1) == b"", "later callback won before the acknowledged first frame"

    status("reviewing", "$CONVERSATION", "Provider title")
    if os.environ.get("SWARM_RUST_HOOK_SOCKET"):
        status("working", "22222222-2222-4222-8222-222222222222", "Wrong conversation")
        status("idle", "malformed-id", "Malformed conversation")
        status("error", None, "Wrong JSON type")
        with socket.socket(socket.AF_UNIX) as peer:
            peer.settimeout(2)
            peer.connect(os.environ["SWARM_RUST_HOOK_SOCKET"])
            payload = b"\x01" + json.dumps({
                "type":"hook", "event":"AgyStatusLine", "token":os.environ["SWARM_HOOK_TOKEN"],
                "payload":{"conversation_id":False, "agent_state":"working",
                           "conversation_title":"Unvalidated shim frame", "observed_ns":time.time_ns()},
            }).encode()
            peer.sendall(struct.pack(">I",len(payload)) + payload)
            assert peer.recv(1) == b"", "shim acknowledged malformed conversation"
        Path("negative-probes-complete").touch()
print("\033[2J\033[HAGY READY\n>\n-----\n", flush=True)
for line in sys.stdin:
    if "finish" in line:
        print("Resume with -c:\nagy --conversation=$CONVERSATION\n", flush=True)
        raise SystemExit(0)
    print(f"fixture received {line.strip()}", flush=True)
'''


def wait_for(check, description, timeout=8):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if result := check():
            return result
        time.sleep(.05)
    raise AssertionError(f"timed out waiting for {description}")


def run_fixture(binary, version, *, settings=False, rust=False):
    with tempfile.TemporaryDirectory(prefix="agy-", dir=Path("/tmp").resolve()) as tmp:
        root = Path(tmp)
        daemon_socket = root / "s" / "d"
        daemon_socket.parent.mkdir(mode=0o700)
        home, work, tools = (root / key for key in ("home", "work", "bin"))
        for path in (home, work, tools):
            path.mkdir()
        cli_settings = home / ".gemini/antigravity-cli/settings.json"
        cli_settings.parent.mkdir(parents=True)
        cli_settings.write_text(json.dumps({"statusLine": {
            "type": "command",
            "command": "env | sed -n '/^SWARM_[^=]*=/s/=.*//p' > saved-command-env; tee original-status.json | wc -c",
            "enabled": True, "stack_with_default": False, "padding": 2,
        }}))
        provider = tools / "agy"
        provider.write_text(f"#!{sys.executable}\n" + FIXTURE.replace(
            "$CONVERSATION", CONVERSATION).replace("$VERSION", version).replace("$SETTINGS", str(settings)))
        provider.chmod(0o700)
        env = {
            "HOME": str(home), "PATH": f"{tools}:/usr/bin:/bin", "TERM": "xterm-256color",
            "SHELL": "/bin/sh", "TMPDIR": str(root),
            "ANTHROPIC_API_KEY": "synthetic-anthropic-secret", "OPENAI_API_KEY": "synthetic-openai-secret",
            "SWARM_DAEMON_STATE": str(daemon_socket.parent),
            "SWARM_DAEMON_SOCK": str(daemon_socket), "SWARM_DAEMON_REMOTE_SOCK": "",
            "SWARM_DAEMON_LOCK": str(root / "lock"), "SWARM_DAEMON_LOG": str(root / "log"),
        }

        def cli(*args, ok=True):
            result = subprocess.run([binary, *args], cwd=work, env=env, stdin=subprocess.DEVNULL,
                                    capture_output=True, timeout=10)
            if ok:
                assert result.returncode == 0, (args, result.stderr.decode())
            return result

        def roster():
            result = cli("ls", "--json", ok=False)
            return json.loads(result.stdout) if result.returncode == 0 else None

        def row():
            return next((row for row in roster() or [] if row["id"] == sid), {})

        def request(message):
            def exchange(peer, value):
                payload = b"\x01" + json.dumps(value).encode()
                peer.write(struct.pack(">I", len(payload)) + payload)
                peer.flush()
                response = peer.read(struct.unpack(">I", peer.read(4))[0])
                assert response[:1] == b"\x01"
                return json.loads(response[1:])

            with socket.socket(socket.AF_UNIX) as conn:
                conn.settimeout(8)
                conn.connect(str(daemon_socket))
                with conn.makefile("rwb") as peer:
                    hello = exchange(peer, {"op": "hello", "protocol_version": 1})
                    return exchange(peer, dict(message, endpoint_id=hello["endpoint_id"]))

        daemon = subprocess.Popen([binary, "daemon"], cwd=work, env=env,
                                  stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        active = []
        try:
            wait_for(lambda: daemon_socket.exists(), "daemon socket")
            sid = cli("spawn", "--cli", "agy", "--name", "fixture agy", "--dir", work,
                      "--model", "Synthetic model", "--prompt", "ordinary launch").stdout.decode().strip()
            active.append(sid)
            expected = {
                "Process": "running", "Turn": "idle",
                "Interaction": "permission" if settings else "none",
            }
            wait_for(lambda: row().get("status") == expected, f"agy status {expected}")
            assert row()["agent"] == "agy"
            if rust:
                assert json.loads((work / "fixture-api-keys").read_text()) == []
            argv = json.loads(next(work.glob("argv-*")).read_text())
            if settings:
                assert argv[0] == "--settings"
                wrapper = json.loads(argv[1])["statusLine"]
                assert wrapper["padding"] == 2 and wrapper["stack_with_default"] is False
                assert "hook AgyStatusLine" in wrapper["command"]
                argv = argv[2:]
                wait_for(lambda: (work / "original-status.json").exists(), "preserved status command")
                wait_for(lambda: row().get("name") == "Provider title", "owned title")
                if rust:
                    wait_for(lambda: (work / "negative-probes-complete").exists(), "hook refusal controls")
                    assert (work / "saved-command-env").read_bytes() == b""
                    assert row()["status"] == expected and row()["name"] == "Provider title"
            else:
                assert not (work / "original-status.json").exists()
            assert argv == ["--model", "Synthetic model", "--prompt-interactive", "ordinary launch"], argv
            assert b"AGY READY" in cli("peek", sid).stdout
            attempt = cli("message", sid, "--text", "must never reach the PTY", ok=False)
            assert attempt.returncode != 0
            assert b"must never reach the PTY" not in cli("peek", sid).stdout

            resume = {"op": "launch", "launch": {"agent": "agy", "cwd": str(work), "cols": 100, "rows": 24,
                      "options": {"resume_from": sid}, "env": [f"{k}={v}" for k, v in env.items()]}}
            assert request(resume)["op"] == "error", "accepted a live source"
            cli("send", sid, "--text", "finish")
            wait_for(lambda: row().get("status", {}).get("Process") == "exited", "native exit")
            if not rust:
                resumed = request(resume)
                assert resumed["op"] == "launch", resumed
                active.append(resumed["session"]["id"])
            else:
                master, slave = pty.openpty()
                fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 32, 124, 0, 0))
                board = subprocess.Popen([binary], cwd=work, env=env, stdin=slave,
                                         stdout=slave, stderr=slave)
                os.close(slave)
                capture = bytearray()

                def board_ready():
                    if select.select([master], [], [], .05)[0]:
                        capture.extend(os.read(master, 8192))
                    return b"fixture agy" in capture or b"Provider title" in capture

                try:
                    wait_for(board_ready, "dashboard roster")
                    os.write(master, b"r")
                    resumed = wait_for(lambda: next((r for r in roster() or []
                                                     if r.get("resumed_from") == sid), None), "dashboard resume")
                    active.append(resumed["id"])
                finally:
                    os.close(master)
                    board.terminate()
                    board.wait(timeout=5)

            def resumed_argv():
                for path in work.glob("argv-*"):
                    args = json.loads(path.read_text())
                    if "--conversation" in args:
                        return args
                return None

            args = wait_for(resumed_argv, "resumed agy argv")
            assert args[-4:] == ["--model", "Synthetic model", "--conversation", CONVERSATION], args
        finally:
            for sid in set(active) | {row["id"] for row in roster() or []}:
                cli("kill", sid, ok=False)
            daemon.terminate()
            try:
                daemon.wait(timeout=5)
            except subprocess.TimeoutExpired:
                daemon.kill()
                daemon.wait()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("reference", type=Path)
    parser.add_argument("candidate", type=Path)
    args = parser.parse_args()
    reference, candidate = (path.resolve(strict=True) for path in (args.reference, args.candidate))
    run_fixture(reference, "1.3.9")
    run_fixture(candidate, "1.3.9", rust=True)
    run_fixture(candidate, "1.4.7", rust=True)
    run_fixture(reference, "1.4.7", settings=True)
    run_fixture(candidate, "1.4.7", settings=True, rust=True)
    print("agy classic launch, owned title/status, semantic refusal and dashboard resume passed.")


if __name__ == "__main__":
    main()
