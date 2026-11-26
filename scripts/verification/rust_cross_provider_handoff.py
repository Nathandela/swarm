#!/usr/bin/env python3
"""Qualify an installed Claude-to-Codex hands-off launch on a disposable checkout."""
import argparse
import http.server
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import tempfile
import threading

sys.dont_write_bytecode = True
from native_fixture_sandbox import check_guards, guarded
from parity_codex import model_fixture
from parity_runtime import control, fixtures, read_control
from rust_claude_worker_input import Provider, save

SOURCE = "PRIVATE_SOURCE_TURN_FIXTURE_MARKER"
FOLLOWUP = "PRIVATE_SOURCE_FOLLOWUP_FIXTURE_MARKER"


def verify(root, candidate, claude, codex, anthropic):
    for name in ("bin", "home", "home/.claude", "home/.codex", "runtime", "tmp", "work", "state"):
        (root / name).mkdir(mode=0o700)
    source = Path(__file__).resolve().parent
    for name, path in (("swarm", candidate), ("claude", claude), ("codex", codex),
                       ("driver", source / "parity/swarm-parity")):
        dest = root / "bin" / name
        shutil.copyfile(path, dest)
        dest.chmod(0o700)
    work = root / "work"
    home = root / "home"
    env = fixtures.environment(root)
    env.pop("CLAUDE_CONFIG_DIR")
    env["ANTHROPIC_API_KEY"] = "synthetic-resume"
    received = []

    def respond(items):
        received.extend(item for item in items if item.get("role") == "user")
        return "Synthetic destination answer."

    openai, _ = model_fixture(root, False, answer_for_input=respond)
    ports = [anthropic.server_port, openai.server_port]
    command = guarded([root / "bin/swarm"], root, ports)
    mac_guard = None
    if sys.platform == "darwin":
        from macos_codex_fixture import Guard, RUST_ID

        endpoint = root / "state/rust-v1" / RUST_ID / "b"
        mac_guard = Guard(root, root / "state", ports[0], False,
                          endpoints=[endpoint], extra_ports=ports[1:])
        command = [mac_guard.wrap(root / "bin/swarm", "guarded-swarm")]
        env["SWARM_FIXTURE_CODEX_ID"] = RUST_ID

    def invoke(*args):
        result = subprocess.run([*command, *args], env=env, cwd=work,
                                capture_output=True, timeout=10, check=True)
        return result.stdout

    def launch(request, caps=()):
        with socket.socket(socket.AF_UNIX) as peer:
            peer.settimeout(12)
            peer.connect(env["SWARM_DAEMON_SOCK"])
            control(peer, "hello", endpoint_id="", protocol_version=1, capabilities=caps)
            hello = read_control(peer)
            assert all(cap in hello["capabilities"] for cap in caps), hello
            control(peer, "launch", endpoint_id=hello["endpoint_id"], launch=request)
            return read_control(peer)

    def rows():
        return json.loads(invoke("ls", "--json"))

    daemon = board = None
    try:
        check_guards(root, ports)
        version = subprocess.check_output(guarded([root / "bin/claude", "--version"], root, ports),
                                          env=env, text=True).split()[0]
        save(home / ".claude.json", dict(hasCompletedOnboarding=True, lastOnboardingVersion=version,
             theme="dark", projects={str(work): dict(hasTrustDialogAccepted=True)},
             customApiKeyResponses=dict(approved=[env["ANTHROPIC_API_KEY"]], rejected=[])))
        save(home / ".claude/settings.json", dict(env=dict(
            ANTHROPIC_BASE_URL=f"http://127.0.0.1:{anthropic.server_port}",
            CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC="1", DISABLE_AUTOUPDATER="1",
            CLAUDE_CODE_TMPDIR=str(root), CLAUDE_CODE_MAX_RETRIES="0")))
        with (root / "daemon-output.log").open("wb") as log:
            daemon = subprocess.Popen([*command, "daemon"], env=env, cwd=work, stdout=log, stderr=log)
            fixtures.wait_for(lambda: Path(env["SWARM_DAEMON_SOCK"]).exists(), "fixture owner")
            sid = invoke("spawn", "--cli", "claude", "--name", "CrossSource",
                         "--dir", str(work), "--prompt", SOURCE).decode().strip()
            local = sid.split("/")[-1]
            source_dir = root / "state/rust-v1" / local
            observation = source_dir / "observation.json"

            def source_replied():
                screen = invoke("peek", sid)
                (root / "source-screen.txt").write_bytes(screen)
                if b"Do you want to use this API key?" in screen and b"No (recommended)" in screen:
                    invoke("send", sid, "--key", "up")
                    invoke("send", sid, "--key", "enter")
                if b"I am using this for local development" in screen:
                    invoke("send", sid, "--key", "down")
                    invoke("send", sid, "--key", "enter")
                if b"trust the files" in screen and b"trust this folder" in screen:
                    invoke("send", sid, "--key", "enter")
                return b"Synthetic native input processed" in screen and observation.exists()

            fixtures.wait_for(source_replied, "authenticated native Claude source turn", timeout=25)
            native = json.loads(observation.read_text())["conversation_id"]
            def source_transcript():
                return next((path for path in (home / ".claude/projects").rglob(f"{native}.jsonl")
                             if SOURCE.encode() in path.read_bytes()), None)

            transcript = fixtures.wait_for(source_transcript, "native Claude persisted the original turn")
            def user_turns():
                return [row for row in map(json.loads, transcript.read_text().splitlines())
                        if row["type"] == "user"]

            baseline = user_turns()
            original_owner = (source_dir / "owner.json").read_bytes()
            request = dict(agent="codex", cwd=str(root), initial_prompt="CLIENT_FORGED_PROMPT",
                           options={"handoff_from": sid})
            caps = ("hands-off-handoff",)
            assert launch(request)["op"] == "error"
            lost = launch(request | {"options": {"handoff_from": sid + "missing"}}, caps)
            assert lost["op"] == "error", lost
            backup = transcript.with_suffix(".parked")
            transcript.rename(backup)
            try:
                transcript.symlink_to(backup)
                refused = launch(request, caps)
                assert refused["op"] == "error", refused
            finally:
                transcript.unlink()
                backup.rename(transcript)
            moved = work.with_name("moved")
            work.rename(moved)
            try:
                work.mkdir()
                refused = launch(request, caps)
                assert refused["op"] == "error" and "checkout identity changed" in refused["error"], refused
            finally:
                work.rmdir()
                moved.rename(work)
            assert [row["id"] for row in rows()] == [sid] and not received

            board = fixtures.Board(command, root, env)
            board.resize(118, 30)
            board.wait_bytes(b"CrossSource")
            board.send(b"h")

            def detected():
                board.drain()
                return next((agent for agent in ("claude", "codex")
                             if f"● {agent}".encode() in board.capture), None)

            selected = fixtures.wait_for(detected, "installed handoff targets", timeout=25)
            if selected == "claude":
                board.send(b"\x1b[C")
                board.wait_bytes("● codex".encode())
            latest = board.capture[board.capture.rfind(b"\x1b[H\x1b[2J"):]
            if "◂ supervised ▸".encode() in latest:
                board.send(b"\t\t\t\x1b[C")
            board.wait_bytes("◂ hands-off ▸".encode())
            board.send(b"\r")
            board.wait_bytes(b"Give the codex successor access to the claude source conversation?")
            assert [row["id"] for row in rows()] == [sid] and not received
            board.send(b"n")
            offset = len(board.capture)
            board.wait_bytes("◂ hands-off ▸".encode(), offset)
            assert [row["id"] for row in rows()] == [sid] and not received
            offset = len(board.capture)
            board.send(b"\r")
            board.wait_bytes(b"Give the codex successor access", offset)
            board.send(b"y")
            child = fixtures.wait_for(lambda: next((row for row in rows()
                                      if row.get("spawned_from") == local), None),
                                      "confirmed Codex successor", timeout=20)
            assert child["agent"] == "codex" and child["cwd"] == str(work), child
            assert child["spawn_intent"] == "handoff" and not child.get("supervision"), child

            def pointer():
                return next((part["text"] for item in received for part in item.get("content", [])
                             if f"conversation uid: {native}" in part.get("text", "")), None)

            prompt = fixtures.wait_for(pointer, "native Codex loopback opening prompt", timeout=25)
            pointers = "\n".join(("<pointers>", f"conversation uid: {native}",
                                  f"transcript file: {transcript}", f"working directory: {work}",
                                  "source agent: claude", f"source swarm session: {local}", "</pointers>"))
            assert pointers in prompt
            assert all(SOURCE not in part.get("text", "") for item in received
                       for part in item.get("content", []))
            fixtures.wait_for(lambda: b"Synthetic destination answer." in invoke("peek", child["id"]),
                              "native Codex displayed the synthetic answer", timeout=15)
            assert user_turns() == baseline
            assert (source_dir / "owner.json").read_bytes() == original_owner
            assert next(row for row in rows() if row["id"] == sid)["status"]["Process"] == "running"
            invoke("send", sid, "--text", FOLLOWUP)
            fixtures.wait_for(lambda: FOLLOWUP.encode() in transcript.read_bytes(),
                              "original Claude continued in its unchanged transcript")
            assert len(rows()) == 2
            child_dir = root / "state/rust-v1" / child["id"].split("/")[-1]
            target = json.loads((child_dir / "session.json").read_text())["provider"]
            print(f"Claude {version} → Codex {target['version']}: board cancellation and "
                  "confirmation, native pointer-only prompt, refusal and original continuation passed")
            for row in rows():
                invoke("kill", row["id"])
    except Exception:
        if board:
            print(repr(bytes(board.capture[-2400:])), file=sys.stderr)
        raise
    finally:
        try:
            if board:
                os.close(board.master)
                os.close(board.slave)
                if board.child.poll() is None:
                    board.child.kill()
                board.child.wait(timeout=10)
        finally:
            if daemon and daemon.poll() is None:
                daemon.terminate()
                daemon.wait(timeout=10)
            openai.shutdown()
            openai.server_close()
            if mac_guard:
                cleanup = fixtures.reap(root, None, {})
                assert not cleanup["remaining"], cleanup
                mac_guard.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("candidate", type=fixtures.binary_path)
    parser.add_argument("claude", type=fixtures.binary_path)
    parser.add_argument("codex", type=fixtures.binary_path)
    args = parser.parse_args()
    root = Path(tempfile.mkdtemp(prefix="ch-", dir=Path("/tmp").resolve()))
    model = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Provider)
    model.daemon_threads = True
    model.observed = []
    model.sentinel = root / "must-not-exist"
    threading.Thread(target=model.serve_forever, daemon=True).start()
    success = False
    try:
        verify(root, args.candidate, args.claude, args.codex, model)
        assert not model.sentinel.exists()
        success = True
    finally:
        model.shutdown()
        model.server_close()
        cleanup = fixtures.reap(root, None, {})
        print("cleanup", cleanup)
        assert not cleanup["remaining"], cleanup
        if success:
            shutil.rmtree(root)
        else:
            print(f"retained private fixture at {root}", file=sys.stderr)
