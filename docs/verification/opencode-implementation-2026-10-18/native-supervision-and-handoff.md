# Native initial choices, handoff and passive supervision

Measured 2026-10-18 with OpenCode **1.19.3**, native source pin
`e3c4eb242585dcd77be1a867391b7c25fd47ce52`. The native binary SHA256 was
`6d3e6458fce4b649edf974c19959d5dbfe1e7f255a503dcfc01215b24ad3ea5b`.
All profiles, projects, Swarm daemons and provider fixtures were private under
`/private/tmp/swarm-opencode-implementation-20261018` (`ROOT` below).
No external provider calls, owner-profile edits or credential copies occurred.
Provider responses were scripted localhost test data, not model-quality evidence.
Every successful result confirms unchanged native configuration and owned shim
exit files after cleanup; native histories remain available.

| Check | Build | Retained result | Measured behavior |
|---|---|---|---|
| Initial invalid choices | `86ce9507` | `ROOT/i190620/result.json` | Typo model and primary agent each produce durable named `rejected` custody, empty native history/draft, held input and **zero** provider requests. No replay. |
| Handoff matrix | `86ce9507` | `ROOT/h191640/result.json` | Actual OpenCode-to-OpenCode preserves committed Plan/model and actual checkout with a new root. Native-to-Claude-fixture exports the exact root with provenance and preserves explicit target model; Claude-fixture-to-native admits the genuine handoff prompt with the selected tuple. Six localhost requests. The Claude side is a fixture CLI/transcript, not a Claude generation. |
| Passive supervision | `fe7a805f` | `ROOT/s192020/result.json` | Separate Swarm children notify a real native parent. Owner control, actual unsent draft and native busy state each defer delivery. Release/clear/completion admits exactly one native user message per event with build/fixture/fixture/max. Daemon SIGKILL/rejoin preserves native identity and does not repeat delivered completion. Twelve localhost requests. |

Reproduction scripts are retained in `ROOT/attached-tui/`:
`native_initial_choice_probe.py`, `native_handoff_matrix_probe.py` and
`native_supervision_probe.py`. They accept `--swarm`, `--source-head` and a fresh
short `--run-dir`; the script and binary hashes are recorded in each result.
The supervision binary SHA256 was
`5607f9ddf21fe5eb18710aa06aa143647612b616dafd60b49f6df1b4e6dfa701`.
The initial/handoff binary SHA256 was
`a3b58ccc8b3d82dbf87320c3a5abe32416fd612c8d80c5491ea961e8b2888645`.

## Reproduced failures and fixes

- `ROOT/s190930`: valid configured Build was rejected because the public native
  agent catalogue returns `hidden: null`. `ROOT/catalog1900/catalog.json` records
  the actual read-only SDK shape. Bridge `27e50a44` accepts native null visibility
  and retains typed no-effect preflight rejection; host `a1778146` persists it.
- `ROOT/s190620`: daemon restart emitted child completion sequence 2 after sequence
  1 had already been admitted, with no new child input. `fe7a805f` keys native
  attention by the exact genuine input/outcome or canonical current request set,
  persists the delivered key only after admission, and preserves pending identity
  across unknown/reconnect. Imported unseen history does not create fresh notices.
- `ROOT/s191040`: clearing the actual native draft left the generic direct-input
  marker sticky, preventing passive delivery. `fe7a805f` removes that marker only
  from managed-native supervisor readiness; actual native Prompt emptiness, shim
  line, controls, lifecycle, uncertainty and compaction fences remain in force.
- `ROOT/i190015` and `ROOT/h191250` failed harness assertions (null rejection
  receipt before the typed-rejection fix, and empty AgentCwd fallback respectively).
  Their provider histories and cleanup are retained; the successful reruns above
  are the qualification evidence.

The focused OpenCode and generic-supervisor race suites passed. These checks do
not qualify external provider quotas, successful DeepSeek/GLM inference, a new
OpenCode version, cross-provider model quality, or a fresh-handoff variant setter.
