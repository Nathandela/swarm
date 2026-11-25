"""Private owner-protocol client shared by the two installed OpenCode probes."""
import hashlib
import json
import os
import socket
import struct
import sys


def owner_request(env, request=None, caps=()):
    expected = "ep-" + hashlib.sha256(os.path.realpath(env["SWARM_DAEMON_STATE"]).encode()).hexdigest()[:8]
    with socket.socket(socket.AF_UNIX) as client:
        client.settimeout(70)
        client.connect(env["SWARM_DAEMON_SOCK"])
        with client.makefile("rwb") as stream:
            def exchange(body):
                frame = b"\x01" + json.dumps(body).encode()
                stream.write(struct.pack(">I", len(frame)) + frame)
                stream.flush()
                size, = struct.unpack(">I", stream.read(4))
                assert 1 < size <= 1 << 20, size
                reply = stream.read(size)
                assert len(reply) == size and reply[0] == 1
                return json.loads(reply[1:])

            hello = exchange(dict(op="hello", protocol_version=1, build_version="dev", capabilities=list(caps)))
            assert hello["op"] == "hello" and hello["endpoint_id"] == expected, hello
            return exchange(dict(endpoint_id=expected, **request)) if request else hello


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
