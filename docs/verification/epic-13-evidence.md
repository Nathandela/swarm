# Epic 13 — Evidence

**Epic**: Release packaging (`agents-tracker-l56`)
**Status**: implemented + committee-hardened; committed as one set at close.

## What Epic 13 delivers

`swarm` is installable by name and versioned end-to-end. A goreleaser v2 pipeline produces 4 static, dependency-free artifacts + sha256 checksums; the build version is stamped into the binary, reported by `swarm version`, and carried on the real client<->daemon `OpHello` handshake so a client can detect a different-build daemon (D-8). Install is documented for Homebrew (macOS cask tap), `go install`, and static download.

## TDD evidence (GG-5)

Failing-first red run captured in [epic-13-red/version-red.txt](epic-13-red/version-red.txt): with only the implementation symbols reverted (internal/version vars, the cmd/swarm `version` subcommand, `Control.BuildVersion` + its client/server wiring) and the test files kept, `internal/version` and `internal/protocol` fail to COMPILE (`undefined: Version`, `reply.BuildVersion undefined`) and `cmd/swarm`'s version tests fail behaviorally (dispatch falls through to usage, exit 2). Implementation restored to green (files diffed identical after restore). The three cross-version compat tests (E13.2 hardening) are honestly noted as not-red-first: Go's `encoding/json` is unknown-field-tolerant by construction, so they lock an inherent property rather than drive new code.

## Criterion walk (E13.1 – E13.3)

| Criterion | Evidence |
|---|---|
| E13.1 4 static artifacts + checksums; N-4 zero deps | `.goreleaser.yaml`: one build `./cmd/swarm` (dev tools excluded), CGO_ENABLED=0, darwin+linux × amd64+arm64 = 4 targets, tar.gz + sha256 `checksums.txt`. CI `release-dryrun` job runs `goreleaser check` + `release --snapshot --clean --skip=publish`, asserts exactly 4 archives, `sha256sum -c checksums.txt`, static-linked on BOTH linux arches, and `ldd` "not a dynamic executable" on linux/amd64 (fails closed). N-4 proven locally by three reviewers: `otool -L` on the darwin binary shows only /usr/lib system libs; `file` on the goreleaser linux artifacts reports "statically linked". |
| E13.2 version stamped, feeds D-8 handshake; binary reports it | `internal/version.Version` (default "dev"), stamped via `-ldflags -X github.com/Nathandela/swarm/internal/version.Version={{.Version}}`. `swarm version`/`--version` prints it. `Control.BuildVersion` (additive omitempty) carries it on `OpHello`; server replies its own, `Client.BuildVersion()` exposes the daemon's. `ProtocolVersion` stays the sole wire skew gate. Tests: exec-level ldflags-stamp round-trip (runs in CI), server-hello-replies-version, client-exposes-daemon-version, + 3 cross-version compat tests. |
| E13.3 Homebrew tap + go install verified; docs | `homebrew_casks` (the non-deprecated goreleaser key) targeting `Nathandela/homebrew-swarm`, with a macOS post-install `xattr -dr com.apple.quarantine` hook so the unsigned binary runs past Gatekeeper. CI verifies `go install ./cmd/swarm` builds + runs `swarm version`. `docs/install.md` covers Homebrew (macOS only — casks error on Linux), `go install`, static download, and checksum verification; linked from README + docs/INDEX. `packaging/homebrew/swarm.rb` is a committed goreleaser-generated example. |

## Committee (audit-012) — three-model, Fable added

codex + Opus (both FIX REQUIRED) + Fable (CONFIRM, ran goreleaser v2.17 end-to-end). All three confirmed the engineering core sound (4 targets, N-4 gate real, wire-compat clean via omitempty + `json` unknown-field tolerance, no frozen test weakened, no secret mishandling). Fable's live goreleaser run resolved a codex↔Opus divergence: the committed cask is genuine generator output, byte-identical to `dist/homebrew/Casks/swarm.rb`. Fixes landed (F1 quarantine hook, F2 install.md macOS-only, F3 provenance comment, F4 CI go-install gate, F5 go 1.24.2 + goreleaser 2.17.0 pins, F6 checksum + arm64 CI asserts, F7 compat tests) and re-verified (`goreleaser check` green with the hook; whole module green under -race).

## Notes / follow-ups (recorded)

- **Signing/notarization**: the quarantine-strip hook makes the unsigned cask binary runnable, which satisfies E13.3's install path. Proper Developer-ID signing + notarization is a pre-public-release follow-up (needs a real Apple developer identity + a tagged release) — recorded for release time, not an Epic 13 blocker.
- **Snapshot non-determinism**: goreleaser stamps `{{.Date}}`, so snapshot artifact checksums differ per rebuild; the committed example `swarm.rb`'s digests are a point-in-time reference and will not match a future local rebuild (documented in its header). This is expected, not drift.
- **`version.Commit`/`Date` + `Client.BuildVersion()`** are stamped/surfaced but not yet consumed (the D-8 restart nudge is a capability, honestly documented as "detectable"; no criterion required TUI wiring).
- **Pre-existing flake** (not this epic): `cmd/swarm-char` `TestDeriveCapability_FromActualAdapterAndRealGrid` flaked once ("fixture pty_capture is empty"), passed on re-run — recorded for Epic 14 test-stability.

## Quality gates (GG-4)

gofmt · `go build ./...` · `go vet ./...` · `GOOS=linux go build ./...` clean; `goreleaser check` valid; whole module green under `-race`. The real-release publish (tag → tap push) needs a GITHUB_TOKEN and a created tap repo — a user/release-time action, out of scope for the dry-run-verified pipeline.
