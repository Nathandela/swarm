"""Only the trusted host can reach the controller. Each listener belongs to one launch."""

from contextlib import closing
import http.client
import http.server
import json
import os
import selectors
import socket
import socketserver
import subprocess
import threading
from binding import require


class LocalHTTP(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("voice", timeout=10)
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


class Worker(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        svc = self.server
        if (
            self.path != "/worker/message"
            or self.headers.get("Authorization") != "Bearer " + svc.local_token
            or self.headers.get("Swarm-Worker-Session") != svc.sid
        ):
            self.send_error(403)
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            require(
                0 < length <= 16384 and self.headers.get("Transfer-Encoding") is None,
                "bad frame",
            )
            body = self.rfile.read(length)
            parsed = json.loads(body)
            require(
                isinstance(parsed, dict)
                and not (
                    parsed.keys() - {"id", "text", "kind", "read_receipt", "read_reply"}
                ),
                "bad fields",
            )
            active, role, token = svc.current()
            require(role == "worker", "not a worker")
            with closing(LocalHTTP(active["bridge_socket"])) as client:
                client.request(
                    "POST",
                    "/worker/message",
                    body,
                    {
                        "Authorization": "Bearer " + token,
                        "Swarm-Worker-Session": svc.sid,
                        "Content-Type": "application/json",
                    },
                )
                response = client.getresponse()
                data = response.read(16385)
                require(len(data) <= 16384, "response too large")
                self.send_response(response.status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)
        except (ValueError, OSError, KeyError, http.client.HTTPException):
            self.send_error(403)


class Manager(socketserver.BaseRequestHandler):
    def handle(self):
        svc = self.server
        active, role, _ = svc.current()
        require(role == "dispatcher", "not a dispatcher")
        cmd = [
            svc.swarm,
            "voice",
            "mcp",
            "--socket",
            active["bridge_socket"],
            "--auth-file",
            active["auth_file"],
            "--conversation",
            active["conversation"],
        ]
        with subprocess.Popen(
            cmd,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            env={"PATH": "/usr/bin:/bin", "SWARM_SESSION_ID": svc.sid},
        ) as child:
            try:
                # Recheck the exact grant on every incoming frame fragment, even on a long-lived stream.
                with selectors.DefaultSelector() as poll:
                    poll.register(self.request, selectors.EVENT_READ)
                    poll.register(child.stdout, selectors.EVENT_READ)
                    while child.poll() is None:
                        for key, _ in poll.select(0.25):
                            if key.fileobj is self.request:
                                data = self.request.recv(65536)
                                if not data:
                                    return
                                svc.current()
                                child.stdin.write(data)
                                child.stdin.flush()
                            else:
                                data = os.read(child.stdout.fileno(), 65536)
                                if not data:
                                    return
                                self.request.sendall(data)
                        svc.current()
            finally:
                child.terminate()
                try:
                    child.wait(timeout=1)
                except subprocess.TimeoutExpired:
                    child.kill()


class RoleServer(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True

    def verify_request(self, request, address):
        return self.slots.acquire(blocking=False)

    def process_request_thread(self, request, address):
        try:
            super().process_request_thread(request, address)
        finally:
            self.slots.release()

    def __init__(self, path, role, current, swarm, sid, local_token):
        self.slots = threading.BoundedSemaphore(4)
        self.current = current
        self.swarm = swarm
        self.sid = sid
        self.local_token = local_token
        super().__init__(str(path), Manager if role == "dispatcher" else Worker)
        os.chmod(path, 0o600)

    def start(self):
        threading.Thread(target=self.serve_forever, daemon=True).start()
