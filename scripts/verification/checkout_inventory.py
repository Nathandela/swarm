#!/usr/bin/env python3
"""Compare pinned Go inventory and the Rust CLI on private local fixture roots."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import shlex
import subprocess
import sys
import tempfile

from stopped_go_state import await_value, daemon, disk, fixture_env, listed, owner, private, rehearse


def inventory(binary, env, *args):
    result = subprocess.run([binary, "checkout", "inventory", "--json", *map(str, args)],
                            env=env, capture_output=True, text=True, timeout=20)
    if not result.stdout:
        raise AssertionError(result.stderr)
    return result.returncode, json.loads(result.stdout)


def git(path, *args):
    return subprocess.run(["git", "-C", str(path), *args], capture_output=True, text=True, check=True)


def manifest(root):
    return {str(p.relative_to(root)): (p.stat().st_mode, hashlib.sha256(p.read_bytes()).hexdigest())
            for p in root.rglob("*") if p.is_file() and not p.is_symlink()}


def check(go, rust, base):
    paths = {name: base / name for name in ("home", "bin", "original", "native", "repo")}
    for path in paths.values():
        path.mkdir(mode=0o700)
    repo = paths["repo"]
    git(repo, "init")
    git(repo, "-c", "user.name=Fixture", "-c", "user.email=f@invalid.invalid", "commit", "--allow-empty", "-m", "root")
    linked = base / 'work\t tree "quoted"'
    ghost = base / "prunable"
    git(repo, "worktree", "add", "--detach", str(linked))
    git(repo, "worktree", "add", "--detach", str(ghost))
    sub = linked / "sub"
    sub.mkdir()
    provider = paths["bin"] / "claude"
    conversation = "11111111-1111-4111-8111-111111111111"
    body = json.dumps({"session_id": conversation, "cwd": str(sub)})
    provider.write_text('#!/bin/sh\nif [ "$1" = --version ]; then echo "2.1.250 (Claude Code)"; exit; fi\n'
                        f"printf 'Session {conversation}\\n'\n"
                        'if [ -n "$SWARM_RUST_HOOK_SOCKET" ]; then\n'
                        f"  printf '%s\\n' {shlex.quote(body)} | {shlex.quote(rust)} hook UserPromptSubmit\n"
                        "fi\nexec /bin/sleep 300\n")
    provider.chmod(0o700)

    legacy = paths["original"]
    ge = fixture_env(base, legacy, paths["bin"])
    re = fixture_env(base, paths["native"], paths["bin"])
    launch = {"agent": "claude", "cwd": str(sub), "name": "Original session", "rows": 24, "cols": 80,
              "env": [f"PATH={ge['PATH']}", f"HOME={ge['HOME']}"]}
    with daemon(go, ge, base / "go.log"):
        opened = owner(ge, {"op": "launch", "launch": launch})
        assert opened["op"] == "launch", opened
        session = opened["session"]["id"]
        leaf = session.split("/")[1]
        try:
            code, old = inventory(go, ge, repo, sub)
            assert code == 0, old
            assert inventory(rust, ge, repo, sub) == (code, old)
            details = next(p for p in old["paths"] if p["path"] == str(sub))
            assert details["git_kind"] == "linked" and details["worktree"] == str(linked)
            assert details["linked_sessions"][0]["state"] == "live"
            await_value(lambda: disk(legacy / leaf / "meta.json")["conversation_id"] == conversation)
        finally:
            assert owner(ge, {"op": "kill", "session_id": session})["op"] == "ok"
        await_value(lambda: listed(ge)[0]["status"]["Process"] == "exited")

    pending = {"operation_id": "fixture:queued", "action": "semantic_send", "session_id": leaf,
               "session_instance": "original-instance", "phase": "prepared"}
    with (legacy / "idempotency/idempotency.log").open("ab") as stream:
        stream.write(json.dumps(pending).encode() + b"\n")
    child = legacy / "retained-child"
    child.mkdir(mode=0o700)
    prior = disk(legacy / leaf / "meta.json")
    private(child / "meta.json", prior | {"id": child.name, "name": "Historical retry",
                                         "shim_pid": 0, "shim_start_time": 0, "roster_hidden": True,
                                         "resumed_from": leaf, "spawned_from": leaf})
    before = manifest(legacy), manifest(repo), manifest(linked)
    code, old = inventory(go, ge, repo, sub)
    assert code == 0, old
    actual = inventory(rust, ge, repo, sub)
    assert actual == (code, old), (old, actual)
    assert before == (manifest(legacy), manifest(repo), manifest(linked))
    ended = next(s for p in old["paths"] for s in p["linked_sessions"] if s["id"] == session)
    assert ended["resume"] == "has_successor" and ended["unresolved_receipts"][0]["id"] == pending["operation_id"]

    with daemon(rust, re, base / "rust.log"):
        opened = owner(re, {"op": "launch", "launch": launch | {"name": "Rust session"}})
        assert opened["op"] == "launch", opened
        second = opened["session"]["id"]
        try:
            code, current = inventory(rust, re, "--supervisor-state", legacy, sub)
            assert code == 0, current
            assert {s["state"] for p in current["paths"] for s in p["linked_sessions"]} == {"live", "exited"}
            native = paths["native"] / "rust-v1" / second.split("/")[1]
            await_value(lambda: disk(native / "observation.json")["conversation_id"] == conversation)
        finally:
            assert owner(re, {"op": "kill", "session_id": second})["op"] == "ok"
        await_value(lambda: listed(re)[0]["status"]["Process"] == "exited")
    code, current = inventory(rust, re, "--supervisor-state", legacy, sub)
    assert code == 0, current
    rust_row = next(s for p in current["paths"] for s in p["linked_sessions"] if s["id"] == second)
    assert rust_row["state"] == "exited" and rust_row["resume"] == "candidate", rust_row

    parked = base / "parked"
    shutil.copytree(legacy, parked)
    connector = base / "discord"
    connector.mkdir(mode=0o700)
    discussion = "discord-teammate-fixture"
    parked_endpoint = "ep-" + hashlib.sha256(str(parked).encode()).hexdigest()[:8]
    manager = parked_endpoint + "/" + leaf
    retirement = "fixture:retirement"
    instance = "fixture-instance"
    mailbox_child = parked / child.name / "meta.json"
    manager_meta = parked / leaf / "meta.json"
    old_manager = disk(manager_meta)
    private(manager_meta, old_manager | {"role": "manager", "manager_conversation_id": discussion,
            "manager_retirement": {"operation_id": retirement, "instance": instance,
                 "native_session_id": conversation, "shim_pid": old_manager["shim_pid"],
                 "shim_start_time": old_manager["shim_start_time"], "claimed_at": old_manager["last_activity"]}})
    private(mailbox_child, disk(mailbox_child) | {"worker_discussion": discussion,
             "worker_instance": "worker-instance", "worker_attempt": "worker-attempt"})
    socket = str(parked / "daemon.sock")
    private(connector / "config.json", {"version": 1, "mode": "personal", "installation_id": "fixture",
            "application_id": "10", "owner_id": "20", "dm_channel_id": "30", "daemon_socket": socket,
            "supervisor": {"state_dir": str(parked)}, "bot_token": "do-not-print"})
    private(connector / "rust-text-v1.json", {"version": 2, "identity": ["fixture", "10", "20", "30", socket],
            "messages": {"one": {"operation_id":"fixture:discord","state":"prepared","text":"do-not-print"}},
            "outbox": {"reply": {"state": "sending"}}, "manager": {"session": manager,
            "launch": {"cwd": str(sub), "manager": {"conversation_id": discussion}},
            "binding": {"session": manager, "session_instance": instance, "conversation_id": discussion},
            "retirement": {"operation_id": retirement}}})
    selector = paths["native"] / "discord-active.json"
    private(selector, str(connector))
    code, selected = inventory(rust, re, sub)
    assert code == 0 and str(parked) in selected["supervisors"], selected
    assert inventory(rust, re, "--discord-state", connector, sub) == (code, selected)
    row, = selected["discussions"]
    assert row["state"] == "parked" and row["manager_session"] == manager
    assert row["worker_sessions"] == [parked_endpoint + "/" + child.name]
    assert {r["kind"] for r in row["unresolved_receipts"]} == {"discord_ingress", "discord_publication"}
    assert "do-not-print" not in json.dumps(selected)
    selector.unlink()

    shutil.rmtree(ghost)
    code, partial = inventory(rust, ge, repo)
    assert code == 1 and not partial["complete"]
    assert any(p["path"] == str(ghost) and p["prunable"] for p in partial["paths"])
    assert inventory(go, ge, repo) == (code, partial)
    conflicting = base / "other-repo"
    conflicting.mkdir()
    git(conflicting, "init")
    git(conflicting, "-c", "user.name=Fixture", "-c", "user.email=f@invalid.invalid", "commit", "--allow-empty", "-m", "root")
    claimed_registration = next(p for p in (repo / ".git/worktrees").iterdir()
                                if str(linked) in (p / "gitdir").read_text())
    other = conflicting / ".git/worktrees"
    other.mkdir()
    shutil.copytree(claimed_registration, other / "duplicate")
    for binary in (go, rust):
        code, uncertain = inventory(binary, ge, repo, conflicting)
        assert code == 1 and any(p["path"] == str(linked) and p["git_kind"] == "ambiguous" for p in uncertain["paths"]), uncertain

    def inspect_export(copied, env, session):
        before = manifest(copied)
        results = [inventory(binary, env, Path(env["HOME"]))[1] for binary in (go, rust)]
        rows = [[row for path in result["paths"] for row in path["linked_sessions"] if row["id"] == session]
                for result in results]
        left, right = rows
        assert left == right and len(right) == 1 and right[0]["resume"] == "candidate", rows
        assert manifest(copied) == before

    fixture = base / "export"
    fixture.mkdir(mode=0o700)
    rehearse(go, rust, fixture, on_export=inspect_export)
    print("PASS checkout inventory: pinned Go parity, native Rust, escaped worktrees, queued receipts and read-only state")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit("usage: checkout_inventory.py PINNED_GO RUST")
    go, rust = (str(Path(value).resolve(strict=True)) for value in sys.argv[1:])
    with tempfile.TemporaryDirectory(prefix="ci-", dir=os.path.realpath("/tmp")) as root:
        check(go, rust, Path(root))
