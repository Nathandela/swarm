# ADR-010: Structured interaction capture is an optional, additive extension of the frozen adapter contract

**Status**: Accepted (owner sign-off 2026-08-07)
**Date**: 2026-08-07
**Extends**: the Epic 9 adapter freeze (`internal/adapter`) — additively and optionally. The `Adapter` interface method set and every existing data type are unchanged; nothing here supersedes ADR-001 or the E9.2 no-I/O rule.
**Companions**: [ADR-009](ADR-009-structured-chat-interaction.md) (the structured-chat pivot this serves), [ADR-011](ADR-011-multi-device-epochs.md) (multi-device epochs), [docs/specifications/interaction-schema.md](../specifications/interaction-schema.md) (the normative item schema — field names and their meanings live there, not here).

## Context

### What is frozen, and why

`internal/adapter/adapter.go:1-19` declares the package the FROZEN anti-corruption boundary (Epic 9, T-1/T-5/T-6): an `Adapter` is a stateless, goroutine-safe strategy object that answers questions about a CLI and owns **no** lifecycle — no process, no fd, no socket, no disk. Even detection is descriptor-based: the adapter supplies pure `Binary`/`VersionArgs`/`ParseVersion` and the core-owned `Detect(a, HostProber)` (`adapter.go:126-147`) performs every `LookPath`/exec.

The freeze buys three things worth keeping. Adding a CLI touches one package. Core keeps all lifecycle, so there is exactly one place where an agent CLI's bytes can escape. And the contract is small enough to pin mechanically: `TestFrozenTypeShape` (`adapter_test.go:66`) builds every type with named fields, and `TestContractPackage_NoIOInSource` (`boundary_test.go:23-30,67-69`) greps production source for a banned fd/disk/socket/exec token list kept in lockstep with implementation-goals.md E9.2.

The freeze is also a **process** commitment, not only a compile-time one. `docs/verification/audit-002-remote-control-design.md:31` and `docs/research/remote-control-design.md:52-55` (G-6) both pre-committed, in writing, that extending this boundary is ADR-level work *regardless of whether the change is additive*. Spike S-B reached PARTIAL rather than PASS for exactly that reason (`docs/verification/spike-SB.md:7-9`): the capture is proven against real CLIs, the blocker is process. This ADR is the process.

### Why it must be extended, narrowly

Structured interaction content does not exist at ingest today. `cmd/swarm/main.go:666-696` (`parseHookStdin`) keeps only top-level **string** fields, dropping any nested object — `tool_input`, the diff, `permission_suggestions` — because `engine.Callback.Payload` (`engine.go:75-81`) and `SignalSource.Descriptor` (`adapter.go:238-241`) are both `map[string]string`. That is not a bug; it is the E9.1 contract as designed, and it is sufficient for the 4-value `status.Interaction` enum it was built for.

It is not sufficient for ADR-009. A chat transcript needs the tool command and the file diff; an approval card needs the request content, both to render and to hash into ADR-007 D7's binding tuple. Deriving that from the sanitized grid is not an option: spike S-A is PARTIAL — FAIL on overlay transitions, DEGRADED on truncated tool output, and it never recovers `tool_input` at all (`spike-SA.md:33-38,96-99`). Meanwhile `adapter.HookPayload` (`fixture.go:36-40`) already preserves raw bodies losslessly in the characterization corpus, and spike S-B proved live `claude`/`codex` payloads survive that path intact. The data exists at the boundary and is thrown away one layer later.

## Decision

**Structured capture is an opt-in declaration plus one optional Go extension interface. No existing adapter changes; no adapter gains an fd.**

**1. Capture is declared in the existing descriptor, not a new field.** An event row's `SignalSource.Descriptor` may carry `"capture": "raw"`, meaning: preserve this event's body instead of flattening it. `Descriptor` is already `map[string]string` and the engine already looks descriptors up by event (`descriptorForEvent`, `engine.go:577-587`), so `SignalSource` itself is untouched and every adapter that declares nothing behaves exactly as today.

**2. Shaping is an OPTIONAL extension interface.** Adapters that do not implement it remain complete and fully supported.

```go
// InteractionSource is the OPTIONAL extension a CLI-native adapter implements.
type InteractionSource interface {
    // Interactions maps ONE captured event body to zero or more items. PURE and
    // TOTAL, on the same terms as ExtractConversationID: never panics on a nil,
    // truncated, garbage, or unbounded body; deterministic; and it never returns
    // content it did not observe in p.Raw.
    Interactions(p HookPayload) []Interaction

    // Decision describes HOW to apply a verdict to the pending approval named by
    // ref, as a descriptor the CORE executes — the adapter performs no I/O (E9.2),
    // exactly as Command/Resume return an argv core runs. ok == false means this
    // CLI has no native mechanism here and the daemon must use the prompt card.
    Decision(ref, verdict string) (DecisionAction, bool)
}

// DecisionAction is the core-executed effect: the body core writes back on the
// pending hook or JSON-RPC channel. The prompt-card path carries NO DecisionAction
// — spike S-C's carve-out is exactly the path on which Decision is never called, so
// its decision-to-keystroke map is produced at capture and held MACHINE-SIDE. It is
// never a field on the item and never reaches the phone (interaction-schema.md
// IS-APR-3 and IS-LIFE-6; ADR-009 (4)).
type DecisionAction struct {
    Reply json.RawMessage
}
```

`Interactions` takes the **existing** `HookPayload` (`fixture.go:36-40`) rather than a new carrier type. Its name is historical — it carries any captured event body, hook or JSON-RPC — and reusing it means the E9.4 fixture corpus is literally the golden vector set for shaping: replay a recorded fixture's payloads, golden-compare the items.

**3. `Interaction` is pure data, and the daemon is the sole producer of what goes on the wire.** The adapter returns normalized fields and nothing else. Item ids, ordering and journal cursor, size caps and excerpting, redaction, the byte-exact content canonicalization and SHA-256 hash, `expires_at`, the D7 binding tuple, the AEAD-covered kind discriminator, transport — all daemon-side. The adapter never sees a session id, a journal, a mailbox, or a key. The `Interaction` type lives in `internal/adapter` (T-5 forbids an adapter importing anything but the contract and `internal/vt`) and is additive-only, on the same terms as `Detection`'s `ConfiguredModel`/`Models` fields (`adapter.go:89-96`).

**4. Approvals, both directions.** On ingest, a `capture=raw` permission event shapes into an `approval_request` item carrying the adapter's normalized request content and the CLI's own request id (the `ref` `Decision` is later called with). The daemon mints `interaction_id`, hashes the content, binds the tuple, and sets the authoritative expiry. The item also declares its apply mechanism at capture time — the adapter can see the tool and its input then, which is where the S-C carve-out is decidable — so the daemon knows before the phone ever renders whether this request resolves natively or must degrade to a prompt card. For a prompt-card request the adapter also produces its decision→keystroke map at capture, because `Decision` is called only on the native path; that map stays machine-side and is never carried on the item (IS-APR-3).

The push wake IS part of this path, and it does not work by default. `maybeWake` ignores any record with no `Group` (`internal/remotegw/push.go:240-242`) and `Group` is set on group transitions only (`internal/journal/journal.go:58`), so an `approval_request` append cannot wake the phone on its own; today's wake is a side effect of the flat status descriptor (`claude.go:62`, `codex.go:42` → idle/permission), which does not fire when the session is already in that group — a second approval inside one turn wakes nothing. And a wake suppressed by §6.0's 30 s per-session window (PB-PUSH-0, `push.go:14-18`) is **dropped**, not deferred: `claimWindow` returns false and `maybeWake` returns with no retry timer (`push.go:258-260,288-297`). IS-LIFE-1 requires the wake unconditionally, so two rules are decided here. (a) An `approval_request` append is wake-eligible on its own, independent of `Group` and of `isTransition`. (b) A wake the window suppresses is **deferred to the end of that window, never dropped**, and one deferred wake serves every request pending at that moment — the envelope is a constant-size empty plaintext (`PushWakeEnvelopeSize`), so coalescing wakes discloses nothing and loses nothing. The arithmetic: a ≤30 s deferral against Codex's 120 s measured expiry leaves ≥90 s, still above S-C's 60 s one-tap floor, and is not close against Claude Code's ≥300 s. Fence: `internal/remotegw/push_trigger_test.go`.

**5. Per-CLI grounding.**

- **Claude Code** (`internal/adapter/claude/claude.go`): hooks are already injected via inline `--settings` (`claude.go:95-106,167-186`), and S-B confirmed the live sequence `UserPromptSubmit → PreToolUse → PermissionRequest → Notification → PostToolUse → Stop` matches the `hookEvents` table (`claude.go:52-63`). Rows that gain `capture=raw`: `UserPromptSubmit` (user message), `PreToolUse` (tool run opened, `tool_input`), `PostToolUse` (tool run closed, `structuredPatch` → file change), `PermissionRequest` (approval request). Note the shape S-B recorded: an Edit's pre-run diff is `old_string`/`new_string`/`replace_all`, not a unified diff. `Decision` returns the `PermissionRequest` hook's allow/deny reply body — S-C measured that hook holding to within ~15 ms at 5 s, 30 s, 120 s and 300 s with no timeout or auto-deny, which is what makes one-tap the primary path. Bash-with-a-file-path is the measured carve-out: a separate confirmation the hook's `allow` does not resolve, so those requests declare the prompt card at capture and `Decision` returns ok=false. The hook mechanism is settled, not provisional: the native alternative S-C flagged was investigated and closed 2026-08-07 (agents-tracker-n047, recorded in spike-SC.md) — `--sdk-url` is hostname-allowlisted to Anthropic endpoints (loopback rejected, verified live against CLI 2.1.224) and print-mode-only, and Remote Control is gated to api.anthropic.com plus a claude.ai subscription, so no local first-party surface exists. The surviving residual is co-occurrence: a session the user also runs Remote Control on may race this hook's answer, and a supervised spawn must not inherit ambient remote-control environment (tracked separately).
- **Codex** (`internal/adapter/codex/codex.go`): app-server JSON-RPC, near-1:1 with the item model. `item/commandExecution/requestApproval` is already a declared event row (`codex.go:39-45`) and S-B captured its `commandActions`, `proposedExecpolicyAmendment`, and `availableDecisions`; `Decision` returns the JSON-RPC response body, safe to 120 s measured. Per S-C's 60–300 s bracket, Codex ships hybrid: one-tap primary, prompt-card fallback **required**, until a 300 s run says otherwise. Two honesty limits carry forward from S-B: `reason` was null in every observation, and `item/fileChange/requestApproval` was never observed — neither is shaped until a fixture confirms it. Codex's event producer is still deferred (D1, `codex.go` comment); this ADR defines the shape that producer feeds, it does not build it.
- **Generic fallback**: an adapter that implements no `InteractionSource` — `opencode` and `agy` today, and any CLI added without native capture — is served by the daemon deriving items from the sanitized snapshot, under spike S-A's two mandatory production rules (overlay-transition freeze, truncated-output verbatim-with-no-claim). Native capture is an upgrade, never a precondition.

**6. What the E9.1 path carries.** `parseHookStdin` keeps the whole body, under the existing 1 MiB `hookStdinLimit`, only for events whose descriptor declares `capture=raw`; its string-flattening loop and its `turn`/`interaction` injection guard are untouched. `engine.Callback` gains `Raw json.RawMessage` alongside `Payload`. `deriveDims` (`engine.go:540-574`) never reads `Raw` — status is still derived from the flat descriptor and flat payload, and B5's degrade-to-none rule and the 4-value `status.Interaction` enum are unchanged. Raw bodies are untrusted tool output: they never influence status, they are capped at ingest, and they are redacted and excerpted daemon-side before anything is journaled.

**7. The item stream is bounded at the producer, because it cannot be bounded downstream.** The gateway forwards every journal record immediately and never coalesces or drops one (R-GW.5, `internal/remotegw/coalesce.go:39-42,112-120`), and each consumes the shared slot in the ≤8 appends/s machine→phone budget across journal and terminal combined (ADR-007:786-788, PB-GW-7, under the relay's hard `MailboxAppendPerMin: 600`). That budget was set against a journal that records group transitions and nothing else (`internal/daemon/journal.go:107`). Capture makes the stream dense — `PreToolUse` plus `PostToolUse` is two appends per tool call before any `file_change` — and `hookStdinLimit` bounds a body, not a rate. So the rate is bounded in the one place the merge is lossless: the daemon admits at most one item append per `remotegw.DefaultAppendWindow` (125 ms) machine-wide, releasing per-session queues oldest-first — `CoalescingSink.Terminal`'s shape, moved upstream of the sink that may not use it — and merges what it holds: a `tool_run` open and its close inside one window become one record, as do same-window `file_change` records for one run. It is a spacing floor, not a batching delay. `approval_request` is never merged and takes the head of the admission queue, so it waits at most one window and never behind a backlog of prose — as IS-DELTA-3 rules. No kind is exempt from the per-target ceiling itself (IS-DELTA-2a); §4's expiry budget is what makes one window affordable. At the 2–3 items a tool call emits, the floor binds above roughly 3 tool calls/s machine-wide and merges there rather than drops. interaction-schema.md's IS-DELTA-3 carves `tool_run` and `file_change` out of the text-concatenation rule accordingly — record collapse, never merged text, alongside IS-DELTA-2's `agent_message`; its §10 claim that "§6 spends the budget differently; it does not raise it" is true because of this floor, not independently of it. Fence: `internal/remotegw/append_budget_test.go`, a transcript case beside the peek case.

## Non-goals

Deliberately excluded, and a future agent should read an attempt at any of these as drift:

- No new I/O primitive in the contract package or any adapter package. The E9.2 banned-token list is unchanged and must stay zero-hit.
- No change to the `Adapter` interface method set. `TestAdapterInterfaceMethodSet` (`adapter_test.go:32`) and the `var _ Adapter = ...` compile assertions (`adapter_test.go:20-23`) keep pinning it; `InteractionSource` is a separate interface, discovered by type assertion.
- No change to the flattened `map[string]string` status path, `status.Interaction`'s four values, or the grid-heuristic tap (`OnOutput`).
- No adapter ownership of transport, journal, wire format, keys, ids, ordering, append rate, or expiry.
- The normative item field list is interaction-schema.md's; the phone surface is ADR-009's; multi-device crypto is ADR-011's.

## Consequences

### Positive

- Every existing adapter compiles and behaves unchanged; the two heuristic-only adapters need no work ever.
- The adapter stays pure and I/O-free, so the boundary's original guarantee survives intact — the extension adds descriptors and pure functions, which is the same trick `Detect(a, HostProber)` already plays.
- Reusing `HookPayload` makes the fixture corpus the conformance corpus: shaping is testable offline against recorded real-CLI bytes, with no live CLI in CI.
- Approval content becomes hashable, which is what ADR-007 D7's binding tuple has been specified against but could not be fed.

### Negative

- The contract now has two surfaces to keep coherent: the pinned `Adapter` method set and the optional extension. A reader must know to look for both.
- `HookPayload` is now load-bearing for both the fixture schema and a runtime signature, so a `FixtureSchemaVersion` bump touches the shaping path. Accepted deliberately; the alternative was a near-duplicate type.
- CLI drift now breaks the chat view, not just status detection. Fixtures must be re-recorded on drift with hook bodies included — S-B's captures are the starting corpus.
- `Interaction` is a third schema to version alongside the fixture schema and the wire protocol.
- §7's floor costs up to one 125 ms window of transcript latency, and a tool-heavy burst reaches the phone merged rather than blow-by-blow. That is the price of staying inside a budget the gateway is forbidden to enforce on this stream; approvals, the one case where the latency would matter, are never merged and go to the head of the queue, so they pay at most one window.

### Conformance obligations

Additions to the E9.2/E9.4 suites, all mechanical:

1. `Interactions` is pure and total: fuzz nil, empty, truncated, deeply nested, and oversized bodies — no panic, deterministic output.
2. Every adapter package still greps zero-hit against `bannedIOTokens`.
3. An adapter implementing `InteractionSource` declares `capture=raw` on every event row it shapes, and every declared `capture` key names a real event row.
4. Fixture replay: recorded payloads → `Interactions` → golden items, per CLI and per scenario.
5. `Decision` returning ok=false is a supported, exercised path, and the same carve-out fixture asserts the shaped item declares `mode: prompt_card` **and** that the adapter produced a machine-side decision→keystroke map the item does not carry — the fallback is tested where it is produced, not assumed.

## Amendment 2026-08-07 — a decision carries its verdict, and conformance obligation 6

**Status**: Accepted (owner ruling, Nathan, 2026-08-07). **Amends**: decision (4)'s capture-time
declarations and the conformance obligations above, additively. Nothing here changes the `Adapter`
method set, the `InteractionSource` signatures, or any non-goal.

`DecisionChoice` gains a third field, **`Verdict`** — `allow` | `deny` | `other` — which the
adapter sets at capture from its own CLI vocabulary, on exactly the terms `Mode` is already set at
capture: it is decidable there and nowhere else. interaction-schema.md §3.5 keeps the decision
**ids** the CLI's own (Codex: `accept` | `acceptWithExecpolicyAmendment` | `cancel`), and the
daemon needs the grant/refuse polarity to resolve §3.6's `allowed` / `denied` split. A daemon
reading `cancel` as a refusal would be guessing at a vocabulary it does not own — the posture
IS-TOOL-2 forbids for the same reason — so the bit is captured beside the id instead. `other` is
that rule's escape hatch, not a convenience: an unplaceable decision is declared unclassified.

The verdict is **machine-side**, like the prompt-card keystroke map: it is never copied onto the
item and never reaches the phone (interaction-schema.md IS-APR-4).

**Conformance obligation 6.** Every decision an `approval_request` offers carries a verdict, and
it is one of the three. The two halves are enforced in different places on purpose:
`Interaction.Validate` rejects a value outside the vocabulary (a **shape** error — the daemon
switches on it), while `CheckConformance` requires the field to be **present** (a
**completeness** obligation — a verdict-less decision is not malformed, it resolves as "not a
denial", which is the wrong answer given quietly). Violator stub: `decisionWithoutVerdict`.
Evidence: `docs/verification/a1-integration.md`.

## Amendment 2026-08-07 — `Stop` is Claude Code's fifth capture row, so the transcript carries the agent's replies

**Status**: Accepted (owner ruling, Nathan, 2026-08-07). **Amends**: decision (5)'s Claude Code
bullet, additively. Nothing here changes the `Adapter` method set, the `InteractionSource`
signatures, the flattened status path, or any non-goal.

Decision (5) named four `capture=raw` rows for Claude Code, and all four are the human's side of
the conversation or the machinery under it: `UserPromptSubmit` (what the owner typed),
`PreToolUse`/`PostToolUse` (what a tool did), `PermissionRequest` (what needs answering). None
carries what the agent **said**. A phone rendering ADR-009's transcript from those four shows the
owner's messages, the tool cards and the approvals, and not one agent reply — half a conversation,
and the half that is not the point.

**`Stop` gains `capture=raw`, and shapes an `agent_message`** (interaction-schema.md §3.2) out of
its body's `last_assistant_message`: `status: completed`, `text` verbatim from that field, and no
`ref` — one `Stop` is the whole reply, so the item is self-contained and the daemon mints it a
fresh `item_id`; a shared `ref` would fold two consecutive replies under one id and put two
terminal statuses on one item (IS-ST-1). Claude Code's row set is therefore **five**. Nothing else
moves: `Stop` is already in the `hookEvents` table for status (`claude.go`, idle/none) and already
injected by the inline `--settings` value, so the argv, the settings JSON and the ingest path gain
nothing new — the row flips one declaration.

**Why `Stop`, and not per-token streaming.** `Stop` is the one hook the recorded corpus shows
carrying reply prose: every `Stop` body in all three S-B fixtures holds a non-empty
`last_assistant_message` (`internal/adapter/claude/testdata/interaction/`), and no other captured
event carries agent text at all. A hook fires per event, not per token, so this path is by
construction one whole record per turn: the reply reaches the phone complete at the turn's end
rather than as it is written. **Delta streaming stays open**, under IS-DELTA-1/IS-DELTA-2
unchanged, for a future streaming source — a stream-json print mode, an SDK transport, or a later
hook that emits increments. Nothing here forecloses it and no rule is bent to fit: the daemon's
fold-by-`ref` machinery already exists for the day such a source does, and a producer that later
emits increments carries a `ref` and needs no schema change. Shipping the whole-message record now
is the difference between a transcript and a transcript with no answers in it.

**Caps and trust are unchanged.** The adapter returns the field whole and normalizes nothing:
§5's `MaxTextBytes` clip, the redaction and the excerpting stay daemon-side per decision (3), and
`last_assistant_message` is untrusted tool output on exactly the terms decision (6) sets for every
other captured body — it never influences status (the `Stop` row's idle/none mapping is untouched)
and it is capped at ingest.

**Conformance.** No new obligation. Obligation 3 (declare `capture=raw` on every row the producer
shapes, and no others) and obligation 4 (fixture replay → golden items) already govern this row,
and both now count `Stop`. Evidence: `docs/verification/a1b-claude-producer.md` §13.

## Alternatives Considered

- **Widen `SignalSource.Descriptor` to `map[string]any`, or add methods to `Adapter`.** Rejected: forces every adapter and the whole conformance suite to change, for no capability the optional interface does not already give.
- **Change nothing; derive everything from the sanitized grid.** Rejected on evidence, not preference: S-A is PARTIAL, and no amount of grid diffing recovers `tool_input` or a diff, so approvals would have no content worth binding a hash to.
- **Let the adapter write the decision itself.** Rejected: that puts an fd in an adapter package, breaking E9.2 and ADR-001 outright. Returning a descriptor core executes is the established shape.
- **A per-CLI plugin process outside the contract.** Rejected as over-engineering for four in-tree CLIs, and it would move shaping away from the machine-side sanitization choke point (`internal/daemon/terminalrender.go`), which ADR-009 keeps as an invariant.

## Notes

This ADR exists because spike S-B asked for it twice and declined to ship without it — the code change is small enough that an agent could plausibly land it as a refactor, which is exactly the failure the freeze was written to prevent.

On numbering: ADR-009, ADR-010 and ADR-011 were minted together by this program. The README's "next is ADR-009" instruction was correct when written and should read ADR-012 once all three land. This is not the 007/008 collision pattern — these are three distinct sequential numbers, allocated once.
