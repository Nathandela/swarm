# Final native runtime review — 2026-10-18

Independent connected review found and fixed three defects. The review covered the native observer/reducer/projection, owner/phone/supervisor send custody, durable receipt reconciliation, capability proof, and the actual TUI SDK bridge. Integration through `6b370687` includes these fixes; this record does not certify unfinished maturity or release gates.

| Finding | Correction | Failing test → implementation |
| --- | --- | --- |
| P1: bridge admission concatenated text and accepted ignored-only, attachment, split-text or duplicate-part rows | Both initial and semantic admission require one genuine exact text part; every part retains identity validation. Synthetic/ignored context and unknown metadata remain supported. | `e7de4ea6` → `0d5a3a4d` |
| P1: a hydrated completion could overwrite a retained terminal error | Durable checkpoints use the reducer's outcome rank; higher-ranked errors win, equal rank requires a strictly newer time, and ties preserve the first source. | `917f083f` → `a7b68507` |
| P1/P2: a silent native process retained readiness, while a temporarily unavailable composer could remain disabled | The existing observer tick rechecks the bounded Ready endpoint. Transport/identity failure withdraws current-state proof. Healthy draft/route unavailability withdraws composer only. Recovery performs one snapshot rather than continuous history polling. | `23433286` → `bfa5ea40` |

All 98 bridge Node tests passed. Full bridge Go race tests passed; the final focused `TestOpenCode` race run passed in 45.398 seconds. Additional Ready identity/version/bound-versus-composer fence assertions passed under race. Both source snapshots were tested against a real OpenCode 1.19.3 standalone TUI using the repository's isolated loopback fixture with a private SIGSTOP extension.

The pre-fix worker remained `ready_for_review`, `busy_known=true`, and `current_scope_known=true` through all 15 seconds of native SIGSTOP. After the fix, the first withdrawn sample appeared at 3.022 seconds: turn unknown, busy/scope unknown, and degraded. State recovered 0.108 seconds after SIGCONT. These are measured samples, not a universal timing guarantee.

Both short runs completed three root inputs and a native child, exercised exact admissions, owner request decisions, tool-loop completion and duplicate-operation reconciliation, and cleaned up their owned workers. Both used nine scripted loopback requests and zero external model requests. No daemon-restart claim is made for these particular pause probes. The [sanitized evidence](final-native-review.json) records source, binary and fixture-script hashes, platform, declared budgets, observations and cleanup results; it intentionally omits private binding tokens and full raw runtime records.

The changed positive case is genuine text accompanied by ignored native context: it now admits because ignored text is excluded consistently with Go. Rich genuine parts remain outside this text-only admission proof. Provider execution quality, physical phone transport, full release gates, and elapsed 24h/72h runs remain separate qualification requirements.
