#!/usr/bin/env python3
"""Original Go managed personal room with an installed, loopback-only Codex."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

from parity_codex import model_fixture
from stopped_go_state import await_value, daemon, fixture_env, listed, owner


def run(bin, env, *args):
    result = subprocess.run([bin, *map(str, args)], env=env, capture_output=True,
                            text=True, timeout=92)
    assert result.returncode == 0, (args[0], result.returncode, result.stderr)
    return result.stdout


def exercise(go, fixture, codex, rust_test, base):
    assert os.environ.get("SWARM_NATIVE_FIXTURE_OS_ISOLATED") == "1"
    assert os.stat("/proc/self/ns/net").st_ino != int(os.environ["SWARM_NATIVE_FIXTURE_HOST_NETNS"])
    for part in ("home", "home/.codex", "bin", "work", "state"):
        (base / part).mkdir(mode=0o700)
    (base / "bin/codex").symlink_to(codex)
    env = fixture_env(base, base / "state", base / "bin")
    env["PATH"] += ":/usr/sbin:/sbin"
    env["TERM"] = "xterm-256color"
    source, calls = model_fixture(base, False)
    try:
        place = base / "connector"
        run(fixture, env, "create", place, base / "state", "personal", "true", base / "work")
        run(fixture, env, "bind", place, "original-room", "400")
        assert run(fixture, env, "ingress", place, "original-room", "501", "settle").strip() == "1"
        with daemon(go, env, base / "go.log"):
            try:
                binding = json.loads(run(fixture, env, "activate", place, "original-room"))
                session = binding["session_id"]
                row, = [r for r in listed(env) if r["id"] == session]
                assert row["role"] == "manager" and row["status"]["Process"] == "running"
                assert row["manager_conversation_id"] == binding["conversation_id"]
                voice, = (place / "discussions").glob("*/voice.db")
                auth = voice.parent / "bridge.auth"
                assert auth.stat().st_mode & 0o777 == 0o600
                original_credential = auth.read_bytes()
                assert calls == []
                test_env = env | {"SWARM_TEST_DISCORD_ACTIVE_ROOT": str(base / "state"),
                                  "SWARM_TEST_DISCORD_ACTIVE_FIXTURE": fixture}
                run(rust_test, test_env, "retained_original_go_native_manager_roundtrip", "--nocapture")
                await_value(lambda: len(calls) >= 1)
                reopened = json.loads(run(fixture, env, "activate", place, "original-room"))
                # Opening the Go voice database advances its controller generation.
                # The supervisor process, PTY, account, native observation and session
                # incarnations must all remain the originals.
                assert reopened.pop("generation") == binding.pop("generation") + 1
                assert reopened == binding
                assert auth.read_bytes() == original_credential
                assert len(calls) == 1
                assert run(fixture, env, "ingress", place, "original-room", "701", "replay").strip() == "0"
            except Exception:
                print("Pinned Go supervisor log:", (base / "go.log").read_text()[-8000:], file=sys.stderr)
                for row in listed(env):
                    print({k: row.get(k) for k in ("agent", "role", "status", "conversation_id")}, file=sys.stderr)
                raise
            finally:
                for row in listed(env):
                    if row["role"] == "manager":
                        owner(env, {"op": "kill", "session_id": row["id"]})
    finally:
        source.shutdown()
        source.server_close()


if __name__ == "__main__":
    go, fixture, codex, rust_test = (str(Path(arg).resolve(strict=True)) for arg in sys.argv[1:])
    with tempfile.TemporaryDirectory(prefix="dn-", dir="/tmp") as directory:
        exercise(go, fixture, codex, rust_test, Path(directory))
    print("PASS Rust drove one installed native Codex turn through the retained Go manager binding")
