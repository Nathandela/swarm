"""Cross-read the Go and Rust Context Guard settings through real owner sockets."""
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
import json
import sys
import tempfile

from stopped_go_state import daemon, fixture_env, owner

CAPS = ["context-guard-settings"]


def verify(go, rust, root):
    state = root / "state"
    for path in [root / "home", root / "tools", state]:
        path.mkdir(mode=0o700)
    env = fixture_env(root, state, root / "tools")
    path = state / "context-guard-settings.json"

    def get(caps=CAPS):
        return owner(env, {"op": "context_guard_get"}, caps=caps)

    def set_policy(revision, enabled, threshold):
        return owner(env, {"op": "context_guard_set", "context_guard_set": {
            "expected_revision": revision, "auto_compact": {
                "enabled": enabled, "threshold_percent": threshold}}}, caps=CAPS)

    def settings(revision, enabled, threshold):
        return {"schema_version": 1, "revision": revision,
                "auto_compact": {"enabled": enabled, "threshold_percent": threshold}}

    with daemon(go, env, root / "go.log"):
        assert get()["context_guard_settings"] == settings(0, False, 80)
        assert get([])["error_code"] == "capability_refused"
        assert set_policy(0, False, 80)["context_guard_settings"] == settings(0, False, 80)
        assert not path.exists()
        assert set_policy(0, True, 60)["context_guard_settings"] == settings(1, True, 60)

    with daemon(rust, env, root / "rust.log"):
        assert get()["context_guard_settings"] == settings(1, True, 60)
        assert get([])["error_code"] == "capability_refused"
        assert set_policy(0, False, 80)["error_code"] == "stale_revision"
        assert set_policy(1, True, 39)["error_code"] == "invalid_field"
        with ThreadPoolExecutor(max_workers=2) as clients:
            replies = list(clients.map(lambda threshold: set_policy(1, True, threshold), (70, 75)))
        assert sorted(r["op"] for r in replies) == ["context_guard_set", "error"], replies
        assert next(r["error_code"] for r in replies if r["op"] == "error") == "stale_revision"
        committed = get()["context_guard_settings"]
        assert committed["revision"] == 2 and committed["auto_compact"]["threshold_percent"] in (70,75)
        assert committed == json.loads(path.read_bytes()) and path.stat().st_mode & 0o777 == 0o600

    with daemon(go, env, root / "rollback.log"):
        assert get()["context_guard_settings"] == committed
        assert set_policy(2, False, 40)["context_guard_settings"] == settings(3, False, 40)

    raw = path.read_bytes()
    corrupt = raw.replace(b'"enabled":false', b'"enabled":true,"enabled":false')
    assert raw != corrupt
    path.write_bytes(corrupt)
    for binary in (rust, go):
        with daemon(binary, env, root / (binary.name + "-corrupt.log")):
            assert get()["error_code"] == "unavailable"
            assert set_policy(3, True, 95)["error_code"] == "unavailable"
            assert path.read_bytes() == corrupt
    print("Passed Go to Rust to Go settings, zero-write default, concurrent CAS and corrupt-document refusal")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit("usage: context_guard_settings.py PINNED_GO RUST")
    with tempfile.TemporaryDirectory(prefix="cg-", dir=Path("/tmp").resolve()) as tmp:
        verify(*(Path(arg).resolve(strict=True) for arg in sys.argv[1:]), Path(tmp))
