#!/usr/bin/env python3
"""Observe concurrent fake OAuth refreshes from two native Codex app-servers."""
from __future__ import annotations

import argparse
import base64
from contextlib import ExitStack, contextmanager
import hashlib
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
from urllib.parse import parse_qs

ACCOUNT = "123e4567-e89b-42d3-a456-426614174013"


def b64(value: object) -> str:
    raw = json.dumps(value, separators=(",", ":")).encode()
    return base64.urlsafe_b64encode(raw).decode().rstrip("=")


def jwt(exp: int) -> str:
    claims = {"sub": "synthetic-codex-probe-user", "exp": exp,
              "https://api.openai.com/auth": {"chatgpt_account_id": ACCOUNT,
                                               "chatgpt_plan_type": "pro"}}
    return f"{b64({'alg': 'none', 'typ': 'JWT'})}.{b64(claims)}.synthetic"


def write_auth(path: Path, exp_in: int = 60) -> None:
    exp = int(time.time()) + exp_in
    path.write_text(json.dumps({"auth_mode": "chatgpt", "tokens": {
        "id_token": jwt(exp), "access_token": jwt(exp),
        "refresh_token": "synthetic-refresh-shared", "account_id": ACCOUNT},
        "last_refresh": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}), encoding="utf-8")
    path.chmod(0o600)


def start_server() -> tuple[ThreadingHTTPServer, list[dict], threading.Event, dict]:
    observations: list[dict] = []
    lock = threading.Lock()
    both_refreshing = threading.Event()
    state = {"refresh_count": 0, "inflight": 0, "max_inflight": 0,
             "original_inflight": set(), "original_max_inflight": 0,
             "original_processes": set(), "trace": []}

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:  # noqa: N802
            with lock:
                observations.append({"method": "GET", "path": self.path})
            data = {"accounts": [{"id": ACCOUNT,
                                  "workspace_backend_origin": f"https://127.0.0.1:{self.server.server_port}",
                                  "account_routing_override": "NO_CONSTRAINT"}]}
            body = json.dumps(data if self.path.endswith("/accounts/check") else {}).encode()
            self.respond(200, body)

        def do_POST(self) -> None:  # noqa: N802
            length = int(self.headers.get("Content-Length", "0"))
            raw = self.rfile.read(length)
            process_label = self.path.rsplit("/", 1)[-1] if "/oauth/token/" in self.path else "unknown"
            decoded = raw.decode("utf-8", errors="replace")
            try:
                fields = json.loads(decoded)
            except json.JSONDecodeError:
                fields = {key: values[0] for key, values in
                          parse_qs(decoded, keep_blank_values=True).items()}
            refresh_token = fields.get("refresh_token", "") if isinstance(fields, dict) else ""
            refresh_token_matches = refresh_token == "synthetic-refresh-shared"
            is_refresh = "/oauth/token/" in self.path
            with lock:
                observations.append({"method": "POST", "path": self.path,
                                    "process_label": process_label,
                                    "synthetic_original_refresh_token": refresh_token_matches,
                                    "request_content_type": self.headers.get("Content-Type"),
                                    "request_body": decoded})
                if is_refresh:
                    state["refresh_count"] += 1
                    state["inflight"] += 1
                    state["max_inflight"] = max(state["max_inflight"], state["inflight"])
                    if refresh_token_matches:
                        state["original_inflight"].add(process_label)
                        state["original_processes"].add(process_label)
                        state["original_max_inflight"] = max(
                            state["original_max_inflight"], len(state["original_inflight"]))
                    state["trace"].append({"event": "start", "time": time.monotonic(),
                        "count": state["refresh_count"], "inflight": state["inflight"],
                        "process_label": process_label,
                        "synthetic_original_refresh_token": refresh_token_matches})
                    if len(state["original_inflight"]) >= 2:
                        both_refreshing.set()
                    if {"process-1", "process-2"}.issubset(state["original_inflight"]):
                        state["target_pair_overlapped"] = True
                        both_refreshing.set()
            if is_refresh:
                # Hold the first refresh until a second independent process arrives.
                both_refreshing.wait(timeout=12)
                now = int(time.time()) + 3600
                body = json.dumps({"access_token": jwt(now), "id_token": jwt(now),
                                   "refresh_token": "synthetic-refresh-rotated",
                                   "expires_in": 3600, "token_type": "bearer"}).encode()
                with lock:
                    state["inflight"] -= 1
                    if refresh_token_matches:
                        state["original_inflight"].discard(process_label)
                    state["trace"].append({"event": "finish", "time": time.monotonic(),
                        "inflight": state["inflight"], "process_label": process_label,
                        "synthetic_original_refresh_token": refresh_token_matches})
            else:
                body = b"{}"
            self.respond(200, body)

        def respond(self, status: int, body: bytes) -> None:
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *_args: object) -> None:
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server, observations, both_refreshing, state


@contextmanager
def app_server(codex: Path, home: Path, base_url: str, process_label: str,
               refresh_token: bool = True) -> Iterator[Callable[[], dict]]:
    env = {
        "PATH": os.defpath,
        "HOME": str(home),
        "XDG_CONFIG_HOME": str(home / "xdg-config"),
        "XDG_CACHE_HOME": str(home / "xdg-cache"),
        "XDG_DATA_HOME": str(home / "xdg-data"),
        "CODEX_HOME": str(home),
        "CODEX_REFRESH_TOKEN_URL_OVERRIDE": f"{base_url}/oauth/token/{process_label}",
        "CODEX_REVOKE_TOKEN_URL_OVERRIDE": f"{base_url}/oauth/revoke/{process_label}",
    }
    for key in ("XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"):
        Path(env[key]).mkdir(mode=0o700, exist_ok=True)
    process = subprocess.Popen([str(codex), "app-server", "--listen", "stdio://"],
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        text=True, env=env, cwd=home)
    pending = bytearray()

    def send(message: dict) -> None:
        assert process.stdin
        process.stdin.write(json.dumps(message) + "\n")
        process.stdin.flush()

    def response(request_id: int) -> dict:
        assert process.stdout
        deadline = time.monotonic() + 15
        stop_reason = "response deadline elapsed"
        while time.monotonic() < deadline:
            if b"\n" in pending:
                line, _, rest = pending.partition(b"\n")
                pending[:] = rest
            else:
                ready, _, _ = select.select([process.stdout.fileno()], [], [], max(0, deadline-time.monotonic()))
                if not ready:
                    break
                chunk = os.read(process.stdout.fileno(), 4096)
                if not chunk:
                    stop_reason = "stdout EOF"
                    break
                pending.extend(chunk)
                continue
            try:
                msg = json.loads(line)
            except json.JSONDecodeError:
                continue
            if msg.get("id") == request_id:
                if "error" in msg:
                    raise RuntimeError(msg["error"].get("message", "RPC failed"))
                return msg
        process_exit = process.poll()
        stderr = process.stderr.read().strip() if process_exit is not None and process.stderr else ""
        stderr = stderr.replace("synthetic-refresh-shared", "[synthetic token]")
        raise RuntimeError(f"app-server {stop_reason} before RPC {request_id}; process_exit={process_exit}; stderr={stderr[:1000]}")

    try:
        initialized = False
        def read_account() -> dict:
            nonlocal initialized
            try:
                if not initialized:
                    send({"method": "initialize", "id": 1, "params": {"clientInfo": {
                        "name": "synthetic-concurrency-probe", "title": "Synthetic refresh probe", "version": "0.1"}, "capabilities": {}}})
                    response(1)
                    send({"method": "initialized", "params": {}})
                    initialized = True
                send({"method": "account/read", "id": 2, "params": {"refreshToken": refresh_token}})
                return response(2).get("result", {})
            except Exception as exc:
                raise RuntimeError(f"{process_label}: {exc}") from exc
        yield read_account
    finally:
        process.terminate()
        try:
            process.wait(timeout=3)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--codex", type=Path, required=True)
    args = parser.parse_args()
    codex = args.codex.expanduser().resolve(strict=True)
    binary_hash = hashlib.sha256(codex.read_bytes()).hexdigest()
    server, observations, both_refreshing, state = start_server()
    base_url = f"http://127.0.0.1:{server.server_port}"
    try:
        with tempfile.TemporaryDirectory(prefix="swarm-codex-refresh-probe-") as td:
            home = Path(td)
            home.chmod(0o700)
            (home / "config.toml").write_text(
                'cli_auth_credentials_store = "file"\n'
                f'chatgpt_base_url = "{base_url}"\n', encoding="utf-8")
            (home / "config.toml").chmod(0o600)
            auth_path = home / "auth.json"
            # Pre-create app-server's SQLite state so simultaneous process startup
            # does not race the database's first-time initialization.
            write_auth(auth_path, exp_in=30 * 24 * 3600)
            with app_server(codex, home, base_url, "warmup", refresh_token=False) as warmup_read:
                warmup_result = warmup_read()
            write_auth(auth_path, exp_in=60)
            with ExitStack() as stack:
                process_labels = ("process-1", "process-2")
                readers = [stack.enter_context(app_server(codex, home, base_url, label))
                           for label in process_labels]
                results, errors = [], []
                ready = threading.Barrier(2)
                def invoke(read: Callable[[], dict]) -> None:
                    try:
                        ready.wait(timeout=5)
                        results.append(read())
                    except Exception as exc:
                        errors.append(repr(exc))
                threads = [threading.Thread(target=invoke, args=(read,)) for read in readers]
                started = time.monotonic()
                for thread in threads:
                    thread.start()
                for thread in threads:
                    thread.join(timeout=25)
                # Refresh can continue after account/read replies; keep both managers alive
                # until the fake OAuth handlers observe their requests.
                both_refreshing.wait(timeout=15)
                settle_deadline = time.monotonic() + 15
                while time.monotonic() < settle_deadline:
                    if state["inflight"] == 0 and state["refresh_count"] > 0:
                        break
                    time.sleep(0.05)
                elapsed = round(time.monotonic() - started, 3)
                if any(thread.is_alive() for thread in threads):
                    errors.append("RPC thread still running after 25s")
        matching_processes = sorted(state["original_processes"])
        overlap_assertion = (state.get("target_pair_overlapped", False)
                             and state["original_max_inflight"] >= 2
                             and {"process-1", "process-2"}.issubset(matching_processes))
        result = {
            "binary": str(codex), "sha256": binary_hash, "binary_bytes": codex.stat().st_size,
            "synthetic_account_id": ACCOUNT, "shared_home": True, "app_server_processes": 2,
            "sqlite_state_warmed_before_parallel_start": True,
            "warmup_account_read_succeeded": bool(warmup_result),
            "refresh_token_shared": True, "account_read_refreshToken": True,
            "oauth_refresh_requests": state["refresh_count"],
            "refresh_handler_trace": state["trace"],
            "max_simultaneous_refresh_handlers": state["max_inflight"],
            "max_simultaneous_handlers_using_original_synthetic_token": state["original_max_inflight"],
            "processes_observed_using_original_synthetic_token": matching_processes,
            "both_processes_used_original_synthetic_token": {"process-1", "process-2"}.issubset(matching_processes),
            "distinct_processes_overlapped_using_original_synthetic_token": overlap_assertion,
            "overlap_assertion_passed": overlap_assertion,
            "rpc_result_count": len(results), "rpc_errors": errors, "elapsed_seconds": elapsed,
            "requests_observed_at_loopback_backend": observations,
            "model_rpc_or_model_request_sent": False, "real_credentials_or_auth_files_touched": False,
            "configured_provider_base_url": base_url,
            "configured_oauth_refresh_urls": {label: f"{base_url}/oauth/token/{label}"
                                                for label in process_labels},
        }
        print(json.dumps(result, indent=2, sort_keys=True))
        return 0 if overlap_assertion else 1
    finally:
        server.shutdown()
        server.server_close()

if __name__ == "__main__":
    raise SystemExit(main())
