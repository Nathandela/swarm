#!/usr/bin/env python3
"""Cross-build all Rust tests and execute them on a designated Linux x86_64 host."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[2]
TARGET = "x86_64-unknown-linux-musl"


def artifacts():
    env = dict(os.environ, CARGO_TARGET_X86_64_UNKNOWN_LINUX_MUSL_LINKER="rust-lld",
               CARGO_PROFILE_TEST_DEBUG="0")
    run = subprocess.run(["cargo", "test", "--workspace", "--locked", "--target", TARGET,
                          "--no-run", "--message-format=json"], env=env, cwd=ROOT,
                         stdout=subprocess.PIPE, text=True)
    binaries = []
    tests = []
    for line in run.stdout.splitlines():
        msg = json.loads(line)
        if msg["reason"] == "compiler-message":
            print(msg["message"]["rendered"], end="")
        if msg["reason"] == "compiler-artifact" and msg.get("executable"):
            path = Path(msg["executable"])
            if not re.fullmatch(r"[A-Za-z0-9_-]+", path.name):
                raise ValueError(f"Unexpected executable name {path.name!r}")
            binaries.append(path)
            if msg["profile"]["test"]:
                tests.append(path.name)
    run.check_returncode()
    if "swarm" not in {path.name for path in binaries} or not tests:
        raise RuntimeError("Cargo did not report the application and its tests")
    if len(set(p.name for p in binaries)) != len(binaries):
        raise RuntimeError("Ambiguous test artifact name")
    return binaries, tests


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("host", help="authorized SSH hostname or alias")
    parser.add_argument("--stage-parent", choices=["/tmp", "/dev/shm"], default="/tmp",
                        help="remote filesystem for the cross-built test executables")
    parser.add_argument("--scratch-parent", default="/tmp",
                        help="existing remote filesystem for private test state and Unix sockets")
    args = parser.parse_args()
    scratch_parent = args.scratch_parent
    if (not re.fullmatch(r"/(?:[A-Za-z0-9_-][A-Za-z0-9_.-]*/)*[A-Za-z0-9_-][A-Za-z0-9_.-]*", scratch_parent)
            or len(scratch_parent) + len("/sw.XXXXXXXX") > 32):
        parser.error("use a short absolute scratch path suitable for Unix sockets")
    host = args.host
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.@-]*", host):
        parser.error("invalid SSH host")
    ssh = ["ssh", "-o", "BatchMode=yes", host]
    platform = subprocess.check_output([*ssh, "uname -sm"], text=True).strip()
    if platform != "Linux x86_64":
        raise RuntimeError(f"Expected Linux x86_64, got {platform!r}")
    binaries, tests = artifacts()
    def new_directory(parent, prefix):
        root = subprocess.check_output(
            [*ssh, f"umask 077 && mktemp -d {parent}/{prefix}.XXXXXXXX"], text=True
        ).strip()
        if not re.fullmatch(re.escape(f"{parent}/{prefix}.") + r"[A-Za-z0-9]{8}", root):
            raise RuntimeError("unexpected remote scratch path")
        return root

    root = new_directory(args.stage_parent, "sw-test")
    # Workspace identity tests need a filesystem that reports directory birth time.
    try:
        scratch = root if args.stage_parent == scratch_parent else new_directory(scratch_parent, "sw")
    except Exception:
        subprocess.run([*ssh, f"rmdir {root}"], check=False)
        raise
    print(f"Running {len(tests)} test executables on {host} in {root}, scratch {scratch}", flush=True)
    subprocess.run(["scp", "-q", "-o", "BatchMode=yes", *binaries, f"{host}:{root}/"], check=True)
    failed = []
    for name in tests:
        result = subprocess.run([*ssh, f"cd {root} && TMPDIR={scratch} SWARM_TEST_BIN={root}/swarm ./{name}"])
        if result.returncode:
            failed.append(name)
    if failed:
        raise SystemExit(f"Failed {failed}; retained binaries at {host}:{root} and scratch at {scratch}")
    # Refuse to discard unexpected files or a surviving test's scratch directory.
    names = " ".join("./" + p.name for p in binaries)
    directories = root if scratch == root else f"{root} {scratch}"
    subprocess.run([*ssh, f"cd {root} && rm -- {names} && cd / && rmdir {directories}"], check=True)


if __name__ == "__main__":
    main()
