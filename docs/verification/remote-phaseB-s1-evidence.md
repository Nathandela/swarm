# Phase B slice S1 — dependency-edge surgery (PB-BIND-0)

**Requirement**: PB-BIND-0 — the gomobile-bound package's dependency closure is constrained by
an executable allowlist of exact import paths, and a test fails on any package outside it.
**Acceptance criterion**: "A test computes `go list -deps` and fails on any package outside the
checked-in allowlist file. Phase A suite green through the extraction."

## The problem (verified before the change)

`internal/phonecore` -> `internal/protocol` -> `internal/daemon` dragged the daemon, shim,
engine, VT emulator, transcript, persistence, PTY and the whole charmbracelet terminal stack
into the package destined for an Android AAR. Shipping that to a device an adversary may
physically hold is both attack surface and contrary to ADR-007 Decision 2, which deliberately
keeps the VT emulator and raw PTY bytes off the network-facing edge.

## RED (failing first, GG-5)

```
=== RUN   TestBoundClosureMatchesAllowlist
    deps_allowlist_test.go:45: 34 of 52 non-standard packages in the closure of
    github.com/Nathandela/swarm/internal/phonecore are not in deps_allowlist.txt:
        github.com/Nathandela/swarm/internal/daemon
        github.com/Nathandela/swarm/internal/engine
        github.com/Nathandela/swarm/internal/persist
        github.com/Nathandela/swarm/internal/shim
        github.com/Nathandela/swarm/internal/transcript
        github.com/Nathandela/swarm/internal/vt
        github.com/charmbracelet/x/vt
        github.com/creack/pty
        ... (34 total)
--- FAIL: TestBoundClosureMatchesAllowlist (0.09s)
```

A real assertion failure listing the forbidden closure — not a compile error, not a missing file.

## The change

A new leaf package `internal/protocol/schema` holds the daemon-free wire message types;
`internal/protocol` **aliases** every moved name (`type Control = schema.Control`, ...). A Go
type alias *is* the type, so wire encoding, type identity, method sets and reflection are
untouched: `cmd/swarm`, `internal/tui`, `internal/skeleton`, `internal/remotegw`,
`internal/phonesim` and all protocol test files compile and pass **unmodified**.

Shape 1 (splitting `internal/protocol` in place) was rejected: `client.go`, `fromdaemon.go` and
`server.go` each pull daemon/vt/wire/persist, so the package could not be made daemon-free
without moving Server/Client out of `protocol` and breaking its public API for four consumers.

## GREEN

- Closure: **52 -> 18** non-stdlib packages; **zero** forbidden packages remain.
- `schema`'s own closure is `internal/status` + stdlib.
- Guard covers **host, android/arm64 and ios/arm64** (the host and android closures already
  differ, so a host-only check had a real blind spot).
- Re-adding `internal/protocol` to the allowlist would surface 35 forbidden packages — the
  guard is non-vacuous, and the test retains an empty-closure vacuity check.

## Independent review

An independent reviewer (neither test author nor implementer) verified:
- **The move is byte-for-byte verbatim**: declaration ranges extracted from `git show HEAD:`
  and diffed mechanically — 150 lines (`types.go`) and 92 lines (`remote.go`), both IDENTICAL.
  Every json tag, `omitempty` and field order preserved (field order fixes JSON key emission).
- **Alias semantics**: constant typing preserved in both directions (`Code*` stayed typed
  `ErrorCode`, `Action*` stayed untyped); the single method `ErrorCode.Transient()` travelled
  with its type; embedded field names unchanged so JSON promotion is identical. What does
  differ (`reflect.Type.PkgPath`) was grepped for and is unused by any assertion.
- **Live wire-compatibility evidence**, stronger than the argument: the machine side was NOT
  repointed — `remotegw` still seals `protocol.Control` while `phonecore` now opens
  `schema.Control` — and the skeleton e2e tests cross that seal/open boundary and pass.

Verdict: **approve**, conditional on the fixes below, all applied.

## Review findings applied

| Finding | Action |
|---|---|
| R1 `docs/specifications/protocol.md` named the wrong implementation site for the wire structs | Repointed to `internal/protocol/schema`. The normative spec must not drift, and the GG-7 drift check compares tags only so it structurally could not catch this. |
| R3 nothing asserted the alias was still an alias | Added `var _ Control = schema.Control{}` — compiles only while they are the same type. Negative control confirmed: un-aliasing yields `cannot use schema.Control{} ... as Control value`. |
| R4 verbatim-moved comment referenced `LaunchContentHash`, unresolvable in its new home | Qualified as `protocol.LaunchContentHash`. |
| R5 the guard computed the HOST closure, not the gomobile target | Added an android/ios arm64 sub-test. Real gap: `golang.org/x/sys/cpu` is in the darwin closure and absent from android, so a `//go:build android` import of a forbidden package would have left CI green while the daemon shipped to the handset. |

## Tracked forward (S8 trap, review R2)

`LaunchContentHash` deliberately stayed in `internal/protocol` — it is the only thing binding a
signed launch to its spec, Go has no function aliases, and moving it would mean either a
forwarding wrapper or rewriting five call sites in committee-validated Phase A code. That is
the right call for this slice, but it leaves S8 (PB-BIND-3 lists "launch") with three options,
and **the third is forbidden**: (a) move the function then, as a reviewed edit; (b) expose it
through the façade from `schema`; (c) **reimplement the canonical length-prefixed encoding in
the façade — NOT PERMITTED**, because a one-byte divergence produces silent signature
verification failures with no compile error and no test linking the two implementations.

## Gates

```
go build ./...                     exit 0
go vet ./...                       exit 0 (excluding internal/remote/qrterm, slice S3's deliberate build-RED)
go test -race ./internal/protocol/... ./internal/phonecore/... ./internal/remotegw/... ./internal/skeleton/...
                                   all ok
```

Full-suite failures at the time of this run belong to slice S3's failing-first QR tests
(`qrterm` build-RED, `cmd/swarm` QR x4, `internal/skeleton` PB-PAIR-7 x2), confirmed not
attributable to S1 by stashing S1's files and reproducing the identical failures at HEAD.
`TestRemotePeek_LargeGridClippedUnderMaxFrame` is a known pre-existing load flake.
