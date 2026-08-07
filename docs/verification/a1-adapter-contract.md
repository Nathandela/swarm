# A1 — the adapter contract's structured-capture extension (ADR-010)

**Workpackage**: W1 of the interaction program — `internal/adapter` only.
**Governing documents**: [ADR-010](../adr/ADR-010-adapter-structured-capture.md) (Accepted
2026-08-07) is normative for everything below; [ADR-009](../adr/ADR-009-structured-chat-interaction.md)
is the pivot it serves and [interaction-schema.md](../specifications/interaction-schema.md) owns the
item field names (`IS-*` rules cited inline).
**Obligation**: implementation-goals.md GG-5 — the failing-first run is recorded verbatim below,
GREEN appended after.

**Scope delivered**: the additive, OPTIONAL extension — `Interaction` (pure data),
`InteractionSource`, `DecisionAction` (one `Reply` field, no `Keys`), the `capture=raw` descriptor
declaration, `AsInteractionSource` (the generic-fallback absence signal), and ADR-010's five
conformance obligations as tests.
**Explicitly NOT delivered here**: any concrete CLI producer (Claude Code / Codex shaping is a
separate slice), the daemon item struct and its `journal.Record` carriage, size caps and
truncation (§5 — daemon-side per ADR-010 §3), the admission queue (ADR-010 §7), and push-wake
(ADR-010 §4). No file outside `internal/adapter` is touched.

---

## 1. RED — the failing-first run

Run at HEAD `056eb74` with the three new test files in place and **no production code written**:
`internal/adapter/interaction.go` did not exist and `conformance.go` was unmodified.

```
$ go test ./internal/adapter/
# github.com/Nathandela/swarm/internal/adapter [github.com/Nathandela/swarm/internal/adapter.test]
internal/adapter/interaction_stubs_test.go:35:77: undefined: DescriptorCapture
internal/adapter/interaction_stubs_test.go:36:71: undefined: DescriptorCapture
internal/adapter/interaction_stubs_test.go:53:53: undefined: Interaction
internal/adapter/interaction_stubs_test.go:98:54: undefined: DecisionAction
internal/adapter/interaction_stubs_test.go:145:56: undefined: Interaction
internal/adapter/interaction_stubs_test.go:157:68: undefined: Interaction
internal/adapter/interaction_stubs_test.go:165:56: undefined: Interaction
internal/adapter/interaction_stubs_test.go:174:58: undefined: Interaction
internal/adapter/interaction_stubs_test.go:191:62: undefined: Interaction
internal/adapter/interaction_stubs_test.go:207:53: undefined: Interaction
internal/adapter/interaction_stubs_test.go:36:71: too many errors
FAIL	github.com/Nathandela/swarm/internal/adapter [build failed]
FAIL
```

The failure is **undefined symbols only** — no syntax error, no "no non-test Go files", no
pre-existing test broken. That is the shape Epic 9's own RED record pinned for this package
(`stubs_test.go:10-14`): the contract-package tests are self-contained, so the RED run names the
missing contract and nothing else.

Failing test files, all new:

| file | what it pins |
|---|---|
| `internal/adapter/interaction_test.go` | the extension's type shape, the schema's kind/status vocabulary, `DecisionAction`'s single field, absence-detection, `Interaction.Validate`, and the conformance teeth (a conformant capturing adapter passes; ten single-defect violators each fail on their own rule) |
| `internal/adapter/interaction_stubs_test.go` | `captureAdapter` (conformant) plus the ten violators |
| `internal/adapter/interaction_fuzz_test.go` | `FuzzInteractionsTotality` — obligation 1 |

---

## 2. RED — second cycle, the no-decisions rule

A gap found while reviewing the teeth: an adapter could dodge EVERY obligation-5 check by
returning an `approval_request` with an empty `decisions` slice, because the mode/keystroke
pairing is checked per decision. IS-APR-3 says the card labels its buttons from
`decisions[].label`, so a request with none renders an unactionable card. The rule was added
failing-first, in its own cycle:

```
$ go test ./internal/adapter/ -run 'TestInteractionValidate/approval-with-no-decisions' -v
=== RUN   TestInteractionValidate
=== RUN   TestInteractionValidate/approval-with-no-decisions
    interaction_test.go:163: Validate accepted a malformed item (expected a violation about "decisions")
--- FAIL: TestInteractionValidate (0.00s)
    --- FAIL: TestInteractionValidate/approval-with-no-decisions (0.00s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/adapter	0.882s
FAIL
```

The rule's first GREEN run then exposed that two sibling cases in the same table
(`keystrokes-on-a-card`, `prompt-lines-on-a-card`) carried TWO defects rather than one and were
failing on the new rule instead of their own. They were given a decision so each case again
breaks exactly one rule — the single-defect discipline `stubs_test.go:17-19` states. No
assertion was weakened.

---

## 3. Open points resolved, and where each is recorded in code

ADR-010 and interaction-schema.md leave four points open at the adapter boundary. Each is
resolved with the leanest reading consistent with the frozen rulings, and each resolution carries
a code comment citing the clause it answers.

**OP-1 — `Interaction.Ref` is one field doing two jobs.** ADR-010 §4 names "the CLI's own request
id (the `ref` `Decision` is later called with)" for approvals only. But IS-ENV-2 requires every
record of a streamed item to repeat one `item_id`, and IS-DELTA-1/-3 require the producer to merge
`agent_message` increments and collapse a `tool_run` open+close — all of which need a correlation
key, and the adapter is the only party that sees the CLI's own id. Resolution: **one** `Ref` field,
adapter-supplied, meaning "the CLI's own id for this interaction". The daemon maps `Ref` to a
minted `item_id` and the item never carries `Ref` (IS-APR-1 leaves exactly one id on the wire).
Recorded at `internal/adapter/interaction.go` on the `Ref` field.

**OP-2 — `Interaction` carries no `approval_resolved` / `session_status` payload fields.** Both
kinds are daemon-minted: IS-LIFE-2's resolver covers five paths (four of which no adapter
observes), IS-ST-2's sweep fires on instance death, and IS-SS-1's `session_status` is the
`status.*` projection the roster already derives. The kind **constants** exist for all eight
(§3 is the wire vocabulary and the daemon reuses these constants), and `Validate` accepts all
eight, so a later agent-sourced cancel needs no contract change. Recorded as a ponytail ceiling on
the `Interaction` type.

**OP-3 — an unknown `capture` value is a violation, not an ignored key.** ADR-010 §1 defines
exactly one value, `raw`. A typo (`capture=Raw`) would otherwise silently disable capture and the
CLI's bodies would be flattened away with no signal. `checkInteractionSource` rejects any other
value. Recorded on `CaptureRaw`.

**OP-4 — the decision→keystroke map lives on `Interaction.Keystrokes`.** ADR-010 §2/§4 says the
map is "produced at capture", "held MACHINE-SIDE", and is not a field on the item or on
`DecisionAction` — but names no home. `Interaction` is the only adapter→daemon carrier, so the map
rides there, keyed by `decisions[].id`, present iff `Mode == prompt_card`. The daemon must never
copy it onto the item (IS-APR-3). `TestDecisionAction_CarriesOnlyTheReplyBody` pins the negative
half by reflection. Recorded on the `Keystrokes` field.

**OP-5 — obligation 3 is split across two drivers.** "An adapter implementing `InteractionSource`
declares `capture=raw` on every event row it shapes" is not decidable from the interface alone —
it needs the corpus. The pure half (capture rows are well-formed, and declaring capture and
implementing the source imply each other) runs inside `CheckConformance`; the corpus half runs in
`CheckInteractionFixture(a, fx)`, which replays a fixture's payloads and flags any event that
shaped items without a `capture=raw` row. Recorded on `CheckInteractionFixture`.

---

## 4. ADR-010's five conformance obligations — where each is discharged

| # | Obligation | Discharged by |
|---|---|---|
| 1 | `Interactions` pure and total: fuzz nil, empty, truncated, deeply nested, oversized — no panic, deterministic | `interactionProbes` battery inside `CheckConformance` + `FuzzInteractionsTotality`; `TestCheckConformance_InteractionsTotalityIsProbed` asserts the battery actually contains a nil, a garbage and an oversized body |
| 2 | Every adapter package still greps zero-hit against `bannedIOTokens` | the pre-existing `TestContractPackage_NoIOInSource` (`boundary_test.go:67`) and `refadapter`'s copy — the new production file names no banned token (it imports only `encoding/json`, `fmt`, `reflect`, `strings`) |
| 3 | capture=raw declared on every shaped row, and every declared capture key names a real event row | `checkInteractionSource` (pure half) + `CheckInteractionFixture` (corpus half) — see OP-5 |
| 4 | Fixture replay: recorded payloads → `Interactions` → golden items | `CheckInteractionFixture` replays `Fixture.HookPayloads` through the source and validates every shaped item; `TestCheckInteractionFixture_ReplaysTheCorpus`. The per-CLI **golden** comparison lands with each producer slice — there is no recorded corpus for a CLI with no shaper yet |
| 5 | `Decision` ok==false is a supported, exercised path; the carve-out item declares `mode: prompt_card` AND the adapter produced a machine-side map the item does not carry | `checkShapedItems`' prompt-card/card pairing rules, `TestCheckInteractionFixture_ProvesThePromptCardCarveOut`, and `TestDecisionAction_CarriesOnlyTheReplyBody` |

---

## 5. GREEN

Production code landed in `internal/adapter/interaction.go` (new) and `internal/adapter/conformance.go`
(the ADR-010 obligations appended; `CheckConformance` gained one line calling them). Nothing else
in the tree was touched.

```
$ go test -race -count=1 ./internal/adapter/...
ok  	github.com/Nathandela/swarm/internal/adapter	3.627s
ok  	github.com/Nathandela/swarm/internal/adapter/agy	3.785s
ok  	github.com/Nathandela/swarm/internal/adapter/claude	3.804s
ok  	github.com/Nathandela/swarm/internal/adapter/codex	2.320s
ok  	github.com/Nathandela/swarm/internal/adapter/detect	10.513s
ok  	github.com/Nathandela/swarm/internal/adapter/fixtureio	3.084s
ok  	github.com/Nathandela/swarm/internal/adapter/opencode	4.159s
ok  	github.com/Nathandela/swarm/internal/adapter/refadapter	4.079s
ok  	github.com/Nathandela/swarm/internal/adapter/registry	1.830s
```

**The four shipped adapters are the load-bearing line.** `claude`, `codex`, `opencode` and `agy`
each call `adapter.CheckConformance` + `adapter.Conformance` from their own package tests
(`claude_test.go:295-298`, `codex_test.go:233-236`, `opencode_test.go:275-278`,
`agy_test.go:459-462`), so the ADR-010 obligations now run against all four — and all four pass
**with zero source changes**. That is the ADR's "every existing adapter compiles and behaves
unchanged" consequence, demonstrated rather than asserted: none of them declares `capture`, none
implements `InteractionSource`, and `checkInteractionSource` returns nothing for them.

```
$ go build ./...
BUILD_OK
$ go vet ./internal/adapter/...
VET_OK
```

Obligation 1's fuzz target, beyond its seeds:

```
$ go test ./internal/adapter/ -run FuzzInteractionsTotality -fuzz FuzzInteractionsTotality -fuzztime 15s
fuzz: elapsed: 15s, execs: 710085 (49682/sec), new interesting: 22 (total: 245)
PASS
ok  	github.com/Nathandela/swarm/internal/adapter	15.961s
```

710 085 executions, no panic, no nondeterminism, and every shaped item passed `Validate`. No
crasher was written to `testdata/fuzz`.

**E9.2 stays zero-hit.** `TestContractPackage_NoIOInSource` (`boundary_test.go:67`) scans every
production file in the package, `interaction.go` included; the new code imports only
`encoding/json` and `fmt`, and `conformance.go` gained `encoding/json` and `reflect`. None is on
the banned list, and no adapter gained an fd — `Decision` returns a descriptor the CORE executes,
the same shape `Command`/`Resume` already use (ADR-010 §2, ADR-001).

---

## 6. Known-failing fence, NOT fixed here (out of this workpackage's scope)

`internal/verify.TestB94_EveryExportedSymbolIsReachableFromProduction` now fails, naming exactly
three symbols and no others:

```
  internal/adapter -- 3 unreachable exported symbol(s):
      internal/adapter.AsInteractionSource
      internal/adapter.CheckInteractionFixture
      internal/adapter.Interaction.Validate
```

This is the fence working as designed, not a defect in it. All three are unreachable for one
reason: their only current caller is the conformance harness, which is itself allowlisted as "a
test contract by construction" (`phaseb_reachability_test.go:161-162`), and the DAEMON-side
consumer that will call `AsInteractionSource` (the ADR-010 §5 fallback decision) and
`Interaction.Validate` (IS-ENV-3's "emit nothing rather than a partial item") belongs to the
daemon workpackage, not this one.

It is deliberately left failing rather than papered over, because this workpackage may touch only
`internal/adapter` and `phaseb_reachability_test.go` is an existing test in another package.
Two remediations, in preference order:

1. **The daemon slice clears two of the three by using them.** Once the item producer calls
   `AsInteractionSource` and `Interaction.Validate`, only `CheckInteractionFixture` remains.
2. **`CheckInteractionFixture` then needs one `b94Allowed` row**, with the same reason its two
   siblings already carry:
   `"github.com/Nathandela/swarm/internal/adapter.CheckInteractionFixture": "as Conformance."`

If the daemon slice lands later than this fence is next run, rows 1 and 3 take the same shape
(`"the ADR-010 capture extension's consumer is the daemon item producer"`) — but a row added for a
symbol that stays unused is exactly the drift the fence exists to catch, so prefer remediation 1.

`golangci-lint run ./internal/adapter/...` reports two new `QF1011` hints on the compile-anchored
signature pins in `interaction_test.go`. They are the identical, deliberate pattern the frozen
`adapter_test.go:77,81` already uses to pin a function signature at compile time, and the package
is not clean at baseline (4 `errcheck` + 3 pre-existing `staticcheck`), so this is convention, not
a regression.
