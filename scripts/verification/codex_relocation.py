#!/usr/bin/env python3
"""Exercise a real Codex thread after deleting its original checkout, offline."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import time

import macos_codex_fixture as macos
from live_go_shim import attach, connect, grid_text, snapshot
from native_fixture_sandbox import check_guards
from parity_codex import model_fixture
from stopped_go_state import await_value, daemon, disk, fixture_env, listed, owner

CAPS = ("attach", "owner-launch", "resume-relocate")
IDS = {
    "go": (macos.GO_ID, macos.RESUMED_GO_ID, "gocodexfixturecc", "gocodexfixturedd"),
    "rust": (macos.RUST_ID, "c0d3c02dc0d3c02dc0d3c02d", "c0d3c03dc0d3c03dc0d3c03d", "c0d3c04dc0d3c04dc0d3c04d"),
}
PROMPTS = ("Codex original checkout turn", "Codex relocated checkout turn", "Codex ordinary resumed turn", "Codex repeated ordinary resumed turn")


def birth(pid):
    if sys.platform == "darwin":
        return macos.process_birth(pid)
    return int(Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()[19])


def stopped(pid, initial):
    try:
        return birth(pid) != initial
    except FileNotFoundError:
        return True


def contexts(rollout):
    rows = rollout.read_bytes().splitlines()
    try:
        events = [json.loads(row) for row in rows]
    except json.JSONDecodeError as exc:
        if rows and exc.doc == rows[-1].decode():
            return []
        raise
    return [row["payload"] for row in events if row["type"] == "turn_context"]


def verify_context(row, cwd):
    assert row["cwd"] == str(cwd), row.get("cwd")
    assert row["sandbox_policy"]["type"] in ("read-only", "workspace-write"), row.get("sandbox_policy")
    return row["sandbox_policy"]["type"]


def rehearse(binary, provider, implementation, root):
    tools, home, state, work, selected = (
        root / name for name in ("bin", "home", "state", "work", "selected")
    )
    for directory in (tools, home, state, work, selected, home / ".codex"):
        directory.mkdir(mode=0o700)
    vendor = tools / "codex"
    if sys.platform == "darwin":
        shutil.copy2(provider, vendor)
    else:
        vendor.symlink_to(provider)
    replies = {prompt: f"Offline Codex relocation reply {index}." for index, prompt in enumerate(PROMPTS, 1)}
    observed = []

    def answer(items):
        user = next((item for item in reversed(items) if item.get("role") == "user"), {})
        for position in user.get("content", []):
            text = position.get("text")
            if text in replies:
                observed.append(text)
                return replies[text]
        return None

    model, _ = model_fixture(root, False, answer_for_input=answer)
    config = home / ".codex/config.toml"
    settings = config.read_text().replace('sandbox_mode="read-only"', 'sandbox_mode="workspace-write"', 1)
    config.write_text(settings + f'\n[projects."{selected}"]\ntrust_level="trusted"\n')
    env = fixture_env(root, state, tools) | {"TERM": "xterm-256color"}
    guard = None

    def state_for(identifier):
        if implementation == "rust":
            return state / "rust-v1" / identifier.split("/")[-1]
        return state / identifier.split("/")[-1]

    def request(cwd, resume=None, relocate=False):
        options = {"resume_from": resume} if resume else {"sandbox": "read-only"}
        if relocate:
            options["resume_relocate"] = "true"
        return owner(env, {"op": "launch", "launch": {
            "agent": "codex", "cwd": str(cwd), "name": "Codex relocation fixture",
            "rows": 30, "cols": 100, "options": options,
        }}, caps=CAPS, timeout=55)

    def conversation(directory):
        path = directory / ("observation.json" if implementation == "rust" else "meta.json")
        return await_value(lambda: disk(path).get("conversation_id"))

    def turn(identifier, prompt):
        await_value(lambda: "OpenAI Codex" in "\n".join(snapshot(env, identifier)["lines"]))
        with connect(env) as connection:
            _, grid = attach(connection, identifier)
            assert "OpenAI Codex" in grid_text(grid)
            send = connection[0]
            send(3, prompt.encode())
            time.sleep(.3)
            send(3, b"\r")
            await_value(lambda: prompt in observed)
            await_value(lambda: replies[prompt] in "\n".join(snapshot(env, identifier)["lines"]))

    def drain(identifier):
        directory = state_for(identifier)
        if implementation == "rust":
            native = disk(directory / "owner.json")
            with socket.socket(socket.AF_UNIX) as probe:
                probe.connect(str(directory / "b"))
                if sys.platform == "darwin":
                    backend, uid = macos.socket_peer(probe)
                else:
                    backend, uid, _ = struct.unpack("3i", probe.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
                assert uid == os.geteuid()
            identities = [(pid, birth(pid)) for pid in (native["pid"], native["worker_pid"], backend)]
        else:
            meta = disk(directory / "meta.json")
            native = disk(directory / "native-process.json")
            identities = [(meta["shim_pid"], meta["shim_start_time"]), (native["pid"], native["start_time"]),
                          (native["backend_pid"], native["backend_start_time"])]
        assert owner(env, {"op": "kill", "session_id": identifier})["op"] == "ok"
        await_value(lambda: all(stopped(pid, start) for pid, start in identities))

    def launch_turn(cwd, index, resume=None, relocate=False):
        result = request(cwd, resume, relocate)
        assert result["op"] == "launch", result
        active = result["session"]["id"]
        try:
            assert Path(result["session"]["cwd"]) == cwd, result
            turn(active, PROMPTS[index])
            return active, conversation(state_for(active))
        finally:
            drain(active)

    try:
        check_guards(root, [model.server_port])
        if sys.platform == "darwin":
            parents = [state / name for name in IDS[implementation]]
            endpoints = [p / "codex.sock" for p in parents] if implementation == "go" else [
                state / "rust-v1" / name / "b" for name in IDS[implementation]]
            guard = macos.Guard(root, state, model.server_port, False, endpoints=endpoints)
            binary = guard.wrap(binary, implementation)
            vendor = guard.wrap(vendor, "version-probe")
        reported = subprocess.check_output([vendor, "--version"], env=env).strip()
        assert reported == b"codex-cli 0.172.0", reported
        env["SWARM_FIXTURE_SESSION_ID"] = IDS[implementation][0]
        with daemon(binary, env, root / "source.log"):
            original, thread = launch_turn(work, 0)
        rollout, = (home / ".codex/sessions").rglob(f"*-{thread}.jsonl")
        prefix = rollout.read_bytes()
        identity = rollout.stat().st_dev, rollout.stat().st_ino
        contexts_seen = [verify_context(contexts(rollout)[-1], work)]
        if implementation == "rust":
            assert contexts_seen == ["read-only"], contexts_seen
        work.rename(root / "preserved-original")
        assert not work.exists()

        env["SWARM_FIXTURE_SESSION_ID"] = IDS[implementation][1]
        with daemon(binary, env, root / "relocated.log"):
            authority_refused = None
            if implementation == "rust":
                auth = home / ".codex/auth.json"
                saved = auth.read_bytes()
                before = [row["id"] for row in listed(env)]
                try:
                    auth.write_text(json.dumps({"auth_mode": "apikey", "OPENAI_API_KEY": "changed-offline"}))
                    rejected = request(selected, original, True)
                    assert rejected["op"] == "error" and "authority" in rejected.get("error", "").lower(), rejected
                    assert [row["id"] for row in listed(env)] == before
                    assert rollout.read_bytes() == prefix and observed == [PROMPTS[0]]
                    authority_refused = True
                finally:
                    auth.write_bytes(saved)
            moved, moved_thread = launch_turn(selected, 1, original, True)
            assert moved_thread == thread
        contexts_seen.append(verify_context(contexts(rollout)[-1], selected))
        sessions = [original, moved]
        for index in (2, 3):
            env["SWARM_FIXTURE_SESSION_ID"] = IDS[implementation][index]
            with daemon(binary, env, root / f"ordinary-resume-{index}.log"):
                resumed, resumed_thread = launch_turn(selected, index, sessions[-1])
                assert resumed_thread == thread and resumed not in sessions
                sessions.append(resumed)
            contexts_seen.append(verify_context(contexts(rollout)[-1], selected))
        if implementation == "rust":
            assert contexts_seen == ["read-only"] * len(PROMPTS), contexts_seen
        assert list((home / ".codex/sessions").rglob("*.jsonl")) == [rollout]
        assert (rollout.stat().st_dev, rollout.stat().st_ino) == identity
        assert rollout.read_bytes().startswith(prefix)
        assert observed == list(PROMPTS), observed
        return {"implementation": implementation, "platform": sys.platform, "architecture": os.uname().machine,
                "codex": "0.172.0", "same_native_thread": True, "same_rollout_file": True,
                "original_rollout_prefix_sha256": hashlib.sha256(prefix).hexdigest(),
                "changed_authority_refused": authority_refused, "inputs": len(observed),
                "turn_context_checkout": ["original", "selected", "selected", "selected"],
                "turn_context_sandbox": contexts_seen}
    finally:
        model.shutdown()
        model.server_close()
        if guard:
            guard.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--provider", type=Path, required=True)
    parser.add_argument("--implementation", choices=IDS, required=True)
    args = parser.parse_args()
    os.umask(0o077)
    with tempfile.TemporaryDirectory(prefix="cx-", dir=Path("/tmp").resolve()) as root:
        print(json.dumps(rehearse(args.binary.resolve(), args.provider.resolve(),
                                 args.implementation, Path(root)), sort_keys=True))


if __name__ == "__main__":
    main()
