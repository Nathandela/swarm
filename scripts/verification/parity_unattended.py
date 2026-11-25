#!/usr/bin/env python3
"""Drive unattended daemon replacement with two differently versioned Rust builds."""
import argparse
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

sys.dont_write_bytecode = True
from parity_ipc import p


def exercise(root):
    before, after = root / "bin/swarm", root / "bin/board"
    env = p.environment(root)
    timer = dict(env, PATH="/timer-fixture-no-providers", ANTHROPIC_API_KEY="timer-must-not-win")
    saved = root / "state/daemon.env"

    def run(binary, args, environment, code=0):
        result = subprocess.run([binary, *args], env=environment, cwd=root / "work",
                                capture_output=True, text=True, timeout=12)
        assert result.returncode == code, (args, result.returncode, result.stderr)
        return (result.stdout or result.stderr).strip()

    def converge(binary=after, code=0):
        return run(binary, ["daemon", "restart", "--unattended"], timer, code)

    assert run(before, ["version"], env) != run(after, ["version"], env)
    assert "no daemon holds the lock" in converge()
    assert not saved.exists() and not (root / "d.sock").exists()
    with (root / "stderr").open("w") as log:
        daemon = subprocess.Popen([before, "daemon"], cwd=root / "work", env=env, stderr=log)
    try:
        p.wait_for(lambda: (root / "d.sock").exists() and saved.exists(), "interactive supervisor")
        assert "already runs this build" in converge(before)
        original = saved.read_bytes()
        worker = run(before, ["spawn", "--cli", "claude", "--dir", str(root / "work"),
                              "--name", "restart-test", "--prompt", "offline fixture"], env)
        assert "deferred" in converge(code=2)
        assert daemon.poll() is None
        directory = root / "state/rust-v1" / worker.split("/")[-1]
        observation = directory / "observation.json"
        token = json.loads((directory / "session.json").read_text())["token"]
        idle = {"token": token, "status": {"Process":"running", "Turn":"idle", "Interaction":"none"}}
        observation.write_text(json.dumps(idle))
        observation.chmod(0o600)
        p.wait_for(lambda: "ready_for_review" in run(before, ["ls","--json"], env), "idle roster projection")
        observation.write_text(json.dumps(dict(idle, token="different-owner")))
        assert "deferred" in converge(code=2)
        observation.unlink()
        assert "deferred" in converge(code=2)
        run(before, ["kill", worker], env)
        p.wait_for(lambda: all(row["status"]["Process"] == "exited"
                               for row in json.loads(run(before, ["ls", "--json"], env))), "worker exit")
        # A directory omitted from the loaded roster cannot be declared idle.
        unknown = root / "state/rust-v1" / ("f" * 24)
        unknown.mkdir(mode=0o700)
        assert "cannot establish custody" in converge(code=2)
        unknown.rmdir()
        saved.unlink()
        assert "refused" in converge(code=3)
        saved.touch(mode=0o600)
        assert "refused" in converge(code=3)
        saved.write_text("UNKNOWN_TIMER_SELECTOR=1\n")
        assert "failed" in converge(code=1)
        assert daemon.poll() is None
        saved.write_bytes(original)
        unusable_log = dict(timer, SWARM_DAEMON_LOG=str(root / "work"))
        assert "failed" in run(after, ["daemon", "restart", "--unattended"], unusable_log, 1)
        assert daemon.poll() is None and run(before, ["ls","--json"], env)
        assert "restarted from the saved environment" in converge()
        daemon.wait(timeout=5)
        assert saved.read_bytes() == original
        assert "already runs this build" in converge()
        assert worker in run(after, ["ls", "--json"], env)
        print("absent, same build, busy, unknown custody, missing/empty/invalid environment and replacement passed")
    finally:
        if daemon.poll() is None:
            daemon.terminate()
            daemon.wait(timeout=5)


def verify(before, after):
    root = Path(tempfile.mkdtemp(prefix="ur-", dir=Path("/tmp").resolve()))
    previous = {}
    try:
        for directory in ("home", "bin", "bin/parity", "state", "runtime", "tmp", "work", "effects"):
            (root / directory).mkdir(mode=0o700)
        for source, name in [(before, "swarm"), (after, "board"), (p.FIXTURE, "claude"),
                             (Path(__file__), "parity_unattended.py"),
                             (Path(__file__).with_name("parity_ipc.py"), "parity_ipc.py"),
                             (Path(p.__file__), "parity/swarm-parity")]:
            shutil.copyfile(source, root / "bin" / name)
            (root / "bin" / name).chmod(0o700)
        p.table()
        subprocess.run([*p.guard(root), "/usr/bin/python3", "-B", root / "bin/parity_unattended.py",
                        "--isolated-root", root], cwd=root / "work", env=p.environment(root),
                       check=True, timeout=45)
    finally:
        cleanup = p.reap(root, None, previous)
        if cleanup["remaining"]:
            raise RuntimeError(f"private test processes survived at {root}")
        shutil.rmtree(root)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--before", type=p.binary_path)
    parser.add_argument("--after", type=p.binary_path)
    parser.add_argument("--isolated-root", type=Path, help=argparse.SUPPRESS)
    args = parser.parse_args()
    if args.isolated_root:
        exercise(args.isolated_root)
    elif args.before and args.after:
        verify(args.before, args.after)
    else:
        parser.error("provide --before and --after binaries with different SWARM_BUILD_VERSION values")
