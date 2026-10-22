## Verdict

Directionally sound. The shim-owned serve topology, qualification-first discipline, honest gap semantics, ADR033 compliance, and refusal to invent a duplicate messaging log are all correct. The main defect is sequencing: the first deployable slice is gated behind both v2 qualification (phase 1) and provisional topology (phase 2), yet the measured legacy failures — watcher timeouts, invisible permission modals, phantom sends — are observation-layer defects that block pilot usability today and are fixable on the already-proven legacy path. A second defect is that multi-request approval storage lands in phase 5, after cross-CLI supervision (phase 4) which will necessarily produce concurrent permission asks.

## Five highest-impact corrections

1. **Unblock the vertical slice from v2.** Legacy SSE busy/idle/error and permission.asked/replied are already qualified. Ship phase 3 on the legacy subscribe-buffer-snapshot-reconcile path with explicit gap markers, and qualify v2 in parallel. Nothing deployable should wait on schema-only endpoints.
2. **Fix the admission/delivery conflation now.** HTTP 204 = admission only is proven; "send to modal returned 0 but no native prompt appeared" is the same bug class. The phase 4 receipt model must treat every admission success as *uncertain* until correlated native input/reply is observed, and never mark completed on admission alone. This must be an explicit acceptance test, not implicit.
3. **Move multi-request approval storage earlier.** The existing one-slot-per-Swarm-session implementation will silently supersede a second child's pending request. If phase 4 supervision runs two children in parallel, one hangs. Either push the phase-5 multi-ID work into phase 3/4 or restrict the supervision pilot to a single child, stated explicitly.
4. **Harden the permission-override risk.** `tools:{bash:true}` promoting permission to allow is a privilege-escalation regression vector. Beyond "omit such fields," add a guard test asserting Swarm never serializes a tools field on any prompt/permission route, and qualify always/reject/races before any always-scope forwarding is enabled.
5. **Define idle positively, not via downgrade.** Since /session/status omits idle keys, /active is process-owned (false idle for unrelated servers), and unchanged grids refresh lastSignalAt every 500ms, idle must be defined as: SSE idle event for the watched session, correlated to the last admitted input, with a timeout fence — and lastSignalAt must stop refreshing on unchanged snapshots. The 30s stale downgrade is a workaround, not a definition.

## Proven facts vs. assumptions vs. proposals

**Proven by this packet:** legacy HTTP/session/SSE behavior including 204-as-admission, error-path completion after first idle, the end-to-end permission round trip and tools-promotion hazard, the ses_-loss-on-SIGKILL vs. graceful-stop contrast, both shared bugs (watch flags-after-ID, handoff root), the pilot timeout during successful replies, invisible modal, pending notification on dead child, and 500ms grid refresh.

**Assumptions requiring qualification (correctly flagged, must stay gated):** all v2 guarantees — same-session/TUI compatibility, same-ID same/different-body retry idempotency, after-cursor gap semantics on non-contiguous sequences, replay-to-follow atomicity, cancellation fencing, 409 conflict behavior across restarts, and whether an attached TUI shares the serve process's session registry. Also unqualified: always/reject/question scopes, compaction, SSE replay.

**Proposals:** shim-owned serve per worker with loopback-only auth secret in per-session custody; persisted nativeID at create time; explicit fail (never the 20s silent standalone fallback) after a native session is allocated; v2 adopted only for durable queue/idempotency and history paging if it qualifies; disclosure-only interrupt scope otherwise.

## 1. Transport/topology

Shim-owned authenticated OpenCode serve per worker, attached TUI, daemon reconnects without owning server lifetime — this mirrors the proven Codex pattern, keeps the shim as PTY/process-group owner, and avoids any broker. Change conditions: (a) if qualification shows the attached TUI and serve do not share a session registry, topology fails and standalone-with-capture must remain the primary; (b) if per-worker port/auth-secret management degrades at scale, add a daemon-side typed proxy — still not a broker; (c) if serve-based resume loses session continuity that standalone preserve (graceful-stop case is proven), keep standalone as a certified path per version, not just fallback.

## 2. Smallest first deployable vertical slice

One OpenCode worker on pinned 1.19.3 with: shim-owned serve + persisted nativeID at create; legacy SSE-driven running/idle/error/attention aggregation with permission modals always visible (fixes Interaction none); semantic send on existing verbs with admission→uncertain→correlated-completion receipts; the two shared-bug fixes; explicit fail-on-reattach-mismatch. Dependencies: only qualified legacy permission semantics (partially done; races unqualified) and SSE reconnect/backoff. No v2, no phone, no native children.

## 3. Missed failure/interleaving cases

- Two children with simultaneous permission asks under one-slot storage → silent supersede/hang (ordering defect above).
- Error event followed by final assistant completion (proven): turn-fold must not mark error terminal before correlated completion; conversely idle must not precede completion-as-terminal.
- TUI user forks/switches sessions while a steer input is admitted to the old thread → input lands in a hidden conversation; fork/switch detection must precede phase 4 sends, not trail in phase 3 polish.
- Owner TUI typing racing a queued steer input for turn ownership.
- Shim kill (not daemon kill): serve dies inside the shim's process group; native session survives but recovery must reattach to the exact session with no second conversation — packet covers daemon/server/shim in phase 7 but reattach-after-shim-kill belongs in the first slice.
- v2 after-cursor gaps silently skipping events during replay-to-follow.
- 409 after restart misread as failure → Swarm must map it to uncertain/never-replay, requiring a restart-retry test.
- Reconnect replay re-showing a permission already replied → stale-reply fencing test (mentioned, not tested).

## 4. Acceptance tests

**Messaging:** SIGKILL daemon mid two-worker OpenCode conversation; after restart, sends deliver byte-exact, PIDs/start identities preserved, watcher settles within SLA, roster not falsely active. Send-to-modal yields uncertain (never completed) and no auto-replay. Same-ID same-body retry before/after restart → exactly one native effect; different body → 409 surfaced as uncertain.

**Monitoring:** successful reply does not time out the watcher (regression); unchanged grid for 5 min does not refresh lastSignalAt; error-then-completion ends as success; modal visible as attention while siblings work; permission.asked replayed after reconnect shown exactly once.

**Recovery:** SIGKILL native process → nativeID already persisted at create (regression for the 631s empty-ID refusal); graceful stop → resume reuses native history and generates a new answer; shim kill → explicit failure, exact-session reattach, no second conversation; 24h soak with bounded memory/disk including identity-map pruning (current maps grow until session end); handoff TO/FROM OpenCode with correct worktree cwd and preserved ProviderCwd/model/agent (regression).

## 5. Remove/defer

Defer: structured phone composer/control until receipts are trustworthy; native children/subagent links until after the first slice; v2 adoption except for durable queue/idempotency/history paging (and only if qualified); compaction observations until post-soak; "projected paged messages" until history qualification; interrupt fencing beyond disclosed scope. Do not build: any provider framework, duplicate Swarm messaging log, full transcript archive, or universal-parity claims — Codex 0.163.0 outside ContextGuard's characterized range and Claude's missing ContextGuard must remain explicitly version-bounded disclosures.

REVIEW_COMPLETE
