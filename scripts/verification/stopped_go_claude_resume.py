#!/usr/bin/env python3
"""Run live Claude turns in one transcript through stopped Go, Rust and Go."""
import argparse
import contextlib
import http.server
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import threading

from native_fixture_sandbox import guarded, check_guards
from rust_claude_worker_input import Provider, save
from stopped_go_state import await_value, disk, fixture_env, listed, owner

KEY = "stopped-fixture-key"
PROMPTS = ("Original Go Claude turn", "Resumed Rust Claude turn", "Restored Go Claude turn")
RESPONSE = b"Synthetic native input processed"


class Model(Provider):
    def do_POST(self):
        raw = self.rfile.read(int(self.headers["Content-Length"]))
        request = json.loads(raw)
        if request.get("stream"):
            assert self.headers["x-api-key"] == KEY
            messages = json.dumps(request.get("messages"))
            seen = tuple(prompt for prompt in PROMPTS if prompt in messages)
            if seen:
                self.server.turns.add(seen)
            self.server.observed.append(seen)
        stream, self.rfile = self.rfile, io.BytesIO(raw)
        try:
            super().do_POST()
        finally:
            self.rfile = stream


def verify(go, rust, installed, root):
    home, work, state, tools = (root / part for part in ("home", "work", "s", "bin"))
    for directory in (home, work, state, tools, home / ".claude"):
        directory.mkdir(mode=0o700)
    go_bin, rust_bin, claude = (tools / name for name in ("swarm", "swarm-rust", "claude"))
    for executable, staged in ((go, go_bin), (rust, rust_bin), (installed, claude)):
        shutil.copyfile(executable, staged)
        staged.chmod(0o700)
    model = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Model)
    model.turns = set()
    model.observed = []
    server = threading.Thread(target=model.serve_forever, daemon=True)
    server.start()
    go_env = fixture_env(root, state, tools) | {
        "TERM": "xterm-256color", "LANG": "en_US.UTF-8",
        "ANTHROPIC_API_KEY": KEY, "TMPDIR": str(root),
    }
    rust_env = go_env | {"SWARM_DAEMON_STOPPED_GO_COMPAT": "1"}
    guard = lambda argv: guarded(argv, root, [model.server_port])

    def run(binary, env, *args):
        return subprocess.run(guard([binary, *args]), cwd=work, env=env, capture_output=True, timeout=25)

    def cli(binary, env, *args):
        output = run(binary, env, *args)
        assert output.returncode == 0, (args, output.stdout, output.stderr)
        return output.stdout

    @contextlib.contextmanager
    def daemon(binary, env, label):
        with (root / (label + ".log")).open("wb") as log:
            process = subprocess.Popen(guard([binary, "daemon"]), cwd=work, env=env, stdout=log, stderr=log)
            try:
                def ready():
                    assert process.poll() is None, log.name
                    return owner(env)
                await_value(ready)
                yield
            finally:
                process.terminate()
                try:
                    process.wait(timeout=8)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)

    request = dict(agent="claude", name="Stopped Claude", cwd=str(work), cols=100, rows=30,
                   env=[f"{key}={go_env[key]}" for key in ("HOME", "PATH", "TERM", "LANG", "ANTHROPIC_API_KEY")])

    def launch(env, values, *, error=None):
        reply = owner(env, {"op": "launch", "launch": values}, timeout=20)
        if error:
            assert reply["op"] == "error" and error in str(reply), reply
            return
        assert reply["op"] == "launch", reply
        return reply["session"]["id"]

    def turn(binary, env, sid, index, transcript=None):
        def ready():
            display = cli(binary, env, "peek", sid)
            if b"I am using this for local development" in display:
                cli(binary, env, "send", sid, "--key", "down")
                cli(binary, env, "send", sid, "--key", "enter")
            if b"trust the files" in display and b"trust this folder" in display:
                cli(binary, env, "send", sid, "--key", "enter")
            return RESPONSE in display if index else "❯".encode() in display
        try:
            await_value(ready)
        except AssertionError:
            print(cli(binary, env, "peek", sid).decode(errors="replace"))
            raise
        cli(binary, env, "send", sid, "--text", PROMPTS[index])
        await_value(lambda: PROMPTS[:index + 1] in model.turns)
        def saved():
            paths = [transcript] if transcript else list((home / ".claude/projects").rglob("*.jsonl"))
            for path in paths:
                saw_user = False
                for line in path.read_bytes().splitlines():
                    row = json.loads(line)
                    if row.get("type") == "user" and PROMPTS[index].encode() in line:
                        assert row["cwd"] == str(work) and row["sessionId"] == path.stem
                        saw_user = True
                    if saw_user and row.get("type") == "assistant" and RESPONSE in line:
                        return path
            return None
        return await_value(saved)

    def kill(binary, env, sid):
        cli(binary, env, "kill", sid)
        await_value(lambda: any(row["id"] == sid and row["status"]["Process"] == "exited" for row in listed(env)))

    try:
        check_guards(root, [model.server_port])
        assert cli(claude, go_env, "--version").strip() == b"2.1.329 (Claude Code)"
        save(home / ".claude.json", dict(
            hasCompletedOnboarding=True, lastOnboardingVersion="2.1.329", theme="dark",
            projects={str(work): dict(hasTrustDialogAccepted=True)},
            customApiKeyResponses=dict(approved=[KEY], rejected=[])))
        settings = home / ".claude/settings.json"
        save(settings, dict(env=dict(ANTHROPIC_BASE_URL=f"http://127.0.0.1:{model.server_port}",
                                    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC="1", DISABLE_AUTOUPDATER="1",
                                    CLAUDE_CODE_TMPDIR=str(root), CLAUDE_CODE_MAX_RETRIES="0")))
        with daemon(go_bin, go_env, "initial-go"):
            source = launch(go_env, request | {"options": {"model": "sonnet"}})
            origin = state / source.split("/")[-1]
            try:
                transcript = turn(go_bin, go_env, source, 0)
                native = await_value(lambda: disk(origin / "meta.json").get("conversation_id"))
                assert native == transcript.stem
            finally:
                kill(go_bin, go_env, source)
        birth = transcript.stat().st_dev, transcript.stat().st_ino
        original = {p.name: p.read_bytes() for p in origin.iterdir() if p.is_file()}
        meta = disk(origin / "meta.json")
        assert meta["cli_identity"]["path"] == str(claude)
        assert disk(origin / "native-writers-stopped.json")["writers_stopped"]
        resume = request | {"options": {"resume_from": source}}
        with daemon(rust_bin, rust_env, "rust"):
            observation = origin / "observed-cli.json"
            held = observation.with_suffix(".held")
            observation.rename(held)
            try:
                launch(rust_env, resume, error="writer death is unproved")
            finally:
                held.rename(observation)
            launch(rust_env, resume | {"cwd":str(home)}, error="original checkout")
            launch(rust_env, resume | {"env":[entry for entry in request["env"] if not entry.startswith("ANTHROPIC_API_KEY=")]},
                   error="account")
            launch(rust_env, resume | {"options":{"resume_from":"wrong/" + source.split("/")[-1]}},
                   error="ordinary local source")
            successor = launch(rust_env, resume)
            try:
                launch(rust_env, resume, error="already has a live or unconfirmed writer")
                turn(rust_bin, rust_env, successor, 1, transcript)
            finally:
                kill(rust_bin, rust_env, successor)
        successor_dir = state / "rust-v1" / successor.split("/")[-1]
        adoption = successor_dir / "claude-go-resume-observed.json"
        held = adoption.with_suffix(".held")
        adoption.rename(held)
        try:
            denied = run(rust_bin, rust_env, "daemon", "export-stopped-go")
            assert denied.returncode and b"confirmed native transcript adoption" in denied.stderr, denied
        finally:
            held.rename(adoption)
        saved_settings = disk(settings)
        save(settings, saved_settings | {"defaultMode": "plan"})
        try:
            denied = run(rust_bin, rust_env, "daemon", "export-stopped-go")
            assert denied.returncode and b"conversation, account or executable changed" in denied.stderr, denied
        finally:
            save(settings, saved_settings)
        assert not (state / successor.split("/")[-1]).exists()
        cli(rust_bin, rust_env, "daemon", "export-stopped-go")
        exported = disk(state / successor.split("/")[-1] / "meta.json")
        assert exported["cli_identity"] == meta["cli_identity"]
        assert exported["launch_options"] == meta["launch_options"]
        assert exported["conversation_id"] == native
        with daemon(go_bin, go_env, "restored-go"):
            restored = launch(go_env, request | {"options":{"resume_from":successor}})
            try:
                turn(go_bin, go_env, restored, 2, transcript)
            finally:
                kill(go_bin, go_env, restored)
        assert (transcript.stat().st_dev, transcript.stat().st_ino) == birth
        assert all((origin / name).read_bytes() == content for name, content in original.items())
        print("PASS Go v0.29.24 → Rust → Go retained Claude 2.1.329 conversation, transcript inode and options")
    finally:
        model.shutdown()
        model.server_close()
        server.join(timeout=5)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("go", "rust", "claude"):
        parser.add_argument(name, type=Path)
    parser.add_argument("--scratch", type=Path)
    args = parser.parse_args()
    inputs = [path.resolve(strict=True) for path in (args.go, args.rust, args.claude)]
    if args.scratch:
        verify(*inputs, args.scratch.resolve(strict=True))
    else:
        with tempfile.TemporaryDirectory(prefix="swarm-cold-claude-", dir="/tmp") as directory:
            verify(*inputs, Path(directory).resolve())
