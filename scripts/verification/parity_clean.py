#!/usr/bin/env python3
"""Compare the public storage cleanup command on private Go-format journal fixtures."""

import argparse
import fcntl
import json
import os
from pathlib import Path
import struct
import subprocess
import tempfile


def write(path, value):
    path.write_bytes(value if isinstance(value, bytes) else json.dumps(value).encode())
    path.chmod(0o600)


def fixture(root):
    root.mkdir(mode=0o700)
    session = root / "session-one"
    session.mkdir(mode=0o700)
    lock, spool = root / "daemon.lock", session / "hooks.spool"
    for p in (
        lock,
        session / "hooks.spool.open.lock",
        session / "hooks.spool.producer.lock",
    ):
        write(p, b"")

    def record(sequence, body):
        return struct.pack(">QI", sequence, len(body)) + body

    retired = record(1, b'{"folded":true}')
    suffix = record(2, b'{"unread":true}') + b"\0\0\x03"
    write(spool, retired + suffix)
    stat = spool.stat()
    write(session / "meta.json", {"id": session.name, "schema_version": 1})
    write(session / "hooks.spool.incarnation", {"id": "fixture-incarnation"})
    write(
        session / "hooks.spool.producer",
        {
            "device": stat.st_dev,
            "inode": stat.st_ino,
            "pid": os.getpid(),
            "start": 1,
            "closed": True,
            "incarnation": "fixture-incarnation",
            "nonce": "fixture-nonce",
        },
    )
    write(session / "hook.fold", b"1")
    write(
        session / "hook.fold.cert",
        {
            "incarnation": "fixture-incarnation",
            "producer": "fixture-nonce",
            "sequence": 1,
        },
    )
    write(session / "recovery.receipt", b"protected")
    log = root / "daemon.log"
    write(log, b"")
    with log.open("ab") as output:
        output.truncate(8 * 1024 * 1024 + 1)
    return spool, retired, suffix, lock


def arm(binary, root):
    env = {
        "HOME": str(root.parent),
        "PATH": "/usr/bin:/bin",
        "SWARM_DAEMON_STATE": str(root),
    }

    def run(*args, expected=0):
        result = subprocess.run(
            [binary, "clean", "--json", *args], env=env, capture_output=True, timeout=5
        )
        assert result.returncode == expected, (
            args,
            result.returncode,
            result.stderr.decode(),
        )
        return json.loads(result.stdout) if expected == 0 else None

    assert run("--apply")["complete"] and not root.exists()
    spool, retired, suffix, lock = fixture(root)
    log = root / "daemon.log"
    original_log = log.stat()
    snapshot = lambda: {
        str(p.relative_to(root)): (p.stat().st_ino, p.stat().st_size, p.read_bytes())
        for p in root.rglob("*")
        if p.is_file()
    }
    before = snapshot()
    preview = run()
    assert snapshot() == before

    def results(report):
        return [
            (row["kind"], row["status"], row["eligible_logical_bytes"])
            for row in report["items"]
        ]

    assert results(preview) == [
        ("diagnostic-log", "eligible", original_log.st_size),
        ("hook-prefix", "eligible", len(retired)),
    ]
    with lock.open("rb") as held:
        fcntl.flock(held, fcntl.LOCK_EX | fcntl.LOCK_NB)
        busy = run("--apply")
        assert results(busy) == [
            ("diagnostic-log", "cleaned", original_log.st_size),
            ("hook-prefix", "retained", 0),
        ]
    assert (log.stat().st_ino, log.stat().st_size) == (original_log.st_ino, 0)
    assert spool.read_bytes() == retired + suffix
    cert_file = spool.parent / "hook.fold.cert"
    cert_bytes = cert_file.read_bytes()
    real_cert = json.loads(cert_bytes)
    write(cert_file, dict(real_cert, sequence=2))
    assert results(run("--apply"))[1] == ("hook-prefix", "retained", 0)
    assert spool.read_bytes() == retired + suffix
    write(cert_file, cert_bytes)
    for name in ("hooks.spool.producer.lock", "hooks.spool.open.lock"):
        with (spool.parent / name).open("rb") as held:
            fcntl.flock(held, fcntl.LOCK_EX | fcntl.LOCK_NB)
            assert results(run("--apply"))[1] == ("hook-prefix", "retained", 0)
            assert spool.read_bytes() == retired + suffix
    original_meta = (spool.parent / "meta.json").read_bytes()
    write(spool.parent / "meta.json", {"schema_version": 98, "id": "session-one"})
    assert results(run("--apply"))[1] == ("hook-prefix", "retained", 0)
    assert spool.read_bytes() == retired + suffix
    write(spool.parent / "meta.json", original_meta)
    # A matching socket-lock descriptor in the session cannot authorize replacing itself.
    with (spool.parent / "singleton").open("wb"):
        pass
    env["SWARM_DAEMON_LOCK"] = str(spool.parent / "singleton")
    assert results(run("--apply"))[1] == ("hook-prefix", "retained", 0)
    assert spool.read_bytes() == retired + suffix
    del env["SWARM_DAEMON_LOCK"]
    applied = run("--apply")
    assert results(applied) == [
        ("diagnostic-log", "unchanged", 0),
        ("hook-prefix", "cleaned", len(retired)),
    ]
    assert spool.read_bytes() == suffix
    proof = json.loads(spool.with_suffix(".spool.producer").read_bytes())
    assert (proof["device"], proof["inode"]) == (
        spool.stat().st_dev,
        spool.stat().st_ino,
    )
    assert (spool.parent / "hook.fold").read_bytes() == b"1"
    assert (spool.parent / "recovery.receipt").read_bytes() == b"protected"
    repeat = run("--apply")
    assert results(repeat) == [
        ("diagnostic-log", "unchanged", 0),
        ("hook-prefix", "unchanged", 0),
    ]
    assert not (root / "daemon.sock").exists()
    backup = root / "unrelated"
    log.rename(backup)
    log.symlink_to(backup)
    assert results(run("--apply"))[0] == ("diagnostic-log", "retained", 0)
    assert backup.read_bytes() == b""
    return [results(v) for v in (preview, busy, applied, repeat)]


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("reference", type=Path)
    parser.add_argument("candidate", type=Path)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="sw-clean-") as tmp:
        base = Path(tmp).resolve()
        assert arm(args.reference.resolve(), base / "go") == arm(
            args.candidate.resolve(), base / "rust"
        )
    print(
        "Go/Rust clean matched absent, preview, busy, apply, replay and symlink refusal; uncertified suffix and recovery receipt preserved"
    )
