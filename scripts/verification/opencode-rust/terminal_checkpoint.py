"""Assert a durable native receipt after a selectively dropped terminal SSE notification."""

import hashlib
import json
import time


def rehearse(expected, root, session, output, history, wait, release, requests):
    state = root / "state/rust-v1" / session.split("/")[-1]
    operation = f"semantic:{time.time_ns() // 1000000}:ad00112233445566778899aabbccdd11"
    text = "RUST_TERMINAL_GAP. Reply with the private fixture only."
    first = output("message", session, "--operation-id", operation, "--text", text)
    assert first["state"] == "native_accepted", first
    release.set()

    path = state / "opencode-receipts" / (hashlib.sha256(operation.encode()).hexdigest() + ".json")
    dropped = state / "fixture-terminal-events-suppressed.jsonl"

    def proof():
        rows = history()["history"]
        assistant = next((row for row in rows
                          if row["info"].get("parentID") == first["native_message_id"]
                          and row["info"].get("finish") == "stop"
                          and any("Deterministic OpenCode fixture response." == part.get("text")
                                  for part in row["parts"])), None)
        if assistant and dropped.exists() and assistant["info"]["id"] in dropped.read_text().splitlines():
            return assistant["info"]["id"]

    native_turn = wait(proof, 15)

    def persisted():
        return json.loads(path.read_text())["receipt"]

    if expected in ("incomplete", "conflict"):
        time.sleep(5)
        assert persisted()["state"] == "native_accepted", persisted()
        assert output("message", session, "--status", "--operation-id", operation) == first
        assert output("message", session, "--operation-id", operation, "--text", text) == first
        assert sum(row["marker"] == "RUST_TERMINAL_GAP" and row["primary"] for row in requests) == 1
        print(f"{expected} history stayed native_accepted without replay", flush=True)
        return

    if expected == "missing":
        # Before the fix this receipt remains nonterminal even with the native
        # user's message and its confirmed reply both available. Read-only
        # history calls above never ask Swarm to reconcile the operation.
        time.sleep(5)
        assert persisted()["state"] == "native_accepted", persisted()
        observed = output("message", session, "--status", "--operation-id", operation)
        assert observed["state"] == "completed", observed
    else:
        observed = wait(lambda: (receipt if (receipt := persisted())["state"] == "completed" else None), 15)
        assert observed["outcome_source"] == "reconciled_snapshot", observed
        assert output("message", session, "--status", "--operation-id", operation) == observed
    assert observed["native_turn_id"] == native_turn
    assert output("message", session, "--operation-id", operation, "--text", text) == observed
    assert sum(row["info"]["role"] == "user" and any(part.get("text") == text for part in row["parts"])
               for row in history()["history"]) == 1
    print(f"missing terminal event witness {expected}, one matched native turn and no replay", flush=True)
