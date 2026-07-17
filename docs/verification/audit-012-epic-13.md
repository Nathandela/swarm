# Audit 012 — Epic 13 (Release packaging)

**Date**: 2026-07-17
**Committee**: codex GPT-5.6 sol (cross-model, source-based) + Opus (independent, ran the gates + otool + ldflags proof) + **Fable** (independent, ran goreleaser v2.17 snapshot END-TO-END). agy quota-blocked. (Fable added to the standing roster per Nathan's instruction.)
**Verdict**: **FIX REQUIRED (narrow).** codex + Opus FIX REQUIRED; Fable CONFIRM-with-follow-ups. All three confirm the engineering core is sound; the blockers are the missing GG-5 evidence (a mandatory project close-gate) plus a set of small real fixes. Fable's end-to-end goreleaser run resolved the codex↔Opus divergences.

## Confirmed sound (all three, several by execution)

- goreleaser: exactly 4 static targets (darwin/linux × amd64/arm64), dev tools excluded, CGO_ENABLED=0, tar.gz + sha256 checksums. Fable RAN `goreleaser check` + `release --snapshot --skip=publish`: 4 archives, `shasum -c` all OK, linux artifacts `file`→statically linked.
- ldflags path `-X github.com/Nathandela/swarm/internal/version.Version` matches package/var exactly; stamp proven end-to-end (build → `swarm version` → OpHello handshake). The stamp test runs in CI (skips only if `go` absent).
- `Control.BuildVersion` is additive/omitempty; `json.Unmarshal` ignores unknowns; server never reads the client's value; `ProtocolVersion` stays the sole skew gate. NO frozen test weakened (git diff proves zero test-file edits); GG-7 drift satisfied legitimately (struct field + protocol.md row added symmetrically).
- N-4 CI gate is real (bash -eo pipefail; `file`/`ldd` fail closed). No secret/token mishandling (`--snapshot --skip=publish`, no GITHUB_TOKEN in the dry-run).
- `packaging/homebrew/swarm.rb` is HONEST generator output — Fable generated it and confirmed byte-identity (modulo version/sha + the DO-NOT-EDIT header) to `dist/homebrew/Casks/swarm.rb`. codex's "provenance faked / snapshot can't generate it" concern is DISPROVEN.

## Fixes (blocking this epic's close)

- **F0 (GG-5, mine) — evidence + red log.** Write docs/verification/epic-13-evidence.md and capture failing-first evidence under docs/verification/epic-13-red/. Mandatory close-gate (implementation-goals.md GG-5 + CLAUDE.md).
- **F1 — cask Gatekeeper quarantine (all three).** The generated cask is unsigned/unnotarized with no quarantine-removal hook; `brew install --cask` → macOS blocks the binary on first run. Add the documented macOS post-install `xattr -dr com.apple.quarantine` hook to `homebrew_casks` (+ reflect in the example swarm.rb). Only manifests on a real published release, but the formula must be actually usable to call E13.3 verified.
- **F2 — install.md Homebrew is macOS-only (Opus + Fable).** Casks error on Linux ("Installing casks is supported only on macOS"). Scope the Homebrew section to macOS; Linux users use `go install` + the static download.
- **F3 — provenance comment (codex).** `.goreleaser.yaml`'s comment calls swarm.rb "hand-written," but it is genuinely generated (F verified). Make the `.goreleaser.yaml` comment + the swarm.rb header consistent + accurate.
- **F4 — CI `go install` verification (codex).** E13.3 requires the `go install` path VERIFIED, not just documented. Add a CI step: `GOBIN=$tmp go install ./cmd/swarm && $tmp/swarm version` (asserts it builds + runs).

## Hardening (small, include in the round)

- **F5** — pin the release-dryrun job to go-version '1.24.2' + a fixed goreleaser-action version (reproducible releases). (Existing jobs' floating '1.24' is pre-existing drift; don't propagate it to the release pipeline.)
- **F6** — CI: assert exactly 4 `swarm_*.tar.gz` AND `sha256sum -c checksums.txt`; add a `file` static-linked assert on the linux/arm64 artifact (currently only amd64).
- **F7** — internal/protocol compat tests: old-client-hello-without-build_version → new server ok; new-client-hello-with-build_version decoded ok; new client → old server omitting build_version → "".

## Nits (optional / documented, not blocking)

- The "daemon probe bypassed entirely" phrasing is imprecise: EnsureDaemon uses the `'V'` liveness probe then closes it; session traffic goes over OpHello (Opus + Fable confirm this is CORRECT design — build version belongs in OpHello). Correct the phrasing if it appears in a comment.
- `version.Commit`/`Date` stamped-but-unread and `Client.BuildVersion()` has no production caller yet (the restart "nudge" is a surfaced capability, honestly documented as "detectable"; no criterion required TUI wiring). Leave as a documented capability.

## Disposition

One tight fix round (F1-F7 + the red-log capture) to e13-packaging; the orchestrator writes epic-13-evidence.md (F0). Re-confirm with the committee (codex + Opus + Fable) before close.
