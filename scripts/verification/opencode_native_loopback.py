"""Actual OpenCode/Swarm integration against a deterministic loopback provider.
No external provider is enabled; responses are scripted test data, not model output.
"""

import argparse, shutil, hashlib, http.client, http.server, json, os, pathlib, secrets, signal, socket, sqlite3, struct, subprocess, tempfile, threading, time, traceback

if not __debug__:
    raise SystemExit(
        "This qualification requires Python assertions; remove -O/PYTHONOPTIMIZE."
    )

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--swarm", required=True, type=pathlib.Path)
parser.add_argument("--opencode", required=True, type=pathlib.Path)
parser.add_argument("--expected-opencode-version", required=True)
parser.add_argument("--run-dir", required=True, type=pathlib.Path)
parser.add_argument("--source-head", required=True)
parser.add_argument("--duration-seconds", type=int, default=0)
parser.add_argument("--max-iterations", type=int, default=500)
parser.add_argument("--interval-seconds", type=float, default=10)
parser.add_argument("--restart-every", type=int, default=20)
parser.add_argument("--variant", choices=["max", "default"], default="max")
parser.add_argument(
    "--tool-finish", choices=["tool_calls", "stop"], default="tool_calls"
)
parser.add_argument("--final-delay-seconds", type=float, default=0)
parser.add_argument("--max-swarm-rss-mib", type=int, default=256)
parser.add_argument("--max-swarm-fds", type=int, default=128)
parser.add_argument("--max-swarm-disk-mib", type=int, default=256)
parser.add_argument("--max-dispatch-seconds", type=float, default=30)
parser.add_argument("--max-rejoin-seconds", type=float, default=15)
args = parser.parse_args()
if any(
    x <= 0
    for x in [
        args.max_swarm_rss_mib,
        args.max_swarm_fds,
        args.max_swarm_disk_mib,
        args.max_dispatch_seconds,
        args.max_rejoin_seconds,
    ]
):
    parser.error("resource budgets must be positive")
if args.expected_opencode_version != "1.19.3":
    parser.error("this public plugin contract is qualified only for OpenCode 1.19.3")
if not args.swarm.is_file() or not args.opencode.is_file():
    parser.error("explicit existing binaries are required")
LSOF = shutil.which("lsof") or (
    "/usr/sbin/lsof" if pathlib.Path("/usr/sbin/lsof").exists() else None
)
if not LSOF:
    parser.error("lsof is required for owned-process resource measurements")
if not 0 <= args.final_delay_seconds <= 5:
    parser.error("final delay must be bounded")
if (
    not 0 <= args.duration_seconds <= 259200
    or not 3 <= args.max_iterations <= 100000
    or not 0 <= args.interval_seconds <= 60
    or args.restart_every < 0
):
    parser.error("invalid bounded fixture limits")
RUN = (
    args.run_dir.resolve()
    if args.run_dir
    else pathlib.Path(tempfile.gettempdir()).resolve() / ("snf-" + secrets.token_hex(4))
)
if len(str(RUN / "s" / ("x" * 16) / "oc.sock").encode()) > 100:
    parser.error("run directory is too long for the private native Unix socket")
RUN.mkdir(mode=0o700)
started_mono = time.monotonic()
started_wall = time.time()
deadline = started_mono + args.duration_seconds if args.duration_seconds else None
stop_requested = False


def request_stop(_signal, _frame):
    global stop_requested
    stop_requested = True


signal.signal(signal.SIGTERM, request_stop)
(RUN / "runner.pid").write_text(str(os.getpid()) + "\n")
budgets = {
    "swarm_rss_mib_per_process": args.max_swarm_rss_mib,
    "swarm_fds_per_process": args.max_swarm_fds,
    "swarm_disk_mib": args.max_swarm_disk_mib,
    "dispatch_seconds": args.max_dispatch_seconds,
    "rejoin_seconds": args.max_rejoin_seconds,
}
(RUN / "declared-budgets.json").write_text(json.dumps(budgets, indent=2) + "\n")
print(
    json.dumps(
        {
            "budgets": budgets,
            "event": "started",
            "run": str(RUN),
            "pid": os.getpid(),
            "duration_seconds": args.duration_seconds,
            "source_head": args.source_head,
        }
    ),
    flush=True,
)
STATE = RUN / "s"
PROJECT = RUN / "project"
PROFILE = RUN / "profile"
BIN = RUN / "bin"
for p in [
    STATE,
    PROJECT,
    BIN,
    PROFILE / "config/opencode",
    PROFILE / "data",
    PROFILE / "cache",
    PROFILE / "state/opencode",
    RUN / "home",
]:
    p.mkdir(parents=True, exist_ok=True)
SWARM = args.swarm.resolve()
NATIVE = args.opencode.resolve()
requests = []
request_lock = threading.Lock()
events = []
statuses = []
errors = []
event_counts = {}
metrics = []
turns = []
INITIAL = "SWARM_LOCAL_INITIAL_" + secrets.token_hex(8)
SEMANTIC = "SWARM_LOCAL_SEMANTIC_" + secrets.token_hex(8)
HIERARCHY = "SWARM_LOCAL_HIERARCHY_" + secrets.token_hex(8)
CHILD = "SWARM_LOCAL_CHILD_" + secrets.token_hex(8)


def content(value):
    if isinstance(value, str):
        return value
    if isinstance(value, list):
        return "\n".join(str(x.get("text", "")) for x in value if isinstance(x, dict))
    return ""


class Provider(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def do_GET(self):
        data = json.dumps(
            {
                "object": "list",
                "data": [
                    {"id": "fixture", "object": "model", "owned_by": "local-test"}
                ],
            }
        ).encode()
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self):
        try:
            assert self.path == "/v1/chat/completions", self.path
            n = int(self.headers.get("content-length", "0"))
            assert 0 < n < 4 * 1024 * 1024
            body = json.loads(self.rfile.read(n))
            assert body.get("model") == "fixture", body.get("model")
            tools = {
                x.get("function", {}).get("name"): x.get("function", {})
                for x in body.get("tools", [])
            }
            messages = body.get("messages", [])
            last = max(
                (i for i, m in enumerate(messages) if m.get("role") == "user"),
                default=-1,
            )
            text = content(messages[last].get("content")) if last >= 0 else ""
            turn = (
                "child"
                if CHILD in text
                else (
                    "hierarchy"
                    if HIERARCHY in text
                    else (
                        "initial"
                        if INITIAL in text
                        else "semantic" if SEMANTIC in text else "auxiliary"
                    )
                )
            )
            after = messages[last + 1 :] if last >= 0 else []
            has_result = any(m.get("role") == "tool" for m in after)
            main = bool(tools) and turn != "auxiliary"
            calls = []
            call_key = hashlib.sha256(text.encode()).hexdigest()[:12]
            if main and not has_result:
                if turn == "hierarchy":
                    assert "task" in tools, "native task tool unavailable"
                    calls.append(
                        {
                            "id": "call_" + call_key + "_task",
                            "type": "function",
                            "function": {
                                "name": "task",
                                "arguments": json.dumps(
                                    {
                                        "description": "Run the isolated native child fixture",
                                        "subagent_type": "general",
                                        "prompt": CHILD + " " + call_key,
                                    }
                                ),
                            },
                        }
                    )
                else:
                    assert "bash" in tools, "native bash tool unavailable"
                    calls.append(
                        {
                            "id": "call_" + call_key + "_bash",
                            "type": "function",
                            "function": {
                                "name": "bash",
                                "arguments": json.dumps(
                                    {
                                        "command": "printf 'SWARM_LOOPBACK_TOOL_OK\\n'",
                                        "description": "Print the fixed local integration marker",
                                    }
                                ),
                            },
                        }
                    )
                    if "question" in tools:
                        calls.append(
                            {
                                "id": "call_" + call_key + "_question",
                                "type": "function",
                                "function": {
                                    "name": "question",
                                    "arguments": json.dumps(
                                        {
                                            "questions": [
                                                {
                                                    "question": "Choose the fixture answer",
                                                    "header": "Fixture",
                                                    "options": [
                                                        {
                                                            "label": "A",
                                                            "description": "Accept the deterministic fixture",
                                                        },
                                                        {
                                                            "label": "B",
                                                            "description": "Unused fixture choice",
                                                        },
                                                    ],
                                                    "multiple": False,
                                                }
                                            ]
                                        }
                                    ),
                                },
                            }
                        )

            answer = (
                ("SWARM_LOOPBACK_" + turn.upper() + "_DONE")
                if main
                else "Local fixture qualification"
            )
            with request_lock:
                assert (
                    len(requests) < args.max_iterations * 8 + 32
                ), "fixture request safety cap exceeded"
                index = len(requests) + 1
                requests.append(
                    {
                        "index": index,
                        "path": self.path,
                        "model": body.get("model"),
                        "stream": body.get("stream"),
                        "turn": turn,
                        "main": main,
                        "after_tool_results": has_result,
                        "offered_tools": sorted(k for k in tools if k),
                        "scripted_tools": [x["function"]["name"] for x in calls],
                        "scripted_finish": args.tool_finish if calls else "stop",
                        "message_count": len(messages),
                        "request_bytes": n,
                        "last_user_hash": hashlib.sha256(text.encode()).hexdigest(),
                    }
                )
            ident = "chatcmpl-local-" + str(index)
            created = int(time.time())
            if body.get("stream"):
                self.send_response(200)
                self.send_header("content-type", "text/event-stream")
                self.send_header("cache-control", "no-cache")
                self.send_header("connection", "close")
                self.end_headers()

                def chunk(delta, finish=None):
                    item = {
                        "id": ident,
                        "object": "chat.completion.chunk",
                        "created": created,
                        "model": "fixture",
                        "choices": [
                            {"index": 0, "delta": delta, "finish_reason": finish}
                        ],
                    }
                    self.wfile.write(("data: " + json.dumps(item) + "\n\n").encode())
                    self.wfile.flush()

                chunk({"role": "assistant", "content": ""})
                time.sleep(0.15 if main else 0.01)
                if calls:
                    for i, call in enumerate(calls):
                        chunk({"tool_calls": [{"index": i, **call}]})
                        time.sleep(0.15)
                    chunk({}, args.tool_finish)
                else:
                    if main and has_result:
                        time.sleep(args.final_delay_seconds)
                    chunk({"content": answer})
                    time.sleep(0.15 if main else 0.01)
                    chunk({}, "stop")
                self.wfile.write(
                    (
                        "data: "
                        + json.dumps(
                            {
                                "id": ident,
                                "object": "chat.completion.chunk",
                                "created": created,
                                "model": "fixture",
                                "choices": [],
                                "usage": {
                                    "prompt_tokens": 100,
                                    "completion_tokens": 20,
                                    "total_tokens": 120,
                                },
                            }
                        )
                        + "\n\ndata: [DONE]\n\n"
                    ).encode()
                )
                self.wfile.flush()
                self.close_connection = True
            else:
                message = {"role": "assistant", "content": None if calls else answer}
                if calls:
                    message["tool_calls"] = calls
                data = json.dumps(
                    {
                        "id": ident,
                        "object": "chat.completion",
                        "created": created,
                        "model": "fixture",
                        "choices": [
                            {
                                "index": 0,
                                "message": message,
                                "finish_reason": args.tool_finish if calls else "stop",
                            }
                        ],
                        "usage": {
                            "prompt_tokens": 100,
                            "completion_tokens": 20,
                            "total_tokens": 120,
                        },
                    }
                ).encode()
                self.send_response(200)
                self.send_header("content-type", "application/json")
                self.send_header("content-length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)
        except (BrokenPipeError, ConnectionResetError):
            pass
        except Exception as exc:
            errors.append({"provider_error": str(exc)})
            self.send_error(500, "fixture assertion failed")


provider = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Provider)
provider.daemon_threads = True
threading.Thread(target=provider.serve_forever, daemon=True).start()
port = provider.server_address[1]
config = PROFILE / "config/opencode/opencode.json"
config_value = {
    "$schema": "https://opencode.ai/config.json",
    "autoupdate": False,
    "enabled_providers": ["fixture"],
    "model": "fixture/fixture",
    "small_model": "fixture/fixture",
    "default_agent": "build",
    "compaction": {"auto": False},
    "agent": {
        "build": {"model": "fixture/fixture"},
        "general": {"model": "fixture/fixture"},
    },
    "permission": {"*": "deny", "bash": "ask", "question": "allow", "task": "allow"},
    "provider": {
        "fixture": {
            "npm": "@ai-sdk/openai-compatible",
            "name": "Deterministic local fixture",
            "options": {
                "baseURL": f"http://127.0.0.1:{port}/v1",
                "apiKey": "local-fixture-not-a-credential",
            },
            "models": {
                "fixture": {
                    "name": "Local Fixture",
                    "tool_call": True,
                    "limit": {"context": 64000, "output": 8192},
                    "variants": {"max": {"reasoningEffort": "high"}},
                }
            },
        }
    },
}
if args.variant != "default":
    for agent in config_value["agent"].values():
        agent["variant"] = args.variant
config.write_text(json.dumps(config_value) + "\n")
tui = PROFILE / "config/opencode/tui.json"
tui.write_text(json.dumps({"theme": "opencode", "scroll_speed": 3}) + "\n")
(PROFILE / "state/opencode/model.json").write_text(
    json.dumps(
        {
            "recent": [{"providerID": "fixture", "modelID": "fixture"}],
            "favorite": [],
            "variant": {} if args.variant == "default" else {"fixture/fixture": args.variant},
        }
    )
    + "\n"
)
config_hash = {
    str(p): hashlib.sha256(p.read_bytes()).hexdigest() for p in [config, tui]
}
wrapper = BIN / "opencode"
wrapper.write_text(
    '#!/usr/bin/python3\nimport os,sys\nenv=os.environ.copy()\nenv["OPENCODE_TEST_HOME"]='
    + repr(str(RUN / "home"))
    + "\nos.execve("
    + repr(str(NATIVE))
    + ",["
    + repr(str(NATIVE))
    + ",*sys.argv[1:]],env)\n"
)
wrapper.chmod(0o700)
env = {
    k: v
    for k, v in os.environ.items()
    if k in ["HOME", "PATH", "SHELL", "TERM", "LANG", "TMPDIR"] or k.startswith("LC_")
}
env.update(
    {
        f"XDG_{k.upper()}_HOME": str(PROFILE / k)
        for k in ["config", "data", "state", "cache"]
    }
)
env.update(
    PATH=str(BIN) + ":" + env["PATH"],
    OPENCODE_DISABLE_AUTOUPDATE="true",
    OPENCODE_DISABLE_DEFAULT_PLUGINS="true",
    OPENCODE_DISABLE_MODELS_FETCH="true",
    OPENCODE_DISABLE_PROJECT_CONFIG="true",
    SWARM_DAEMON_STATE=str(STATE),
    SWARM_DAEMON_SOCK=str(STATE / "d.sock"),
    SWARM_DAEMON_LOCK=str(STATE / "d.lock"),
    SWARM_DAEMON_LOG=str(STATE / "d.log"),
)
version = subprocess.run(
    [str(wrapper), "--version"],
    env=env,
    cwd=PROJECT,
    capture_output=True,
    text=True,
    timeout=15,
)
if version.returncode != 0 or version.stdout.strip() != args.expected_opencode_version:
    raise SystemExit("isolated native binary version does not match expected contract")
log = (RUN / "daemon-output.txt").open("ab")
daemon = None
sid = None
binding = None
stream_conn = None
endpoint = None
result = {
    "run": str(RUN),
    "source_head": args.source_head,
    "requested_duration_seconds": args.duration_seconds,
    "started_at_unix": started_wall,
    "max_iterations": args.max_iterations,
    "turns": turns,
    "metrics": metrics,
    "event_counts": event_counts,
    "queue_depth": "not exposed; gap events and reconnect outcomes are measured",
    "binary_sha256": hashlib.sha256(SWARM.read_bytes()).hexdigest(),
    "qualification": "actual native runtime, SDK, tool and observer integration with SCRIPTED LOCAL responses; not external model quality/execution qualification",
    "external_model_requests": 0,
    "enabled_provider": "fixture",
    "fixture_profile_variant": args.variant,
    "native_write_via_fake_user_seed": False,
    "request_decision_scope": "public owner native_requests/native_decide with complete current context; authenticated phone transport is not exercised",
    "declared_budgets": budgets,
    "native_version": version.stdout.strip(),
    "native_binary_sha256": hashlib.sha256(NATIVE.read_bytes()).hexdigest(),
    "goroutines": "not exposed by production owner API; not qualified",
    "events": events,
    "status_observations": statuses,
}


def readn(c, n):
    out = b""
    while len(out) < n:
        more = c.recv(n - len(out))
        if not more:
            raise RuntimeError("protocol EOF")
        out += more
    return out


def write_frame(c, value):
    raw = json.dumps(value).encode()
    c.sendall(struct.pack(">I", len(raw) + 1) + b"\x01" + raw)


def read_frame(c):
    n = struct.unpack(">I", readn(c, 4))[0]
    raw = readn(c, n)
    assert raw[0] == 1
    return json.loads(raw[1:])


def rpc(op, timeout=65, **kw):
    global endpoint
    with socket.socket(socket.AF_UNIX) as c:
        c.settimeout(timeout)
        c.connect(str(STATE / "d.sock"))
        write_frame(
            c,
            {
                "op": "hello",
                "protocol_version": 1,
                "capabilities": [
                    "semantic-send",
                    "opencode-observation",
                    "native-requests-v1",
                    "journal",
                ],
            },
        )
        hello = read_frame(c)
        endpoint = hello["endpoint_id"]
        write_frame(c, {"op": op, "endpoint_id": endpoint, **kw})
        began = time.monotonic()
        reply = read_frame(c)
        elapsed = time.monotonic() - began
        if op in ["semantic_send", "native_decide"]:
            result["dispatch_count"] = result.get("dispatch_count", 0) + 1
            result["max_observed_dispatch_seconds"] = max(
                result.get("max_observed_dispatch_seconds", 0), elapsed
            )
            assert elapsed <= args.max_dispatch_seconds, (
                "dispatch latency budget",
                op,
                elapsed,
                args.max_dispatch_seconds,
            )
        return reply


class UDS(http.client.HTTPConnection):
    def __init__(self, b):
        super().__init__("private", timeout=15)
        self.b = b

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(15)
        self.sock.connect(self.b["socketPath"])


def bridge(path, body=None):
    c = UDS(binding)
    raw = json.dumps(body) if body is not None else None
    c.request(
        "POST" if raw else "GET",
        path,
        body=raw,
        headers={
            "Authorization": "Bearer " + binding["token"],
            "Content-Type": "application/json",
        },
    )
    response = c.getresponse()
    data = json.loads(response.read())
    code = response.status
    c.close()
    assert code == 200, (code, data)
    return data


def event_reader():
    global stream_conn
    try:
        stream_conn = UDS(binding)
        stream_conn.request(
            "GET", "/events", headers={"Authorization": "Bearer " + binding["token"]}
        )
        response = stream_conn.getresponse()
        assert response.status == 200
        if stream_conn.sock:
            stream_conn.sock.settimeout(None)
        while True:
            line = response.readline(1048577)
            if not line:
                return
            if line.startswith(b"data: "):
                event = json.loads(line[6:])
                events.append(event)
                event_counts[event["type"]] = event_counts.get(event["type"], 0) + 1
                if len(events) > 512:
                    del events[:-512]
    except Exception as exc:
        result["event_stream_end"] = type(exc).__name__


def root_user_count():
    database = PROFILE / "data/opencode/opencode.db"
    connection = sqlite3.connect(
        "file:" + str(database) + "?mode=ro", uri=True, timeout=3
    )
    try:
        return connection.execute(
            "select count(*) from message where session_id=? and json_extract(data,'$.role')='user'",
            (binding["rootID"],),
        ).fetchone()[0]
    finally:
        connection.close()


def sample(phase, iteration):
    database = PROFILE / "data/opencode/opencode.db"
    meta = json.loads((directory / "meta.json").read_text())
    pids = {
        "daemon": [daemon.pid] if daemon and daemon.poll() is None else [],
        "shim": [meta["shim_pid"]],
    }
    lookup = subprocess.run(
        [LSOF, "-t", str(database)], capture_output=True, text=True, timeout=3
    )
    pids["native"] = sorted({int(x) for x in lookup.stdout.split() if x.isdigit()})
    rss = {}
    for role, ids in pids.items():
        values = []
        for pid in ids:
            r = subprocess.run(
                ["ps", "-o", "rss=", "-p", str(pid)],
                capture_output=True,
                text=True,
                timeout=3,
            )
            if r.stdout.strip().isdigit():
                values.append({"pid": pid, "rss_kib": int(r.stdout.strip())})
        rss[role] = values
    fds = {}
    for role, ids in pids.items():
        values = []
        for pid in ids:
            measured = subprocess.run(
                [LSOF, "-nP", "-p", str(pid), "-Ff"],
                capture_output=True,
                text=True,
                timeout=5,
            )
            count = sum(
                1
                for line in measured.stdout.splitlines()
                if line.startswith("f") and line[1:].isdigit()
            )
            assert count > 0, ("owned process FD measurement unavailable", role, pid)
            values.append({"pid": pid, "count": count})
        fds[role] = values
    state_bytes = 0
    for file in STATE.rglob("*"):
        try:
            if file.is_file():
                state_bytes += file.stat().st_size
        except FileNotFoundError:
            pass
    daemon_output_bytes = (RUN / "daemon-output.txt").stat().st_size
    swarm_disk_bytes = state_bytes + daemon_output_bytes
    for role in ["daemon", "shim"]:
        assert rss[role], ("owned Swarm RSS measurement unavailable", role)
        assert all(v["rss_kib"] <= args.max_swarm_rss_mib * 1024 for v in rss[role]), (
            "Swarm RSS budget",
            role,
            rss[role],
        )
        assert all(v["count"] <= args.max_swarm_fds for v in fds[role]), (
            "Swarm FD budget",
            role,
            fds[role],
        )
    assert swarm_disk_bytes <= args.max_swarm_disk_mib * 1024 * 1024, (
        "Swarm disk budget",
        swarm_disk_bytes,
    )

    growth = {
        suffix
        or "sqlite": (
            database.with_name(database.name + suffix).stat().st_size
            if database.with_name(database.name + suffix).exists()
            else 0
        )
        for suffix in ["", "-wal", "-shm"]
    }
    point = {
        "elapsed_seconds": round(time.monotonic() - started_mono, 3),
        "phase": phase,
        "iteration": iteration,
        "rss": rss,
        "fds": fds,
        "swarm_state_bytes": state_bytes,
        "daemon_output_bytes": daemon_output_bytes,
        "swarm_disk_bytes": swarm_disk_bytes,
        "native_store_bytes": growth,
        "root_user_count": root_user_count(),
        "event_counts": dict(event_counts),
        "daemon_restarts": result.get("daemon_restarts", 0),
    }
    metrics.append(point)
    with (RUN / "metrics.jsonl").open("a") as stream:
        stream.write(json.dumps(point) + "\n")
    heartbeat = {
        "phase": phase,
        "iteration": iteration,
        "elapsed_seconds": point["elapsed_seconds"],
        "native_root": binding["rootID"],
        "provider_requests": len(requests),
        "event_counts": dict(event_counts),
        "daemon_restarts": result.get("daemon_restarts", 0),
        "latest_metrics": point,
    }
    temporary = RUN / "heartbeat.tmp"
    temporary.write_text(json.dumps(heartbeat, indent=2) + "\n")
    temporary.replace(RUN / "heartbeat.json")
    return point


def start_daemon():
    global daemon
    daemon = subprocess.Popen(
        [str(SWARM), "daemon"],
        env=env,
        cwd=PROJECT,
        stdout=log,
        stderr=log,
        start_new_session=True,
    )
    until = time.monotonic() + 15
    while time.monotonic() < until:
        if daemon.poll() is not None:
            raise RuntimeError("private daemon exited")
        try:
            with socket.socket(socket.AF_UNIX) as check:
                check.settimeout(0.2)
                check.connect(str(STATE / "d.sock"))
            return
        except OSError:
            time.sleep(0.05)
    raise RuntimeError("daemon startup timeout")


def stages():
    for stage in ["initial", "semantic", "hierarchy"]:
        if stop_requested:
            return
        yield stage
    if deadline:
        iteration = 3
        while (
            not stop_requested
            and iteration < args.max_iterations
            and time.monotonic() + 45 < deadline
        ):
            yield "hierarchy"
            iteration += 1


def answer_pending(snapshot):
    receipts = []
    for pending in snapshot["permissions"] + snapshot["questions"]:
        for attempt in range(12):
            until = time.monotonic() + 8
            card = None
            while time.monotonic() < until:
                response = rpc(
                    "native_requests",
                    session_id=sid,
                    native_requests={
                        "session": sid,
                        "request_id": pending["id"],
                        "native_session_id": pending["sessionID"],
                    },
                )
                card = response.get("native_requests_result", {}).get("request")
                if card:
                    break
                time.sleep(0.1)
            assert card and json.loads(card["details_json"]) == pending, (
                "complete native request context unavailable",
                response,
            )
            body = {
                k: card[k]
                for k in [
                    "session",
                    "session_instance",
                    "root_id",
                    "generation",
                    "native_session_id",
                    "request_id",
                    "request_hash",
                    "expires_at",
                ]
            }
            body["kind"] = (
                "permission_reply" if card["kind"] == "permission" else "question_reply"
            )
            if card["kind"] == "permission":
                body["reply"] = "once"
            else:
                body["answers"] = [["A"] for _ in card["questions"]]
            operation = "native:fixture:" + secrets.token_hex(16)
            response = rpc(
                "native_decide",
                session_id=sid,
                operation_id=operation,
                body_version=1,
                native_decision=body,
            )
            receipt = response.get("native_decision_receipt", {})
            assert receipt.get("operation_id") == operation and all(
                receipt.get(k) == body[k]
                for k in [
                    "session",
                    "session_instance",
                    "root_id",
                    "generation",
                    "native_session_id",
                    "request_id",
                    "kind",
                ]
            ), response
            if receipt.get("state") == "admitted":
                receipts.append(receipt)
                break
            # Only a correlated, durably rejected no-effect attempt permits a fresh operation.
            # A timeout, generic error or uncertain receipt stops this workload immediately.
            assert (
                receipt.get("state") == "rejected"
                and receipt.get("native_effect") == "none"
            ), response
            result["definite_decision_rejection_count"] = (
                result.get("definite_decision_rejection_count", 0) + 1
            )
            retained = result.setdefault("definite_decision_rejections", [])
            retained.append(receipt)
            del retained[:-128]
            time.sleep(0.25)
        else:
            raise AssertionError(
                "native request remained unqualified after bounded no-effect refusals"
            )
    return receipts


try:
    start_daemon()
    launch = rpc(
        "launch",
        launch={
            "agent": "opencode",
            "name": "local-native-qualification",
            "cwd": str(PROJECT),
            "options": {"model": "fixture/fixture", "agent": "build"},
            "env": [k + "=" + v for k, v in env.items()],
            "cols": 110,
            "rows": 36,
            "initial_prompt": INITIAL,
        },
    )
    result["launch_reply"] = launch
    assert launch.get("op") != "error", launch
    sid = launch["session"]["id"]
    local = sid.split("/", 1)[-1]
    directory = STATE / local
    binding = json.loads((directory / "opencode-binding.json").read_text())
    meta = json.loads((directory / "meta.json").read_text())
    result["identity"] = {
        k: binding[k] for k in ["workerID", "rootID", "directory", "backendGeneration"]
    }
    result["initial_binding"] = meta["opencode_binding"]
    assert meta["opencode_binding"]["initial"]["phase"] == "admitted", meta[
        "opencode_binding"
    ]
    assert (
        meta["opencode_binding"]["initial"]["model"] == "fixture/fixture"
        and meta["opencode_binding"]["initial"]["agent"] == "build"
        and meta["opencode_binding"]["initial"]["variant"] == args.variant
    )
    result["ready"] = bridge("/ready")
    assert result["ready"]["acknowledged"]
    threading.Thread(target=event_reader, daemon=True).start()
    for stage_index, stage in enumerate(stages()):
        if stage != "initial":
            cap = json.loads((directory / "capabilities.json").read_text())
            operation = (
                "semantic:" + str(int(time.time() * 1000)) + ":" + secrets.token_hex(16)
            )
            body = {
                "session": sid,
                "session_instance": cap["session_instance"],
                "text": (SEMANTIC if stage == "semantic" else HIERARCHY)
                + " iteration "
                + str(stage_index),
            }
            for admission_attempt in range(12):
                operation = (
                    "semantic:"
                    + str(int(time.time() * 1000))
                    + ":"
                    + secrets.token_hex(16)
                )
                reply = rpc(
                    "semantic_send",
                    session_id=sid,
                    operation_id=operation,
                    body_version=1,
                    composer_send=body,
                )
                if reply.get("op") == "ok":
                    break
                assert reply.get("error_code") == "input_busy", reply
                proof = rpc("semantic_status", session_id=sid, operation_id=operation)
                receipt = proof.get("semantic_receipt", {})
                assert (
                    proof.get("op") == "semantic_status"
                    and receipt.get("state") == "rejected"
                    and receipt.get("operation_id") == operation
                    and receipt.get("session") == sid
                    and receipt.get("session_instance") == body["session_instance"]
                ), proof
                result.setdefault("definite_rejections", []).append(
                    {
                        "iteration": stage_index,
                        "attempt": admission_attempt,
                        "reply": reply,
                        "durable_proof": proof,
                    }
                )
                time.sleep(1)
            else:
                raise AssertionError(
                    "native activity remained unqualified after bounded definitive-refusal retries"
                )
            result[stage + "_send"] = reply
            result[stage + "_operation_id"] = operation
            result[stage + "_caller_message_id"] = (
                "msg_"
                + hashlib.sha256(
                    (
                        operation
                        + "\0"
                        + binding["rootID"]
                        + "\0"
                        + binding["backendGeneration"]
                    ).encode()
                )
                .digest()[:16]
                .hex()
            )
        intermediate_receipts = []
        expected_message = (
            meta["opencode_binding"]["initial"]["message_id"]
            if stage == "initial"
            else result[stage + "_caller_message_id"]
        )
        until = time.monotonic() + 30
        pending_since = None
        decisions = []
        observed_busy = False
        concurrent = False
        descendant_observations = []
        busy_sampled = False
        while time.monotonic() < until:
            snapshot = bridge("/snapshot?limit=100")
            meta = json.loads((directory / "meta.json").read_text())
            statuses.append(
                {
                    "stage": stage,
                    "at": time.time(),
                    "native": snapshot["statuses"],
                    "daemon": meta["status"],
                    "permissions": len(snapshot["permissions"]),
                    "questions": len(snapshot["questions"]),
                }
            )
            if len(statuses) > 256:
                del statuses[:-256]
            observed_busy |= any(
                x.get("type") != "idle" for x in snapshot["statuses"].values()
            )
            if observed_busy and not busy_sampled:
                sample("busy", stage_index)
                busy_sampled = True
            if snapshot.get("descendants"):
                descendant_observations.append(
                    {
                        "descendants": snapshot["descendants"],
                        "statuses": snapshot["statuses"],
                        "permissions": snapshot["permissions"],
                        "questions": snapshot["questions"],
                        "hierarchyComplete": snapshot["hierarchyComplete"],
                        "hierarchyScope": snapshot["hierarchyScope"],
                    }
                )
            if snapshot["permissions"] or snapshot["questions"]:
                if pending_since is None:
                    pending_since = time.monotonic()
                concurrent |= bool(snapshot["permissions"] and snapshot["questions"])
                if concurrent or time.monotonic() - pending_since > 2:
                    decisions += answer_pending(snapshot)
            rows = snapshot["messages"]
            marker = "SWARM_LOOPBACK_" + stage.upper() + "_DONE"
            final = next(
                (
                    row
                    for row in reversed(rows)
                    if row["info"]["role"] == "assistant"
                    and row["info"].get("parentID") == expected_message
                    and row["info"].get("finish") == "stop"
                    and any(
                        part.get("type") == "text" and marker in part.get("text", "")
                        for part in row["parts"]
                    )
                ),
                None,
            )
            if (
                args.tool_finish == "stop"
                and stage != "initial"
                and not final
                and any(
                    row["info"].get("parentID") == expected_message
                    and row["info"].get("finish") == "stop"
                    and any(part.get("type") == "tool" for part in row["parts"])
                    for row in rows
                )
            ):
                receipt = rpc("semantic_status", session_id=sid, operation_id=operation)
                intermediate_receipts.append(receipt)
                assert receipt["semantic_receipt"]["state"] == "admitted", receipt
            if (
                final
                and not snapshot["statuses"]
                and not snapshot["permissions"]
                and not snapshot["questions"]
                and meta["status"].get("Turn") == "idle"
            ):
                break
            time.sleep(0.1)
        else:
            raise AssertionError("native stage did not finish: " + stage)
        result[stage] = {
            "observed_native_busy": observed_busy,
            "concurrent_permission_and_question": concurrent,
            "decision_receipts": decisions,
            "final_snapshot": snapshot,
            "daemon_status": meta["status"],
            "descendant_observations": descendant_observations,
        }
        assert observed_busy and (decisions or stage == "hierarchy"), (
            stage,
            observed_busy,
            decisions,
        )
        if stage == "hierarchy":
            assert (
                descendant_observations and not snapshot["descendants"]
            ), "active child scope did not settle"
            child_ids = {
                row["id"]
                for observation in descendant_observations
                for row in observation["descendants"]
            }
            child_info = next(
                (
                    event["properties"]["info"]
                    for event in reversed(events)
                    if event["type"] == "message.updated"
                    and event.get("properties", {}).get("info", {}).get("sessionID")
                    in child_ids
                    and event["properties"]["info"].get("role") == "assistant"
                ),
                None,
            )
            assert child_info, "no native child message identity observed"
            exact_child = bridge(
                "/message?sessionID="
                + child_info["sessionID"]
                + "&messageID="
                + child_info["id"]
            )
            assert (
                exact_child["info"]["id"] == child_info["id"]
                and exact_child["info"]["sessionID"] == child_info["sessionID"]
            )
            result["last_exact_descendant_message"] = exact_child
        users = [row for row in rows if row["info"]["role"] == "user"]
        assert any(row["info"]["id"] == expected_message for row in users)
        assert root_user_count() == stage_index + 1
        if stage != "initial":
            assert users[-1]["info"]["id"] == result[stage + "_caller_message_id"]
            result[stage + "_exact_message"] = bridge(
                "/message?messageID=" + result[stage + "_caller_message_id"]
            )
            repeat = rpc(
                "semantic_send",
                session_id=sid,
                operation_id=operation,
                body_version=1,
                composer_send=body,
            )
            result[stage + "_duplicate"] = repeat
            assert repeat["op"] == "ok", repeat
            assert root_user_count() == stage_index + 1
        if stage != "initial":
            final_receipt = rpc(
                "semantic_status", session_id=sid, operation_id=operation
            )
            result[stage + "_terminal_receipt"] = final_receipt
            assert (
                final_receipt["semantic_receipt"]["state"] == "completed"
            ), final_receipt
            result[stage + "_intermediate_receipts"] = intermediate_receipts
            if args.tool_finish == "stop":
                assert (
                    intermediate_receipts
                ), "no native stop+tool intermediate row was observed"
        if stage_index == 2:
            def native_history(**selectors):
                reply = rpc(
                    "interaction_history",
                    session_id=sid,
                    operation_id="qualification:history:" + secrets.token_hex(8),
                    interaction_history={
                        "session": sid,
                        "native": True,
                        "limit": 1,
                        **selectors,
                    },
                )
                assert reply["op"] == "interaction_history", reply
                page = reply.get("native_history")
                assert page and page["v"] == 2 and page["items"], reply
                assert not reply.get("journal"), reply
                assert len(page["scope"]) == 64, page
                for item in page["items"]:
                    assert item["provenance"] == "imported", item
                    assert item["kind"] in [
                        "message_snapshot", "tool_snapshot", "file_snapshot", "plan_snapshot"
                    ], item
                    assert not item.get("operation_id") and not item.get("input_sha256"), item
                return page

            newest = native_history()
            assert not newest["floor"] and newest.get("next_cursor"), newest
            earlier = native_history(native_cursor=newest["next_cursor"])
            anchored = native_history(before_item=newest["items"][0]["item_id"])
            assert newest["scope"] == earlier["scope"] == anchored["scope"]
            assert earlier == anchored, (earlier, anchored)
            assert not {x["item_id"] for x in newest["items"]} & {
                x["item_id"] for x in earlier["items"]
            }, (newest, earlier)
            result["native_history_pages"] = {
                "newest": newest, "continuation": earlier, "before_item": anchored
            }
        turns.append(
            {
                "iteration": stage_index,
                "kind": stage,
                "native_message_id": expected_message,
                "operation_id": None if stage == "initial" else operation,
                "native_completed_parent_id": final["info"]["parentID"],
                "concurrent_permission_and_question": concurrent,
                "decision_count": len(decisions),
                "active_child_ids": sorted(
                    {
                        row["id"]
                        for observation in descendant_observations
                        for row in observation["descendants"]
                    }
                ),
                "scope_settled": not snapshot["descendants"],
                "daemon_idle": meta["status"].get("Turn") == "idle",
            }
        )
        sample("idle", stage_index)
        if stage_index > 0 and stage_index % 10 == 0:
            old = bridge(
                "/message?messageID="
                + result["initial_binding"]["initial"]["message_id"]
            )
            assert old["info"]["role"] == "user"
        if (
            args.restart_every
            and stage_index > 0
            and stage_index % args.restart_every == 0
        ):
            saved = {
                key: meta[key]
                for key in ["conversation_id", "shim_pid", "shim_start_time"]
            }
            before_count = root_user_count()
            rejoin_started = time.monotonic()
            daemon.kill()
            daemon.wait(5)
            time.sleep(0.2)
            start_daemon()
            until = rejoin_started + args.max_rejoin_seconds
            while time.monotonic() < until:
                meta = json.loads((directory / "meta.json").read_text())
                roster = rpc("list")
                observed = next(
                    (
                        v.get("opencode", {})
                        for v in roster.get("sessions", [])
                        if v["id"] == sid
                    ),
                    {},
                )
                if (
                    observed.get("root_id") == binding["rootID"]
                    and observed.get("generation") == binding["backendGeneration"]
                    and observed.get("current_scope_known")
                    and observed.get("busy_known")
                    and not observed.get("busy")
                    and meta["status"].get("Turn") == "idle"
                    and not meta.get("backend_plan_error")
                ):
                    break
                time.sleep(0.1)
            rejoin_elapsed = time.monotonic() - rejoin_started
            assert (
                rejoin_elapsed <= args.max_rejoin_seconds
                and observed.get("current_scope_known")
                and observed.get("busy_known")
                and not observed.get("busy")
            ), "daemon did not rejoin a live exact native source within budget"
            result["max_observed_rejoin_seconds"] = max(
                result.get("max_observed_rejoin_seconds", 0), rejoin_elapsed
            )
            assert (
                all(meta[key] == value for key, value in saved.items())
                and root_user_count() == before_count
            ), "native identity/input changed on daemon restart"
            result["daemon_restarts"] = result.get("daemon_restarts", 0) + 1
            sample("daemon_rejoined", stage_index)
        if deadline and stage_index >= 2:
            pause_until = min(time.monotonic() + args.interval_seconds, deadline)
            while not stop_requested and time.monotonic() < pause_until:
                time.sleep(max(0, min(0.5, pause_until - time.monotonic())))
    if deadline and len(turns) < args.max_iterations:
        while not stop_requested and time.monotonic() < deadline:
            sample("deadline_idle", len(turns) - 1)
            time.sleep(max(0, min(5, deadline - time.monotonic())))
    result["observed_elapsed_seconds"] = round(time.monotonic() - started_mono, 3)
    result["requested_duration_completed"] = bool(
        deadline and time.monotonic() >= deadline and not stop_requested
    )
    result["stopped_by_request"] = stop_requested
    result["config_unchanged"] = all(
        hashlib.sha256(pathlib.Path(p).read_bytes()).hexdigest() == h
        for p, h in config_hash.items()
    )
    assert result["config_unchanged"]
    assert not errors, errors
    result["pass"] = True
except Exception as exc:
    result["pass"] = False
    result["error"] = str(exc)
    result["traceback"] = traceback.format_exc()
    if daemon and daemon.poll() is None:
        try:
            result["failure_owner_roster"] = rpc("list", timeout=5)
        except Exception as error:
            result["failure_roster_error"] = str(error)
    if binding:
        for name, route in [("ready", "/ready"), ("snapshot", "/snapshot?limit=50")]:
            try:
                result["failure_native_" + name] = bridge(route)
            except Exception as error:
                result["failure_native_" + name + "_error"] = str(error)
finally:
    if daemon and daemon.poll() is None:
        candidates = (
            [sid]
            if sid
            else (
                [
                    endpoint + "/" + p.name
                    for p in STATE.iterdir()
                    if p.is_dir() and (p / "meta.json").exists()
                ]
                if endpoint
                else []
            )
        )
        for candidate in candidates:
            try:
                result.setdefault("cleanup", []).append(
                    rpc("kill", timeout=5, session_id=candidate)
                )
            except Exception as exc:
                result.setdefault("cleanup_errors", []).append(str(exc))
        cleanup_until = time.monotonic() + 5
        while time.monotonic() < cleanup_until:
            if all(
                (p / "exit.json").exists()
                for p in STATE.iterdir()
                if p.is_dir() and (p / "meta.json").exists()
            ):
                break
            time.sleep(0.1)
        result["all_owned_shim_exit_files"] = all(
            (p / "exit.json").exists()
            for p in STATE.iterdir()
            if p.is_dir() and (p / "meta.json").exists()
        )
        daemon.terminate()
        try:
            daemon.wait(15)
        except subprocess.TimeoutExpired:
            daemon.kill()
            daemon.wait(5)
    if stream_conn:
        try:
            stream_conn.close()
        except Exception:
            pass
    provider.shutdown()
    provider.server_close()
    log.close()
    result["provider_requests"] = requests
    result["provider_errors"] = errors
    result["fixture_source_sha256"] = hashlib.sha256(
        pathlib.Path(__file__).read_bytes()
    ).hexdigest()
    result["retained_events_limit"] = 512
    result["retained_statuses_limit"] = 256
    if not result.get("all_owned_shim_exit_files", False):
        result["pass"] = False
        result["cleanup_unqualified"] = True
    (RUN / "result.json").write_text(json.dumps(result, indent=2) + "\n")
    (RUN / "finished.json").write_text(
        json.dumps(
            {
                "pass": result.get("pass", False),
                "elapsed_seconds": round(time.monotonic() - started_mono, 3),
                "iterations": len(turns),
                "requested_duration_completed": result.get(
                    "requested_duration_completed", False
                ),
                "error": result.get("error"),
            }
        )
        + "\n"
    )
    print(
        json.dumps(
            {
                "event": "finished",
                "run": str(RUN),
                "pass": result.get("pass", False),
                "error": result.get("error"),
                "provider_requests": len(requests),
                "provider_errors": errors,
            }
        ),
        flush=True,
    )

raise SystemExit(0 if result.get("pass") else 1)
