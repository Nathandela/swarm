#!/usr/bin/env python3
"""Exercise the pinned Go v0.29.25 gap writer and retained Rust ledger gates."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

from discord_retained_guild_prepare import serve_discord
from discord_retained_native import run
from stopped_go_state import fixture_env


def witness(root, fixture, rust_test):
    for name in ("home", "work", "bin", "state"):
        (root / name).mkdir(mode=0o700)
    place = root / "connector"
    env = fixture_env(root, root / "state", root / "bin")
    rest, _, _, posts = serve_discord()
    port = str(rest.server_port)
    source = place / "discord.db"

    def go(*args):
        return run(fixture, env, *args)

    def state():
        return json.loads(go("ingress-state", place, "original-room"))

    def rust(phase):
        run(rust_test, env | {"SWARM_TEST_DISCORD_ACTIVE_ROOT": str(root / "state"),
                              "SWARM_TEST_DISCORD_REST_PORT": port,
                              "SWARM_TEST_DISCORD_GUILD_PHASE": phase},
            "native_go_retained_guild_preparation", "--ignored", "--nocapture")

    def refuse():
        before = source.read_bytes()
        rust("gap-refuse")
        assert source.read_bytes() == before and posts == []

    try:
        go("create", place, root / "state", "teammate", "true", root / "work")
        go("bind-thread", place, "original-room", "401", "399")
        go("ingress", place, "original-room", "501", "settle")
        go("guild-gap-start", place)

        live, older = rest.messages["701"], rest.messages["501"]
        page = [live] + [dict(live, id=str(n)) for n in range(700, 601, -1)]
        rest.history = lambda path: 403 if "before=" in path else page
        partial = subprocess.run([fixture, "guild-advance", place, port, "repair-gap"],
                                 env=env, capture_output=True, timeout=10)
        assert partial.returncode != 0 and b"API status 403" in partial.stderr, partial.stderr
        binding = state()["binding"]
        assert binding["GapMissed"] == 1 and binding["RepairTargetEpoch"] == 1
        assert binding["RepairEpoch"] == 0 and binding["RepairAfter"] == "602"
        refuse()
        print("PASS Rust refused Go's partially repaired GapMissed with no write", flush=True)

        go("guild-record", place, "original-room", "701", "201", "fixture external")
        rest.history = lambda _: [older]
        go("guild-advance", place, port, "repair-gap")
        go("guild-advance", place, port, "stage")
        go("guild-gap-settle", place)
        binding = state()["binding"]
        assert binding["GapMissed"] == binding["RepairEpoch"] == binding["NotifiedEpoch"] == 1
        assert binding["Boundary"] == "701"
        refuse()
        print("PASS Rust refused Go's queued gap notice with no write", flush=True)

        go("guild-gap-seal", place)
        receipt = go("guild-gap-receipt", place)
        assert json.loads(receipt)["DiscordMessageIDs"] == ["702"] and posts == []
        settled = source.read_bytes()
        for damage in ("gap-type", "gap-alias", "gap-repair-alias", "gap-sequence-type", "gap-future"):
            go("guild-damage-binding", place, damage)
            try:
                if damage == "gap-repair-alias":
                    assert state()["binding"]["RepairEpoch"] == 0
                refuse()
            finally:
                source.write_bytes(settled)
        print("PASS malformed, case-aliased and future GapMissed values left the ledger untouched", flush=True)

        current = dict(live, id="703", content="New follow-up after Go repaired its history.")
        first = True
        def history(_):
            nonlocal first
            if first:
                first = False
                return [live]
            return [current, live]
        rest.history = history
        rust("gap-capture")
        rust("gap-check")
        recovered = state()
        assert recovered["binding"]["GapMissed"] == 1
        assert go("guild-gap-receipt", place) == receipt
        assert len(recovered["pending"]) == 1 and recovered["pending"][0]["Route"]["message_id"] == "703"
        assert recovered["pending"][0]["State"] == "prepared" and posts == []
        print("PASS retained Rust capture preserved the Go marker and exact compacted receipt; no Discord POSTs", flush=True)
    finally:
        rest.shutdown()
        rest.server_close()


if __name__ == "__main__":
    assert os.stat("/proc/self/ns/net").st_ino != int(os.environ["SWARM_NATIVE_FIXTURE_HOST_NETNS"])
    with tempfile.TemporaryDirectory(prefix="gp&gap-") as directory:
        witness(Path(directory).resolve(), *sys.argv[1:])
