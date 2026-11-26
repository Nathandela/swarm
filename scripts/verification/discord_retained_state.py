#!/usr/bin/env python3
"""Exercise original Go-made connector evidence without using Discord."""
import fcntl
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from contextlib import ExitStack
from stopped_go_state import await_value, daemon, disk, fixture_env, listed, owner


def run(binary, env, *args, code=0):
    result = subprocess.run([binary, *map(str, args)], env=env, capture_output=True,
                            text=True, timeout=20)
    assert result.returncode == code, (result.returncode, result.stdout, result.stderr)
    assert "synthetic-unused-credential" not in result.stdout + result.stderr
    return result.stdout


def running(stack, env, label):
    result = owner(env, {"op": "launch", "launch": {
        "agent": "claude", "cwd": env["HOME"], "name": label, "cols": 80, "rows": 24,
        "env": [f"PATH={env['PATH']}", f"HOME={env['HOME']}"],
    }})
    assert result["op"] == "launch", result
    session = result["session"]["id"]

    def stop():
        assert owner(env, {"op": "kill", "session_id": session})["op"] == "ok"

    stack.callback(stop)
    await_value(lambda: any(row["id"] == session and row["status"]["Process"] == "running"
                            for row in listed(env)))
    return session


def route(fixture, env, connector, discussion, session):
    endpoint, socket, state = run(fixture, env, "route", connector, discussion).split()
    assert endpoint == session.split("/")[0]
    selected = env | {"SWARM_DAEMON_SOCK": socket, "SWARM_DAEMON_STATE": state}
    assert any(row["id"] == session and row["status"]["Process"] == "running"
               for row in listed(selected))
    return selected


def inspect(go, rust, fixture, base):
    home, original, destination, tools = (base / part for part in ("home", "old", "new", "bin"))
    for directory in home, original, destination, tools:
        directory.mkdir(mode=0o700)
    env = fixture_env(base, destination, tools)
    old_env = fixture_env(base, original, tools)
    fake = tools / "claude"
    fake.write_text('#!/bin/sh\nif [ "$1" = --version ]; then echo "2.1.250 (Claude Code)"; exit; fi\n'
                    'exec /bin/sleep 300\n')
    fake.chmod(0o700)
    old_exclude = "fixture-token"
    for mode in ("personal", "teammate"):
        connector = base / ("dormant-" + mode)
        run(fixture, env, "create", connector, original, mode, "false")
        run(fixture, env, "traffic", connector, "sent")
        pieces = ["discord", "migrate-state", "--state-dir", connector]
        protected = {name: (connector / name).read_bytes() for name in ("discord.db", "config.json", old_exclude)}
        before = sorted(p.name for p in connector.iterdir())
        preview = json.loads(run(rust, env, *pieces, "--dry-run"))
        assert preview["eligible"] and preview["inventory"]["mode"] == mode
        assert preview["inventory"]["exclusions"] == 1 and preview["inventory"]["settled"] == 1 and not preview["gateway_authority"]
        assert before == sorted(p.name for p in connector.iterdir())
        adopted = json.loads(run(rust, env, *pieces))
        assert adopted["state"] == "retained"
        assert json.loads(run(rust, env, *pieces)) == adopted
        undone = json.loads(run(rust, env, *pieces, "--rollback"))
        assert undone["state"] == "rolled_back" and not undone["gateway_authority"]
        assert protected == {name: (connector / name).read_bytes() for name in protected}
        assert run(fixture, env, "read", connector).strip() == "true"
        assert run(fixture, env, "traffic-read", connector, "sent").strip() == "sent"
        run(fixture, env, "change", connector)
        assert run(fixture, env, "read", connector).strip() == "false"
        changed = json.loads(run(rust, env, *pieces, "--dry-run", code=1))
        assert changed["state"] == "changed"
    unsafe = base / "pending"
    run(fixture, env, "create", unsafe, original, "teammate", "false")
    run(fixture, env, "pending", unsafe)
    run(fixture, env, "traffic", unsafe, "uncertain")
    before = (unsafe / "discord.db").read_bytes()
    blocked = json.loads(run(rust, env, "discord", "migrate-state", "--state-dir", unsafe, code=1))
    assert blocked["inventory"]["unresolved"] == 2 and not blocked["eligible"]
    assert before == (unsafe / "discord.db").read_bytes()
    assert not (unsafe / ".rust-go-discord-retained.json").exists()

    active = base / "transition"
    run(fixture, env, "create", active, original, "teammate", "true")
    run(fixture, env, "traffic", active, "sent")
    run(fixture, env, "traffic", active, "uncertain")
    run(fixture, env, "bind", active, "existing", "401")
    protected = {name: (active / name).read_bytes() for name in ("discord.db", old_exclude)}
    config = active / "config.json"
    old_selector = original / "discord-active.json"
    new_selector = destination / "discord-active.json"
    original_config = disk(config)
    original_selection = old_selector.read_bytes()
    command = ["discord","migrate-supervisor","--state-dir",active]
    # The original Go and new Rust supervisors are real; the connector stays offline.
    with ExitStack() as stack:
        stack.enter_context(daemon(go, old_env, base/"old.log"))
        stack.enter_context(daemon(rust, env, base/"new.log"))
        old_session = running(stack, old_env, "Original discussion session")
        new_session = running(stack, env, "Destination discussion session")
        route(fixture, env, active, "existing", old_session)
        assert run(rust, env, *command, "--dry-run") == run(go, env, *command, "--dry-run")
        assert disk(config) == original_config and not new_selector.exists()
        run(rust, env, *command, "--from-state-dir", original, code=1)
        pidfile = original / "daemon.pid"
        actual = pidfile.read_bytes()
        try:
            pid, birth = actual.split()
            pidfile.write_bytes(pid + b" " + str(int(birth) + 1).encode() + b"\n")
            run(rust, env, *command, "--dry-run", code=1)
        finally:
            pidfile.write_bytes(actual)
        assert disk(config) == original_config and not new_selector.exists()
        alien = destination / "unrelated.lock"
        fd = os.open(alien, os.O_CREAT | os.O_EXCL | os.O_RDWR, 0o600)
        with os.fdopen(fd, "rb") as unrelated:
            fcntl.flock(unrelated, fcntl.LOCK_EX)
            wrong_home = env | {"SWARM_DAEMON_LOCK": str(alien)}
            run(rust, wrong_home, *command, "--dry-run", code=1)
            run(rust, wrong_home, *command, code=1)
            assert disk(config) == original_config and not new_selector.exists()
            # Supply a foreign lock in the old Go enrollment, with its true socket and PID.
            wrong_source = original_config | {"supervisor": original_config["supervisor"] | {"lock": str(alien)}}
            saved = config.read_bytes()
            try:
                config.write_text(json.dumps(wrong_source))
                run(rust, env, *command, code=1)
                assert disk(config) == wrong_source and not new_selector.exists()
            finally:
                config.write_bytes(saved)
        run(rust, env, *command)
        result = disk(config)
        assert result["daemon_socket"] == env["SWARM_DAEMON_SOCK"]
        assert result["previous_supervisor"]["socket"] == old_env["SWARM_DAEMON_SOCK"]
        assert result["previous_supervisor"]["endpoint"] == "ep-" + hashlib.sha256(str(original).encode()).hexdigest()[:8]
        assert disk(new_selector) == str(active) and old_selector.read_bytes() == original_selection
        # Reproduce the crash image after config commit and before selector publication.
        new_selector.unlink()
        run(rust, env, *command)
        assert disk(config) == result and disk(new_selector) == str(active)
        assert run(go, env, *command, "--dry-run") == run(rust, env, *command, "--dry-run")
        assert protected == {name: (active / name).read_bytes() for name in protected}
        run(fixture, env, "bind", active, "new", "402", env["SWARM_DAEMON_SOCK"])
        route(fixture, env, active, "existing", old_session)
        route(fixture, env, active, "new", new_session)
    assert run(fixture, env, "read", active).strip() == "true"
    assert run(fixture, env, "traffic-read", active, "sent").strip() == "sent"
    assert run(fixture, env, "traffic-read", active, "uncertain").strip() == "uncertain"
    # Operate on an enabled but stopped connector with Go-made receipts. There is
    # no bridge or Discord transport in this rehearsal.
    controls = base / "controls"
    run(fixture, old_env, "create", controls, original, "personal", "true")
    run(fixture, old_env, "traffic", controls, "sent")
    run(fixture, old_env, "traffic", controls, "uncertain")
    run(fixture, old_env, "bind", controls, "existing-dm", "400")
    assert run(fixture, old_env, "ingress", controls, "existing-dm", "501", "settle").strip() == "1"
    config_bytes = (controls / "config.json").read_bytes()
    token_bytes = (controls / old_exclude).read_bytes()
    route_args = ["discord", "migrate-state", "--controls"]
    with ExitStack() as stack:
        stack.enter_context(daemon(go, old_env, base / "controls.log"))
        target = running(stack, old_env, "Selected audience worker")
        raw_before = (controls / "discord.db").read_bytes()
        assert json.loads(run(rust, old_env, *route_args, "--dry-run"))["eligible"]
        assert (controls / "discord.db").read_bytes() == raw_before
        assert json.loads(run(rust, old_env, *route_args))["state"] == "retained_controls"
        run(rust, old_env, "discord", "run", code=1)
        run(rust, old_env, "discord", "exclude", "invalid/target", code=1)
        with (controls / "discord.db").open("rb") as writer:
            fcntl.flock(writer, fcntl.LOCK_EX)
            run(rust, old_env, "discord", "exclude", target, code=1)
        assert (controls / "discord.db").read_bytes() == raw_before
        run(rust, old_env, "discord", "exclude", target)
        assert run(fixture, old_env, "audience-read", controls, target).strip() == "true"
        after = (controls / "discord.db").read_bytes()
        run(rust, old_env, "discord", "exclude", target)
        assert (controls / "discord.db").read_bytes() == after
        run(rust, old_env, "discord", "include", target)
        assert run(fixture, old_env, "audience-read", controls, target).strip() == "false"
        run(rust, old_env, "discord", "exclude", target)
        assert json.loads(run(rust, old_env, *route_args, "--rollback"))["state"] == "rolled_back"
        assert run(fixture, old_env, "audience-read", controls, target).strip() == "true"
        run(rust, old_env, "discord", "include", target, code=1)
        run(fixture, old_env, "audience-write", controls, target, "false")
        assert run(fixture, old_env, "audience-read", controls, target).strip() == "false"
    assert (controls / "config.json").read_bytes() == config_bytes
    assert (controls / old_exclude).read_bytes() == token_bytes
    assert run(fixture, old_env, "traffic-read", controls, "sent").strip() == "sent"
    assert run(fixture, old_env, "traffic-read", controls, "uncertain").strip() == "uncertain"
    # Pinned Go re-sees this message after rollback but creates no new pending work.
    assert run(fixture, old_env, "ingress", controls, "existing-dm", "501", "replay").strip() == "0"
    assert run(fixture, old_env, "ingress", controls, "existing-dm", "502", "hold").strip() == "1"
    pending_database = (controls / "discord.db").read_bytes()
    assert not json.loads(run(rust, old_env, *route_args, "--dry-run", code=1))["eligible"]
    assert (controls / "discord.db").read_bytes() == pending_database
    for phase in ("prepared", "executing", "running"):
        recovery = base / ("recovery-" + phase)
        run(fixture, old_env, "create", recovery, original, "personal", "true")
        run(fixture, old_env, "bind", recovery, "existing-dm", "400")
        run(fixture, old_env, "voice-ingress", recovery, "existing-dm", "701", phase)
        original_db = recovery / "discord.db"
        voice_db, = (recovery / "discussions").glob("*/voice.db")
        before = original_db.read_bytes()
        voice_bytes = voice_db.read_bytes()
        command = ["discord", "migrate-state", "--settle-personal-ingress", "701"]
        code = int(phase == "prepared")
        dispatch = ["discord", "migrate-state", "--dispatch-personal-ingress", "701"]
        run(rust, old_env, *dispatch, "--dry-run", code=int(not code))
        if code:
            run(rust, old_env, *dispatch, code=1)
        assert (original_db.read_bytes(), voice_db.read_bytes()) == (before, voice_bytes)
        run(rust, old_env, *command, "--dry-run", code=code)
        assert original_db.read_bytes() == before
        with voice_db.open("rb") as voice_writer:
            fcntl.flock(voice_writer, fcntl.LOCK_EX)
            run(rust, old_env, *command, code=1)
        assert original_db.read_bytes() == before
        result = run(rust, old_env, *command, code=code)
        if not code:
            assert f"Native phase remains {phase}." in result
        after = original_db.read_bytes()
        assert (after == before) == (phase == "prepared")
        run(rust, old_env, *command, code=code)
        assert original_db.read_bytes() == after
        pending = run(fixture, old_env, "ingress", recovery, "existing-dm", "701", "replay").strip()
        assert pending == ("1" if phase == "prepared" else "0")
        assert voice_db.read_bytes() == voice_bytes
        phase_result = run(fixture, old_env, "voice-phase", voice_db.parent, "701").strip()
        assert phase_result == f"{phase} {str(phase != 'prepared').lower()}"
    corrupt = base / "recovery-count"
    run(fixture, old_env, "create", corrupt, original, "personal", "true")
    run(fixture, old_env, "bind", corrupt, "existing-dm", "400")
    run(fixture, old_env, "voice-ingress", corrupt, "existing-dm", "701", "running")
    assert run(fixture, old_env, "ingress", corrupt, "existing-dm", "702", "hold").strip() == "2"
    run(fixture, old_env, "pending-count", corrupt, "1")
    source = corrupt / "discord.db"
    voice, = (corrupt / "discussions").glob("*/voice.db")
    original_bytes = (source.read_bytes(), voice.read_bytes())
    settle = ["discord", "migrate-state", "--settle-personal-ingress", "701"]
    run(rust, old_env, *settle, "--dry-run", code=1)
    run(rust, old_env, *settle, code=1)
    assert (source.read_bytes(), voice.read_bytes()) == original_bytes
    print("PASS Go-made Discord personal/team retained evidence, rollback, and verified original/default supervisor split")


if __name__=="__main__":
    if len(sys.argv)!=4:
        sys.exit("usage: discord_retained_state.py PINNED_GO RUST GO_FIXTURE")
    bins=(str(Path(arg).resolve(strict=True)) for arg in sys.argv[1:])
    with tempfile.TemporaryDirectory(prefix="ds-",dir=os.path.realpath("/tmp")) as tmp:
        inspect(*bins,Path(tmp))
