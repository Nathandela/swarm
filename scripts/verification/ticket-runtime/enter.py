"""Runs inside bwrap. Its only provider bridge has one fixed local IPC target."""

import os
import socket
import subprocess
import sys
import time

mode = sys.argv[1]
if mode == "backend":
    bridge = subprocess.Popen(
        [
            "/usr/bin/socat",
            "TCP-LISTEN:29992,bind=127.0.0.1,reuseaddr,fork",
            "UNIX-CONNECT:/caps/provider.sock",
        ],
        start_new_session=True,
    )
    for _ in range(100):
        if bridge.poll() is not None:
            raise SystemExit("provider bridge stopped")
        try:
            with socket.create_connection(("127.0.0.1", 29992), timeout=0.05):
                break
        except OSError:
            time.sleep(0.01)
    else:
        raise SystemExit("provider bridge unavailable")
    args = ["/native/codex", "app-server", "--listen", "unix:///native-rpc/codex.sock"]
elif mode == "frontend":
    args = ["/native/codex", *sys.argv[2:], "--remote", "unix:///native-rpc/codex.sock"]
else:
    raise SystemExit("unsupported mode")
os.execv(args[0], args)
