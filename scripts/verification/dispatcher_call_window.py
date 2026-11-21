#!/usr/bin/env python3
"""Sample a designated connector's local status without opening a call or reading transcripts."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import time


def check_profile(state_dir, expected):
    with (state_dir / "config.json").open("rb") as source:
        data = source.read(1024 * 1024 + 1)
    if len(data) > 1024 * 1024:
        raise ValueError("connector configuration exceeds local inspection limit")
    config = json.loads(data)
    calls = config.get("calls") or {}
    if config.get("application_id") != expected["application_id"] or any(
        calls.get(k) != expected[k] for k in ("guild_id", "room_id", "discussion_id")
    ):
        raise ValueError("local profile does not match designated test surface")


def sample(binary, state_dir, expected):
    check_profile(state_dir, expected)
    cmd = [str(binary), "discord", "status", "--state-dir", str(state_dir)]
    raw = subprocess.run(cmd, check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=10)
    status = json.loads(raw.stdout)
    runtime = status.get("call_runtime") or {}
    if any(runtime.get(k) != v for k, v in expected.items()):
        raise ValueError("connector build or designated test surface does not match")
    if not status.get("connected"):
        raise ValueError("designated connector is disconnected")
    metrics = status.get("call_metrics") or {}
    fields = ("call_id", "ready_ms", "duration_ms", "input_frames", "accepted_turns",
              "verified_publications", "queue_overflows", "written_only_readouts",
              "last_turn_end_to_admission_ms", "last_turn_end_to_result_frame_ms")
    return {"pid": runtime["pid"], "version": runtime["version"], "state": status["calls"],
            "metrics": {k: metrics[k] for k in fields if k in metrics}}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--binary", required=True, type=Path)
    p.add_argument("--state-dir", required=True, type=Path)
    for key in ("application-id", "guild-id", "room-id", "discussion-id", "commit"):
        p.add_argument("--" + key, required=True)
    p.add_argument("--version", required=True, help="expected version reported by the running connector")
    p.add_argument("--pid", required=True, type=int, help="PID recorded when launching the isolated connector")
    p.add_argument("--binary-sha256", required=True, help="digest of the prepared or verified release executable")
    p.add_argument("--stage", choices=("candidate", "published"), required=True)
    p.add_argument("--seconds", type=int, default=0)
    args = p.parse_args()
    if not 0 <= args.seconds <= 1800:
        p.error("sampling duration must be between zero and 1800 seconds")
    binary = args.binary.resolve(strict=True)
    state_dir = args.state_dir.resolve(strict=True)
    expected = {k: getattr(args, k) for k in ("application_id", "guild_id", "room_id", "discussion_id", "commit", "version", "pid")}
    with binary.open("rb") as source:
        checksum = hashlib.sha256()
        for block in iter(lambda: source.read(1024 * 1024), b""):
            checksum.update(block)
        digest = checksum.hexdigest()
    if args.pid <= 0 or digest != args.binary_sha256:
        p.error("PID or status client digest does not match the recorded trial launch")
    started = time.monotonic()
    while True:
        result = sample(binary, state_dir, expected)
        elapsed = time.monotonic() - started
        result.update(stage=args.stage, elapsed_s=round(elapsed, 3), commit=args.commit,
                      status_client_sha256=digest, observed_unix_ms=time.time_ns() // 1_000_000)
        print(json.dumps(result), flush=True)
        if elapsed >= args.seconds:
            return
        time.sleep(min(5, args.seconds - elapsed))


if __name__ == "__main__":
    main()
