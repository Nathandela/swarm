import os, socket, sys
data = sys.stdin.buffer.read().rstrip(b"\n")
raw = os.environ.get("SWARM_R6_RAW_DIR")
if raw:
    with open(os.path.join(raw, "hooks.ndjson"), "ab") as f:
        f.write(data + b"\n\x00\n")
sink = os.environ.get("SWARM_CHAR_HOOK_SINK")
if sink and data:
    # swarm-char's hookSink reads newline-delimited JSON objects, one per line: compact
    # the (possibly pretty-printed) body onto one line WITHOUT re-serializing -- JSON
    # strings escape raw newlines, so every newline in the body is formatting.
    line = data.replace(b"\r", b" ").replace(b"\n", b" ")
    s = socket.socket(socket.AF_UNIX)
    s.connect(sink)
    s.sendall(line + b"\n")
    s.close()
