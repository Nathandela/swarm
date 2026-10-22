#!/usr/bin/env python3
"""Probe Codex app-server auth caching with fake tokens and a loopback backend."""

from __future__ import annotations

import argparse
import base64
import hashlib
from contextlib import contextmanager
import json
import os
from pathlib import Path
import select
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Callable, Iterator


ACCOUNT_A = "123e4567-e89b-42d3-a456-426614174013"
ACCOUNT_B = "123e4567-e89b-42d3-a456-426614174014"


def _b64(value: object) -> str:
    raw = json.dumps(value, separators=(",", ":")).encode()
    return base64.urlsafe_b64encode(raw).decode().rstrip("=")


def _jwt(account_id: str) -> str:
    claims = {
        "sub": "synthetic-codex-probe-user",
        "exp": int(time.time()) + 30 * 86400,
        "https://api.openai.com/auth": {
            "chatgpt_account_id": account_id,
            "chatgpt_plan_type": "pro",
        },
    }
    return f"{_b64({'alg': 'none', 'typ': 'JWT'})}.{_b64(claims)}.synthetic"


def _write_fake_auth(path: Path, account_id: str) -> None:
    path.write_text(
        json.dumps(
            {
                "auth_mode": "chatgpt",
                "tokens": {
                    "id_token": _jwt(account_id),
                    "access_token": _jwt(account_id),
                    "refresh_token": f"synthetic-refresh-{account_id}",
                    "account_id": account_id,
                },
                "last_refresh": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            }
        ),
        encoding="utf-8",
    )
    path.chmod(0o600)


def _start_server() -> tuple[ThreadingHTTPServer, list[str]]:
    requests: list[str] = []

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:  # noqa: N802
            requests.append(self.path)
            payload = {
                "accounts": [
                    {
                        "id": account_id,
                        "workspace_backend_origin": f"https://127.0.0.1:{self.server.server_port}",
                        "account_routing_override": "NO_CONSTRAINT",
                    }
                    for account_id in (ACCOUNT_A, ACCOUNT_B)
                ]
            }
            body = json.dumps(
                payload if self.path.endswith("/accounts/check") else {}
            ).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_POST(self) -> None:  # noqa: N802
            self.rfile.read(int(self.headers.get("Content-Length", "0")))
            requests.append(self.path)
            body = b"{}"
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *_args: object) -> None:
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server, requests


@contextmanager
def _app_server(
    codex: Path, home: Path, base_url: str
) -> Iterator[Callable[[], dict]]:
    env = {key: os.environ[key] for key in ("PATH", "TMPDIR", "SYSTEMROOT") if key in os.environ}
    env["HOME"] = str(home)
    env["XDG_CONFIG_HOME"] = str(home / "config")
    env["XDG_DATA_HOME"] = str(home / "data")
    env["XDG_CACHE_HOME"] = str(home / "cache")
    env["CODEX_HOME"] = str(home)
    env["CODEX_REFRESH_TOKEN_URL_OVERRIDE"] = f"{base_url}/oauth/token"
    env["CODEX_REVOKE_TOKEN_URL_OVERRIDE"] = f"{base_url}/oauth/revoke"

    process = subprocess.Popen(
        [str(codex), "app-server", "--listen", "stdio://"],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        text=True,
        env=env,
        cwd=home,
    )
    pending = bytearray()

    def send(message: dict) -> None:
        assert process.stdin is not None
        process.stdin.write(json.dumps(message) + "\n")
        process.stdin.flush()

    def response(request_id: int) -> dict:
        assert process.stdout is not None
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if b"\n" in pending:
                line, _, remainder = pending.partition(b"\n")
                pending[:] = remainder
            else:
                readable, _, _ = select.select(
                    [process.stdout.fileno()],
                    [],
                    [],
                    max(0, deadline - time.monotonic()),
                )
                if not readable:
                    break
                chunk = os.read(process.stdout.fileno(), 4096)
                if not chunk:
                    break
                pending.extend(chunk)
                continue
            try:
                message = json.loads(line)
            except json.JSONDecodeError:
                continue
            if message.get("id") == request_id:
                if "error" in message:
                    raise RuntimeError(message["error"].get("message", "RPC failed"))
                return message
        raise TimeoutError(f"app-server did not answer RPC {request_id}")

    next_id = 2
    try:
        send(
            {
                "method": "initialize",
                "id": 1,
                "params": {
                    "clientInfo": {
                        "name": "offline-auth-reload-probe",
                        "title": "Offline auth reload probe",
                        "version": "0.1",
                    },
                    "capabilities": {},
                },
            }
        )
        response(1)
        send({"method": "initialized", "params": {}})

        def account_read() -> dict:
            nonlocal next_id
            request_id = next_id
            next_id += 1
            send(
                {
                    "method": "account/read",
                    "id": request_id,
                    "params": {"refreshToken": False},
                }
            )
            return response(request_id).get("result", {})

        yield account_read
    finally:
        process.terminate()
        try:
            process.wait(timeout=3)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()


def _account_id(result: dict) -> str:
    return result["workspaceRouting"]["chatgptAccountId"]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--codex", type=Path, required=True)
    args = parser.parse_args()
    codex = args.codex.expanduser().resolve()
    if not codex.is_file():
        parser.error(f"Codex binary not found: {codex}")

    server, requests = _start_server()
    base_url = f"http://127.0.0.1:{server.server_port}"
    try:
        with tempfile.TemporaryDirectory(prefix="codex-auth-reload-probe-") as td:
            home = Path(td)
            (home / "config.toml").write_text(
                'cli_auth_credentials_store = "file"\n'
                f'chatgpt_base_url = "{base_url}"\n',
                encoding="utf-8",
            )
            auth_path = home / "auth.json"
            _write_fake_auth(auth_path, ACCOUNT_A)

            with _app_server(codex, home, base_url) as read_account:
                first = _account_id(read_account())
                _write_fake_auth(auth_path, ACCOUNT_B)
                after_replace = _account_id(read_account())

            # The prior process has exited; a fresh one now loads account B.
            with _app_server(codex, home, base_url) as read_account:
                after_restart = _account_id(read_account())

        result = {
            "binary": str(codex),
            "binary_sha256": hashlib.sha256(codex.read_bytes()).hexdigest(),
            "observed_at_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "home_and_cwd_isolated": True,
            "network_boundary": "Backend and OAuth URLs configured to loopback; no packet capture or OS network sandbox asserted",
            "synthetic_accounts": {"A": ACCOUNT_A, "B": ACCOUNT_B},
            "first_process_account_id": first,
            "after_auth_file_replace_account_id": after_replace,
            "after_process_restart_account_id": after_restart,
            "same_process_kept_cached_account": first == after_replace == ACCOUNT_A,
            "restart_loaded_replacement_account": after_restart == ACCOUNT_B,
            "backend_requests_recorded_by_loopback_server": requests,
            "oauth_refresh_endpoint_called": any(
                path.endswith("/oauth/token") for path in requests
            ),
            "model_rpc_requested_by_probe": False,
            "prior_nonisolated_attempt": {
                "result": "401 during workspace-routing discovery without chatgpt_base_url override",
                "external_request_made": True,
                "model_request_sent": False,
                "real_credentials_or_auth_files_touched": False,
            },
        }
        print(json.dumps(result, indent=2, sort_keys=True))
        return 0 if result["same_process_kept_cached_account"] and result[
            "restart_loaded_replacement_account"
        ] else 1
    finally:
        server.shutdown()
        server.server_close()


if __name__ == "__main__":
    raise SystemExit(main())
