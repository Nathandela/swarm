"""Private owner-protocol client shared by the two installed OpenCode probes."""
from contextlib import contextmanager
import hashlib
import json
import os
import socket
import struct
import sys


def read_frame(stream):
    size, = struct.unpack(">I", stream.read(4))
    assert 1 < size <= 1 << 20, size
    reply = stream.read(size)
    assert len(reply) == size
    return reply[0], reply[1:]


def write_frame(stream, tag, payload):
    stream.write(struct.pack(">IB", len(payload) + 1, tag) + payload)
    stream.flush()


def send_control(stream, body):
    write_frame(stream, 1, json.dumps(body).encode())


def read_control(stream):
    tag, payload = read_frame(stream)
    assert tag == 1, tag
    return json.loads(payload)


@contextmanager
def owner_stream(env, caps=(), timeout=70):
    expected = "ep-" + hashlib.sha256(os.path.realpath(env["SWARM_DAEMON_STATE"]).encode()).hexdigest()[:8]
    with socket.socket(socket.AF_UNIX) as client:
        client.settimeout(timeout)
        client.connect(env["SWARM_DAEMON_SOCK"])
        with client.makefile("rwb") as stream:
            send_control(stream, dict(op="hello", protocol_version=1, build_version="dev", capabilities=list(caps)))
            hello = read_control(stream)
            assert hello["op"] == "hello" and hello["endpoint_id"] == expected, hello
            yield stream, hello


def owner_request(env, request=None, caps=(), timeout=70):
    with owner_stream(env, caps, timeout=timeout) as (stream, hello):
        if request is None:
            return hello
        send_control(stream, dict(endpoint_id=hello["endpoint_id"], **request))
        return read_control(stream)


def history_page(env, session, limit=1, before=""):
    reply = owner_request(env, dict(op="opencode_history", session_id=session, limit=limit, before=before),
                          ("opencode-history-v1",))
    assert reply["op"] == "opencode_history" and reply["session_id"] == session, reply
    return reply["history_result"]


def launch_request(env, directory, options=None, **fields):
    launch = dict(agent="opencode", cwd=str(directory), env=[f"{k}={v}" for k, v in env.items()],
                  cols=80, rows=24, options=options or {})
    return owner_request(env, dict(op="launch", launch=launch, **fields))


def resume_request(env, session, directory, **fields):
    return launch_request(env, directory, {"resume_from": session}, **fields)


if __name__ == "__main__":
    match sys.argv[1:]:
        case []:
            owner_request(os.environ)
            sys.exit()
        case ["launch", directory]:
            result = launch_request(os.environ, directory)
        case ["resume", session, directory]:
            result = resume_request(os.environ, session, directory)
        case ["history", session]:
            print(json.dumps(history_page(os.environ, session), sort_keys=True))
            sys.exit()
        case _:
            sys.exit("expected launch DIR, resume ID DIR or history ID")
    print(json.dumps(result))
    assert result["op"] == "launch", result
