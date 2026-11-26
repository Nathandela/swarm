#!/usr/bin/env python3
"""Qualify an installed Claude/Codex hands-off launch on a disposable checkout."""
import argparse
import http.server
import io
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


def text_parts(content):
    if isinstance(content, str):
        return [content]
    return [part["text"] for part in content if "text" in part]


class ClaudeModel(Provider):
    def do_POST(self):
        body = self.rfile.read(int(self.headers["Content-Length"]))
        for message in json.loads(body).get("messages", []):
            if message.get("role") == "user":
                self.server.prompts.extend(text_parts(message.get("content", [])))
        stream, self.rfile = self.rfile, io.BytesIO(body)
        try:
            super().do_POST()
        finally:
            self.rfile = stream


def verify(root, candidate, claude, codex, anthropic, source_agent):
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
    codex_prompts = []
    target_agent = "codex" if source_agent == "claude" else "claude"
    prompts = {"codex": codex_prompts, "claude": anthropic.prompts}
    received = prompts[target_agent]
    responses = {"codex": b"Synthetic Codex answer.", "claude": b"Synthetic native input processed"}

    def respond(items):
        codex_prompts.extend(part for item in items if item.get("role") == "user"
                             for part in text_parts(item.get("content", [])))
        return responses["codex"].decode()

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
            sid = invoke("spawn", "--cli", source_agent, "--name", "CrossSource",
                         "--dir", str(work), "--prompt", SOURCE).decode().strip()
            local = sid.split("/")[-1]
            source_dir = root / "state/rust-v1" / local
            observation = source_dir / "observation.json"

            def replied(sid, agent):
                try:
                    screen = invoke("peek", sid)
                except subprocess.CalledProcessError as error:
                    row = next(row for row in rows() if row["id"] == sid)
                    if row["status"]["Process"] == "exited":
                        raise RuntimeError(error.stderr.decode(errors="replace")) from error
                    return False
                (root / f"{agent}-screen.txt").write_bytes(screen)
                if b"Do you want to use this API key?" in screen and b"No (recommended)" in screen:
                    invoke("send", sid, "--key", "up")
                    invoke("send", sid, "--key", "enter")
                if b"I am using this for local development" in screen:
                    invoke("send", sid, "--key", "down")
                    invoke("send", sid, "--key", "enter")
                if b"trust the files" in screen and b"trust this folder" in screen:
                    invoke("send", sid, "--key", "enter")
                return responses[agent] in screen

            fixtures.wait_for(lambda: replied(sid, source_agent) and observation.exists(),
                              "authenticated native source turn", timeout=25)
            native = json.loads(observation.read_text())["conversation_id"]

            def native_transcript(agent, cid, marker):
                directory = home / (".claude/projects" if agent == "claude" else ".codex/sessions")
                prefix = "" if agent == "claude" else "*-"
                return next((path for path in directory.rglob(f"{prefix}{cid}.jsonl")
                             if marker.encode() in path.read_bytes()), None)

            transcript = fixtures.wait_for(lambda: native_transcript(source_agent, native, SOURCE),
                                           "native source persisted the original turn")

            def user_turns():
                return [row for row in map(json.loads, transcript.read_text().splitlines())
                        if row["type"] == "user" or
                        (row["type"] == "event_msg" and row["payload"]["type"] == "user_message")]

            baseline = user_turns()
            original_owner = (source_dir / "owner.json").read_bytes()
            request = dict(agent=target_agent, cwd=str(root), initial_prompt="CLIENT_FORGED_PROMPT",
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
            if selected != target_agent:
                board.send(b"\x1b[C" if target_agent == "codex" else b"\x1b[D")
                board.wait_bytes(f"● {target_agent}".encode())
            latest = board.capture[board.capture.rfind(b"\x1b[H\x1b[2J"):]
            if "◂ supervised ▸".encode() in latest:
                board.send(b"\t\t\t\x1b[C")
            board.wait_bytes("◂ hands-off ▸".encode())
            board.send(b"\r")
            disclosure = f"Give the {target_agent} successor access to the {source_agent} source conversation?"
            board.wait_bytes(disclosure.encode())
            assert [row["id"] for row in rows()] == [sid] and not received
            board.send(b"n")
            offset = len(board.capture)
            board.wait_bytes("◂ hands-off ▸".encode(), offset)
            assert [row["id"] for row in rows()] == [sid] and not received
            offset = len(board.capture)
            board.send(b"\r")
            board.wait_bytes(disclosure.encode(), offset)
            board.send(b"y")
            child = fixtures.wait_for(lambda: next((row for row in rows()
                                      if row.get("spawned_from") == local), None),
                                      "confirmed native successor", timeout=20)
            assert child["agent"] == target_agent and child["cwd"] == str(work), child
            assert child["spawn_intent"] == "handoff" and not child.get("supervision"), child

            def pointer():
                replied(child["id"], target_agent)
                return next((text for text in received if f"conversation uid: {native}" in text), None)

            prompt = fixtures.wait_for(pointer, "native destination loopback opening prompt", timeout=25)
            pointers = "\n".join(("<pointers>", f"conversation uid: {native}",
                                  f"transcript file: {transcript}", f"working directory: {work}",
                                  f"source agent: {source_agent}", f"source swarm session: {local}", "</pointers>"))
            assert pointers in prompt
            fixtures.wait_for(lambda: replied(child["id"], target_agent),
                              "native destination displayed the synthetic answer", timeout=15)
            child_dir = root / "state/rust-v1" / child["id"].split("/")[-1]
            if target_agent == "claude":
                observed = child_dir / "observation.json"
                def destination_project():
                    if not observed.exists():
                        return False
                    cid = json.loads(observed.read_text())["conversation_id"]
                    assert cid != native
                    path = native_transcript("claude", cid, f"conversation uid: {native}")
                    if path is None:
                        return False
                    return any(row.get("cwd") == str(work) and row.get("sessionId") == cid
                               and row["type"] == "user"
                               and any(pointers in text for text in text_parts(row["message"]["content"]))
                               for row in map(json.loads, path.read_text().splitlines()))

                fixtures.wait_for(destination_project, "native Claude wrote the pointer in the source project")
            assert user_turns() == baseline
            assert (source_dir / "owner.json").read_bytes() == original_owner
            assert next(row for row in rows() if row["id"] == sid)["status"]["Process"] == "running"
            invoke("send", sid, "--text", FOLLOWUP)
            fixtures.wait_for(lambda: FOLLOWUP.encode() in transcript.read_bytes()
                              and any(FOLLOWUP in text for text in prompts[source_agent]),
                              "original provider continued in its unchanged transcript")
            private_text = (SOURCE, responses[source_agent].decode())
            assert not any(original in text for text in received for original in private_text)
            assert len(rows()) == 2
            origin = json.loads((source_dir / "session.json").read_text())["provider"]
            target = json.loads((child_dir / "session.json").read_text())["provider"]
            print(f"{source_agent} {origin['version']} → {target_agent} {target['version']}: "
                  "board cancellation and "
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
    parser.add_argument("--source", choices=("claude", "codex"), default="claude")
    args = parser.parse_args()
    root = Path(tempfile.mkdtemp(prefix="ch-", dir=Path("/tmp").resolve()))
    model = http.server.ThreadingHTTPServer(("127.0.0.1", 0), ClaudeModel)
    model.daemon_threads = True
    model.observed = []
    model.prompts = []
    model.sentinel = root / "must-not-exist"
    threading.Thread(target=model.serve_forever, daemon=True).start()
    success = False
    try:
        verify(root, args.candidate, args.claude, args.codex, model, args.source)
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
