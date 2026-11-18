These are macOS-only experimental provenance probes, not a production MCP implementation. They use copied native CLIs, private synthetic configuration, fake loopback models, and the existing `native_fixture_sandbox.py` OS guards. Keep those guards enabled. Copy this directory to a private scratch directory before running: the Codex probe writes its copied executable/config/logs beside its script.

From the repository root:

```bash
probe_dir=$(mktemp -d /private/tmp/swarm-provenance.XXXXXX)
cp -R docs/verification/voice-manager/probes/. "$probe_dir/"
export SWARM_PROVENANCE_REPO="$PWD"
export SWARM_CAUSAL_PROBE_ARTIFACT="$probe_dir"
python3 - <<'PY'
import json, os
from pathlib import Path
root = Path(os.environ['SWARM_PROVENANCE_REPO'])
scratch = Path(os.environ['SWARM_CAUSAL_PROBE_ARTIFACT'])
(scratch/'overlay.json').write_text(json.dumps({'Replace': {
    str(root/'internal/claudeinput/causal_probe_review_test.go'):
    str(scratch/'claude_probe_test.go.txt')}}))
PY
env -u SWARM_SESSION_ID -u SWARM_HOOK_TOKEN -u SWARM_DAEMON_SOCK -u SWARM_SHIM_HOOK_SOCK -u SWARM_HOOK_SEQ_FILE -u SWARM_HOOK_CAPTURE -u SWARM_NATIVE_ACCOUNT_FIXTURE SWARM_CAUSAL_QUEUE_PROBE=1 go test -overlay "$probe_dir/overlay.json" ./internal/claudeinput -run '^TestNativeClaudeCausalProbe$' -count=1 -v
```

Omit `SWARM_CAUSAL_QUEUE_PROBE=1` for the idle identical-composer case. The wrapper reuses the production broker and private native-CLI test helper and requires Claude 2.1.319. Generated `result.json` and `partial-*` contain only this scripted synthetic conversation; review them before publication. Native process groups are stopped/waited, and the MCP recorder records/rechecks its kernel PID/start identity.

Use a separate scratch copy for Codex. Pass the actual native executable if the command resolves to a wrapper:

```bash
codex_probe_dir=$(mktemp -d /private/tmp/swarm-codex-provenance.XXXXXX)
cp -R docs/verification/voice-manager/probes/. "$codex_probe_dir/"
PYTHONDONTWRITEBYTECODE=1 python3 "$codex_probe_dir/codex_probe.py" "$(command -v codex)"
```

The Codex fixture creates its own isolated app-server/thread; it never adopts a production session. Inspect `result.json`: MCP `_meta.callId` joins to `mcpToolCall.id`, and native thread, turn, server, tool and arguments must agree. `_meta.itemId` can differ. Keep the recorded native executable hash with the result. Remove only the owned scratch directories after retaining selected sanitized evidence. No real provider key or login is needed.
