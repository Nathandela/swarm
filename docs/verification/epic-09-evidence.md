# Epic 9 — Evidence

**Epic**: Adapter contract + characterization harness (`agents-tracker-5sx`) — the frozen anti-corruption boundary (T-1/T-5/T-6)
**Commits**: 8bcf65b (initial + vt Close fix), 6d43704 (contract revision), 7afa326 (swarm-char CLI finish).

## TDD evidence (GG-5)

Designer wrote the failing suite first (red log in [epic-09-red/adapter-red.txt](epic-09-red/adapter-red.txt), undefined-only, 17 symbols mapping 1:1 to the API). Every fix round wrote failing tests first (verified red-to-green).

## Criterion walk (E9.1 – E9.6)

| Criterion | Evidence |
|---|---|
| E9.1 Adapter interface + conformance (T-1) | The ONE interface, frozen; CheckConformance rejects 14+ single-defect violators (shell in ANY argv, impure/nondeterministic Command, panicking/non-total extractor on nil AND non-nil grids, bad option/signal schema, resume rules, empty-id) |
| E9.2 statelessness / no I/O | **Contract package internal/adapter is verified I/O-free** (source-grep over an expanded banned-token list, applied to the contract + each adapter). Detect() made PURE — descriptors (Binary/VersionArgs/ParseVersion) + a core `Detect(a, HostProber)`; the exec-based prober lives in internal/adapter/detect, fixture disk I/O in internal/adapter/fixtureio — both outside the pure contract |
| E9.3 swarm-char harness (T-6) | drives a real CLI in a PTY; -scenario/-input (scripted keystrokes)/-hook-sink (unix socket → HookPayloads)/-geometry/-adapter flags; produces a schema-valid Fixture with scenario + hooks + real-grid capability; bounded hook collection (no hang) |
| E9.4 fixture-corpus schema | versioned; LoadFixture rejects future/garbage; Validate complete |
| E9.5 reference adapter proving T-5 | fixture-only refAdapter passes conformance; import-boundary check (`go list -deps` ⊆ {adapter, vt}) has teeth |
| E9.6 capability matrix | CapabilityEntry derived from the selected adapter + the real captured grid at the characterization geometry |

## The contract-design fix (why the epic mattered)

codex caught that `Detect()` was declared on the pure interface but structurally must probe the host — contradicting ADR-001, the exact invariant Epic 9 freezes. Resolution: descriptor-based detection, so every future adapter (Epic 11 Claude/Codex) is genuinely fd/disk/socket-free and core owns all host I/O. Getting this right before the real adapters is the whole point of the epic.

## Real bug found

The ExtractConversationID totality fuzz found a NON-TERMINATION — traced (goroutine dumps) not to the extractor but to two bugs in internal/vt Emulator.Close()'s DSR-provoke drain-unblock. Fixed by closing charm's reply-pipe writer directly (io.EOF), unblocking the drain regardless of parser state, no shared flag to race. Both reviewers confirmed no shim regression. Two crasher corpus files retained as guards.

## Review outcomes (protocol step 5 — cross-model)

- **Opus (independent)**: APPROVE with 1 MEDIUM (nil-grid-only extraction) + LOW/INFO.
- **codex GPT-5.6 sol (cross-model)**: FIX REQUIRED (3 HIGH incl. the Detect contract flaw) → contract-revision fix round → 3/4 OK, swarm-char CLI NOT-RESOLVED → CLI finish → **APPROVE all, no new defects**.
- Committee synthesis: [audit-005-epic-09.md](audit-005-epic-09.md). Divergence adjudicated by reading code — codex substantially right on the contract flaw Opus rated INFO.

## Quality gates (GG-4)

gofmt · build · vet · `go test ./internal/adapter/... ./cmd/swarm-char/ ./internal/vt/ ./internal/shim/ -race -count=3` green; both extractor fuzzers 30s clean.
