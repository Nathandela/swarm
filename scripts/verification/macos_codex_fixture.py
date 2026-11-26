"""Exact per-fixture Darwin Codex socket allowances for the native Go/Rust witness."""

import ctypes
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shlex
import socket
import stat
import struct
import subprocess

from native_fixture_sandbox import profile


GO_ID = "gocodexfixtureaa"
RESUMED_GO_ID = "gocodexfixturebb"
RUST_ID = "c0d3c01dc0d3c01dc0d3c01d"


class Guard:
    def __init__(self, root, state, port, cold, *, endpoints=None, extra_ports=(), codex_binary=None):
        self.tools = root / "bin"
        namespace = Path("/tmp").resolve() / f"codex-daemon-{os.geteuid()}"
        metadata = namespace.lstat()
        assert (stat.S_ISDIR(metadata.st_mode) and metadata.st_uid == os.geteuid()
                and stat.S_IMODE(metadata.st_mode) == 0o700)
        if endpoints is None:
            endpoints = [state / GO_ID / "codex.sock"]
            if cold:
                endpoints += [state / RESUMED_GO_ID / "codex.sock", state / "rust-v1" / RUST_ID / "b"]
        quote = lambda p: json.dumps(str(p))
        rules = [profile(root, [port, *extra_ports]),
                 f'(deny file-read* (subpath {quote(Path.home())}))',
                 f'(deny file-read* (subpath {quote(namespace)}))',
                 f'(allow file-read-metadata (literal {quote(namespace)}))']
        self.paths = []
        for endpoint in endpoints:
            alias = namespace / hashlib.sha256(os.fsencode(endpoint)).hexdigest()
            lock = alias.with_suffix(".lock")
            for path in (alias, lock):
                try:
                    path.lstat()
                except FileNotFoundError:
                    pass
                else:
                    raise AssertionError(f"fixture vendor coordinate already exists: {path}")
            self.paths.append((alias, lock))
            rules += [
                f'(allow file-read-metadata (literal {quote(alias)}) (literal {quote(lock)}))',
                f'(allow file-read-data (literal {quote(lock)}))',
                f'(allow file-write* (literal {quote(alias)}) (literal {quote(lock)}))',
                f'(allow network-bind (local unix-socket (literal {quote(alias)})))',
                f'(allow network-inbound (local unix-socket (literal {quote(alias)})))',
                f'(allow network-outbound (remote unix-socket (literal {quote(alias)})))',
            ]
        self.seatbelt = root / "codex.sb"
        self.seatbelt.write_text("\n".join(rules))
        subprocess.run(["/usr/bin/codesign", "--verify", codex_binary or self.tools / "codex"], check=True)

    def wrap(self, binary, label):
        wrapper = self.tools / label
        args = ["/usr/bin/sandbox-exec", "-f", str(self.seatbelt), str(binary)]
        wrapper.write_text("#!/bin/sh\nexec " + shlex.join(args) + ' "$@"\n')
        wrapper.chmod(0o700)
        return wrapper

    def close(self):
        for alias, lock in self.paths:
            retire_listener(alias, lock)


def retire_listener(alias, lock):
    try:
        descriptor = os.open(lock, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except FileNotFoundError:
        assert not alias.is_socket(), f"unaccompanied fixture socket preserved: {alias}"
        return
    with os.fdopen(descriptor, "rb") as locked:
        info = os.fstat(locked.fileno())
        assert stat.S_ISREG(info.st_mode) and info.st_uid == os.geteuid() and info.st_nlink == 1
        fcntl.flock(locked, fcntl.LOCK_EX | fcntl.LOCK_NB)
        try:
            listener = alias.lstat()
        except FileNotFoundError:
            pass
        else:
            assert stat.S_ISSOCK(listener.st_mode) and listener.st_uid == os.geteuid()
            with socket.socket(socket.AF_UNIX) as client:
                try:
                    client.connect(str(alias))
                except ConnectionRefusedError:
                    current = alias.lstat()
                    assert (current.st_dev, current.st_ino) == (listener.st_dev, listener.st_ino)
                    alias.unlink()
                else:
                    raise AssertionError(f"live fixture socket preserved: {alias}")
        current = lock.lstat()
        assert (current.st_dev, current.st_ino) == (info.st_dev, info.st_ino)
        lock.unlink()


def process_birth(pid):
    proc = ctypes.CDLL("/usr/lib/libproc.dylib", use_errno=True)
    # Darwin arm64 PROC_PIDTBSDINFO is 136 bytes. The last two u64s are start seconds and microseconds.
    info = ctypes.create_string_buffer(136)
    size = proc.proc_pidinfo(pid, 3, ctypes.c_uint64(0), info, len(info))
    if size != len(info):
        raise FileNotFoundError(pid)
    sec, usec = struct.unpack_from("=QQ", info, len(info) - 16)
    return sec * 1_000_000 + usec


def socket_peer(client):
    pid, = struct.unpack("i", client.getsockopt(0, 2, 4))
    uid, gid = ctypes.c_uint(), ctypes.c_uint()
    assert ctypes.CDLL(None).getpeereid(client.fileno(), ctypes.byref(uid), ctypes.byref(gid)) == 0
    return pid, uid.value
