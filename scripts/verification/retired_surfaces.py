#!/usr/bin/env python3
"""Exercise the retired public commands and negotiated capabilities on a fresh installation."""

import argparse
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location(
    "owner_wire", ROOT / "scripts/verification/opencode-rust/launch.py"
)
wire = importlib.util.module_from_spec(spec)
spec.loader.exec_module(wire)


def verify(binary, root):
    home = root / "home"
    home.mkdir(mode=0o700)
    state = root / "sessions"
    sock = state / "daemon.sock"
    env = {
        "HOME": str(home),
        "PATH": "/usr/bin:/bin",
        "SWARM_DAEMON_STATE": str(state),
        "SWARM_DAEMON_SOCK": str(sock),
    }
    for command in ("remote", "relay", "voice", "logins"):
        result = subprocess.run(
            [binary, command, "--help"], env=env, capture_output=True, timeout=3
        )
        assert result.returncode == 2 and not state.exists(), (
            command,
            result.returncode,
        )
    help_text = subprocess.check_output(
        [binary, "--help"], env=env, text=True, timeout=3
    )
    declared = set(help_text.split("[", 1)[1].split("]", 1)[0].split("|"))
    assert (
        declared.isdisjoint({"remote", "relay", "voice", "logins"})
        and {"discord", "teammate", "schedule"} <= declared
    )
    retained = subprocess.check_output(
        [binary, "discord", "--help"], env=env, text=True, timeout=3
    )
    assert "stop-call" in retained
    daemon = subprocess.Popen(
        [binary, "daemon"],
        env=env,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
    )
    try:
        deadline = time.monotonic() + 5
        while not sock.is_socket():
            if daemon.poll() is not None or time.monotonic() >= deadline:
                raise AssertionError("fresh owner socket did not start")
            time.sleep(0.02)
        requested = ("subscribe", "remote-gateway", "pairing", "policy", "activity")
        hello = wire.owner_request(env, caps=requested)
        assert hello["capabilities"] == ["subscribe"], hello
        for op in ("remote_approve", "pair", "push_register", "login_switch"):
            reply = wire.owner_request(env, dict(op=op), caps=requested)
            assert reply["op"] == "error", (op, reply)
        assert not any(
            p.name
            in {"gateway.sock", "remote.sock", "pairing", "login-worker", "logins"}
            for p in root.rglob("*")
        )
    finally:
        daemon.terminate()
        daemon.wait(timeout=5)
    print(
        "Retired commands and capability offers refused; retained Discord call help and owner subscription present"
    )


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="sr-", dir=os.path.realpath("/tmp")) as tmp:
        verify(args.binary.resolve(), Path(tmp))
