"""Host-side, offline fixture for the Responses API. No network or real credentials."""

import http.server
import json


class Provider(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        if self.path != "/v1/responses":
            self.send_error(404)
            return
        size = int(self.headers["Content-Length"])
        if not (0 < size < 2 * 1024 * 1024):
            self.send_error(413)
            return
        body = json.loads(self.rfile.read(size))
        self.server.calls += 1
        if self.server.calls > 4:
            self.send_error(400)
            return
        names = [t.get("name") for t in body.get("tools", [])]
        if self.server.calls == 1:
            if "exec_command" in names:
                name, args = (
                    "exec_command",
                    {
                        "cmd": getattr(
                            self.server, "command", "/usr/bin/python3 /probe.py"
                        ),
                        "login": False,
                    },
                )
            elif "shell_command" in names:
                name, args = (
                    "shell_command",
                    {
                        "command": getattr(
                            self.server, "command", "/usr/bin/python3 /probe.py"
                        )
                    },
                )
            else:
                raise ValueError("Unexpected tool names: " + str(names))
            item = {
                "type": "function_call",
                "name": name,
                "id": "fc_1",
                "call_id": "call_probe",
                "arguments": json.dumps(args),
                "status": "completed",
            }
        else:
            item = {
                "type": "message",
                "id": "msg_1",
                "role": "assistant",
                "content": [
                    {"type": "output_text", "text": "Synthetic probe completed."}
                ],
                "status": "completed",
            }
        resp = {
            "id": "resp_" + str(self.server.calls),
            "object": "response",
            "status": "completed",
            "output": [item],
            "usage": {"input_tokens": 20, "output_tokens": 12, "total_tokens": 32},
        }
        events = [
            (
                "response.created",
                {"response": dict(resp, status="in_progress", output=[])},
            ),
            ("response.output_item.added", {"output_index": 0, "item": item}),
            ("response.output_item.done", {"output_index": 0, "item": item}),
            ("response.completed", {"response": resp}),
        ]
        data = "".join(
            "event: "
            + name
            + "\ndata: "
            + json.dumps(dict(type=name, **payload))
            + "\n\n"
            for name, payload in events
        ).encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)
