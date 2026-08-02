# ADR-008: Go toolchain floor raised to 1.25

**Status**: Accepted
**Date**: 2026-07-25
**Supersedes**: the floor recorded in ADR-005 (>= 1.24), not ADR-005 itself

## Context

Phase B binds the phone-facing Go core into an Android AAR with `gomobile`. The Phase B
requirements make that dependency mandatory, not incidental — §2 states that `gomobile bind`
requires `golang.org/x/mobile` **in the module dependency graph** (`go get -tool
golang.org/x/mobile/cmd/gobind`), as a tool directive rather than something linked into the
daemon binaries.

Landing that tool directive raised the module's `go` directive from `1.24.2` to `1.25.0`. A
reviewer challenged the bump as not load-bearing, having reverted it and observed that
`go build ./...`, `go vet ./mobile/` and the PB-BIND-0/PB-BIND-2 guards all still passed. That
experiment was sound for the *guards* — they shell out to a prebuilt `gobind` resolved from
`$GOPATH/bin` and never compile `x/mobile` — but not for the module graph.

The implementer refused the revert and demonstrated why. Independently verified here:

```
$ go list -m -f '{{.Path}} {{.GoVersion}}' all | awk '$2 >= "1.25"'
github.com/Nathandela/swarm 1.25.0
golang.org/x/mobile         1.25.0
golang.org/x/mod            1.25.0
golang.org/x/sync           1.25.0
golang.org/x/sys            1.25.0
golang.org/x/tools          1.25.0
```

A hand-reverted `1.24.2` is **not a fixpoint**: `go build ./...` fails with `go: updates to
go.mod needed`, and `go mod tidy` / `go mod download` / `go get` each rewrite the directive
straight back to `1.25.0`. Removing the tool directive *and* the `x/mobile` require does not
help either, because `x/sys` and `x/sync` — raised by `x/mobile` — declare `1.25.0` themselves.
This is not a `go.sum` problem; a hand-populated `go.sum` for the 1.24 graph persisted and the
failure remained.

So the floor is 1.25 as a consequence of a requirement the committee already accepted.

## Decision

**Raise the repository's Go toolchain floor to 1.25.** Record it truthfully everywhere the old
floor was stated, rather than leaving a pin that no longer describes what builds.

The alternative considered and rejected: pin an older `x/mobile` revision whose `go` directive is
<= 1.24. Rejected because `gomobile` is the foundation of the entire Android deliverable — every
later Phase B slice consumes the AAR it produces — and pinning a stale revision changes what bind
support ships inside that artifact (PB-TOOL-2's concern) to preserve a version number. Choosing a
worse foundation to avoid editing a constant is the wrong trade.

## Consequences

Required, and owned by **S13** (which owns PB-TOOL-1 "toolchain pinned in-repo", PB-TOOL-5 "no Go
regression", and PB-TOOL-7 "CI covers the new artifacts"):

- `.github/workflows/ci.yml` pins `go-version: '1.24'` in **seven** jobs and `'1.24.2'` in the
  release job. With `go.mod` at 1.25.0 and the default `GOTOOLCHAIN=auto`, those jobs **silently
  download 1.25 anyway** — the pin does not fail, it just stops being true. That is worse than a
  wrong pin, because it reads as verified when it is not. Raise them.
- `CLAUDE.md` and `AGENTS.md` both state ">= 1.24 (raised by the VT emulator dependency,
  ADR-005)". Update to >= 1.25 and cite this ADR alongside ADR-005 — the VT emulator reason
  remains true and is simply no longer the binding constraint.

Anyone on 1.24 can no longer build the repository. That is a real cost and the honest one: the
code genuinely requires 1.25, and a floor that lies about it converts a clear build error into a
silent toolchain substitution.

## Notes

The floor moved as a *side effect* of a tool directive rather than as a deliberate choice, which
is exactly how toolchain floors usually drift. It is recorded here so the next person finds a
decision rather than an accident, and so the CI pins are corrected in the same breath instead of
being discovered later as a lie.
