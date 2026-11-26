#!/usr/bin/env python3
"""Exercise original Go-made connector evidence without using Discord."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from stopped_go_state import daemon, disk, fixture_env


def run(binary, env, *args, code=0):
    result = subprocess.run([binary, *map(str, args)], env=env, capture_output=True,
                            text=True, timeout=20)
    assert result.returncode == code, (result.returncode, result.stdout, result.stderr)
    assert "synthetic-unused-credential" not in result.stdout + result.stderr
    return result.stdout


def inspect(go, rust, fixture, base):
    home, original, destination, tools = (base / part for part in ("home", "old", "new", "bin"))
    for directory in home, original, destination, tools:
        directory.mkdir(mode=0o700)
    env = fixture_env(base, destination, tools)
    old_env = fixture_env(base, original, tools)
    old_exclude = "fixture-token"
    for mode in ("personal", "teammate"):
        connector = base / ("dormant-" + mode)
        run(fixture, env, "create", connector, original, mode, "false")
        pieces = ["discord", "migrate-state", "--state-dir", connector]
        protected = {name: (connector / name).read_bytes() for name in ("discord.db", "config.json", old_exclude)}
        before = sorted(p.name for p in connector.iterdir())
        preview = json.loads(run(rust, env, *pieces, "--dry-run"))
        assert preview["eligible"] and preview["inventory"]["mode"] == mode
        assert preview["inventory"]["exclusions"] == 1 and not preview["gateway_authority"]
        assert before == sorted(p.name for p in connector.iterdir())
        adopted = json.loads(run(rust, env, *pieces))
        assert adopted["state"] == "retained"
        assert json.loads(run(rust, env, *pieces)) == adopted
        undone = json.loads(run(rust, env, *pieces, "--rollback"))
        assert undone["state"] == "rolled_back" and not undone["gateway_authority"]
        assert protected == {name: (connector / name).read_bytes() for name in protected}
        assert run(fixture, env, "read", connector).strip() == "true"
        run(fixture, env, "change", connector)
        assert run(fixture, env, "read", connector).strip() == "false"
        changed = json.loads(run(rust, env, *pieces, "--dry-run", code=1))
        assert changed["state"] == "changed"
    unsafe = base / "pending"
    run(fixture, env, "create", unsafe, original, "teammate", "false")
    run(fixture, env, "pending", unsafe)
    before = (unsafe / "discord.db").read_bytes()
    blocked = json.loads(run(rust, env, "discord", "migrate-state", "--state-dir", unsafe, code=1))
    assert blocked["inventory"]["unresolved"] == 1 and not blocked["eligible"]
    assert before == (unsafe / "discord.db").read_bytes()
    assert not (unsafe / ".rust-go-discord-retained.json").exists()

    active = base / "transition"
    run(fixture, env, "create", active, original, "personal", "true")
    protected = {name: (active / name).read_bytes() for name in ("discord.db", old_exclude)}
    config = active / "config.json"
    old_selector = original / "discord-active.json"
    new_selector = destination / "discord-active.json"
    original_config = disk(config)
    original_selection = old_selector.read_bytes()
    command = ["discord","migrate-supervisor","--state-dir",active]
    # The supervisors are real Go daemons; the connector stays offline.
    with daemon(go, old_env, base/"old.log"), daemon(go, env, base/"new.log"):
        assert run(rust, env, *command, "--dry-run") == run(go, env, *command, "--dry-run")
        assert disk(config) == original_config and not new_selector.exists()
        run(rust, env, *command, "--from-state-dir", original, code=1)
        assert disk(config) == original_config and not new_selector.exists()
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
    assert run(fixture, env, "read", active).strip() == "true"
    print("PASS Go-made Discord personal/team retained evidence, rollback, and verified original/default supervisor split")


if __name__=="__main__":
    if len(sys.argv)!=4:
        sys.exit("usage: discord_retained_state.py PINNED_GO RUST GO_FIXTURE")
    bins=(str(Path(arg).resolve(strict=True)) for arg in sys.argv[1:])
    with tempfile.TemporaryDirectory(prefix="ds-",dir=os.path.realpath("/tmp")) as tmp:
        inspect(*bins,Path(tmp))
