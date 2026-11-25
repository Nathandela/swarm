#!/usr/bin/env python3
"""Compare Rust read verbs with the pinned Go CLI on one isolated Go daemon."""

from importlib.machinery import SourceFileLoader
from importlib.util import module_from_spec, spec_from_loader
from pathlib import Path
import argparse
import shutil
import subprocess
import sys
import tempfile
import time

sys.dont_write_bytecode = True
driver = Path(__file__).resolve().parent / "parity/swarm-parity"
spec = spec_from_loader("parity_driver", SourceFileLoader("parity_driver", str(driver)))
p = module_from_spec(spec)
spec.loader.exec_module(p)


def compare(reference, candidate):
    root = Path(tempfile.mkdtemp(prefix="si-", dir=Path("/tmp").resolve()))
    d = None
    children = []
    previous = {}
    try:
        for name in ["home", "runtime", "tmp", "work", "state", "bin", "effects"]:
            (root / name).mkdir(mode=0o700)
        go = root / "bin/go-swarm"
        rust = root / "bin/rust-swarm"
        for source, target in [(reference, go), (candidate, rust), (p.FIXTURE, root / "bin/claude")]:
            shutil.copyfile(source, target)
            target.chmod(0o700)
        guard = p.guard(root)
        env = p.environment(root)
        def run(bin, *args):
            return subprocess.run([*guard, bin, *args], env=env, cwd=root/"work", capture_output=True, timeout=6)
        def snapshot(res):
            return res.returncode, res.stdout, res.stderr
        def same(*args, status=0):
            ref = snapshot(run(go, *args))
            current = snapshot(run(rust, *args))
            assert ref[0] == status and ref == current, (args, ref, current)
            print(args, "exit", status, "identical")
            return ref[1]
        p.table()
        same("ls", "--json", status=1)
        with (root / "daemon.stderr").open("wb") as err:
            d = subprocess.Popen([*guard, go, "daemon"], env=env, cwd=root/"work", stderr=err)
            p.wait_for(lambda: (root/"d.sock").exists(), "owner socket")
            same("ls")
            same("ls", "--json")
            spawn = run(go, "spawn", "--cli", "claude", "--name", "amber & clay", "--dir", str(root/"work"), "--prompt", "offline fixture")
            assert spawn.returncode == 0, snapshot(spawn)
            session = spawn.stdout.decode().strip()
            p.wait_for(lambda: (root/"effects"/"worker").exists(), "worker started")
            p.wait_for(lambda: b"fixture ready" in run(go, "peek", session).stdout, "fixture screen")
            time.sleep(.4)
            same("ls")
            same("ls", "--json")
            same("peek", session)
            same("peek", session, "--lines", "0x3")
            same("peek", session, "--lines", "0", status=2)
            same("peek", session, "--lines", "0", "--lines", "1")
            same("peek", session, "--lines", "garbage", status=2)
            same("peek", session, "--help", status=2)
            same("ls", "--help", status=2)
            same("ls", "--json=perhaps", status=2)
            same("ls", "---json", status=2)
            same("watch", "--help", status=1)
            same("watch", session, "--timeout", "5seconds", status=1)
            same("peek", "mismatch/missing", status=1)
            same("watch", session, "--until", "change", "--timeout", "5ms", status=2)
            same("watch", "missing", "--timeout", "5ms", status=1)
            for binary in (go, rust):
                children.append(subprocess.Popen(
                    [*guard, binary, "watch", session, "--until", "completed", "--timeout", "5s"],
                    env=env, cwd=root / "work", stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                ))
            time.sleep(.3)
            killed = run(go, "kill", session)
            assert killed.returncode == 0, snapshot(killed)
            events = [(*child.communicate(timeout=6), child.returncode) for child in children]
            assert events[0] == events[1] and events[0][2] == 0, events
            print("watch completed via live event", "exit 0 identical")
            same("watch", "--until=completed", session)
    finally:
        if d and d.poll() is None:
            d.terminate()
            d.wait(timeout=5)
        for child in children:
            if child.poll() is None:
                child.terminate()
                child.communicate(timeout=5)
        try:
            cleanup = p.reap(root, None, previous)
            print("cleanup", cleanup)
            if cleanup["remaining"]:
                raise RuntimeError("fixture processes survived cleanup")
        finally:
            shutil.rmtree(root)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--reference", required=True, type=p.binary_path)
    parser.add_argument("--candidate", required=True, type=p.binary_path)
    args = parser.parse_args()
    compare(args.reference, args.candidate)
