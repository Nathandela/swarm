"""Exercise unattended connector startup with a private, previously used supervisor profile."""

from pathlib import Path
import subprocess
import sys
import tempfile

from stopped_go_state import await_value, daemon, fixture_env, owner, private


def verify(binary, root):
    home, original, ambient, place, tools = [
        root / n for n in ("home", "original", "ambient", "place", "bin")
    ]
    for path in (home, original, ambient, place, tools):
        path.mkdir(mode=0o700)
    bound = fixture_env(root, original, tools)
    other = fixture_env(root, ambient, tools)
    with daemon(binary, bound, root / "first.log"):
        expected = owner(bound)["endpoint_id"]
    config = {
        "version": 1,
        "enabled": True,
        "name": "service fixture",
        "mode": "personal",
        "installation_id": "fixture",
        "application_id": "200",
        "owner_id": "300",
        "dm_channel_id": "400",
        "manager_cli": "codex",
        "work_dir": str(home),
        "token_file": str(root / "deliberately-absent-token"),
        "daemon_socket": bound["SWARM_DAEMON_SOCK"],
        "supervisor": {
            "state_dir": str(original),
            "lock": bound["SWARM_DAEMON_LOCK"],
            "log": str(original / "log"),
        },
    }
    private(place / "config.json", config)
    private(place / ".rust-offline-owner.json", "fixture")

    def run():
        return subprocess.run(
            [binary, "discord", "run", "--state-dir", place, "--unattended"],
            env=other,
            capture_output=True,
            text=True,
            timeout=15,
            check=False,
        )

    try:
        result = run()
        assert result.returncode == 1 and "token" in result.stderr.lower(), (
            result.stderr
        )
        assert await_value(lambda: owner(bound))["endpoint_id"] == expected
        assert not Path(other["SWARM_DAEMON_SOCK"]).exists()
        assert not (place / "bridge.sock").exists()

        config["supervisor"]["state_dir"] = str(ambient)
        private(place / "config.json", config)
        refused = run()
        assert refused.returncode == 1 and "different supervisor" in refused.stderr, (
            refused.stderr
        )
        assert (
            owner(bound)["endpoint_id"] == expected
            and not Path(other["SWARM_DAEMON_SOCK"]).exists()
        )
        print(
            "Saved unattended environment started the pinned supervisor; mismatched socket identity refused"
        )
    finally:
        for env in (bound, other):
            try:
                reply = owner(
                    env,
                    {
                        "op": "owner_daemon_restart",
                        "state_dir": env["SWARM_DAEMON_STATE"],
                    },
                    caps=["owner-daemon-restart"],
                )
                assert reply["op"] == "ok", reply
            except (ConnectionError, FileNotFoundError):
                pass


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("usage: discord_service_bootstrap.py RUST_BINARY")
    with tempfile.TemporaryDirectory(prefix="ds-", dir=Path("/tmp").resolve()) as tmp:
        verify(Path(sys.argv[1]).resolve(strict=True), Path(tmp))
