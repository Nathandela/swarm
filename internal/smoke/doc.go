// Package smoke holds the Epic 14 real-CLI smoke / characterization harness
// (Epic 11 deferrals D1/D2, bead agents-tracker-54z VERIFY list).
//
// SAFETY: the harness itself is BILLABLE — running it launches the REAL `claude`
// and `codex` CLIs, which requires authentication and incurs cost. Every file
// carrying that logic is gated behind the `realcli` build tag, so it is EXCLUDED
// from the normal `go build ./...`, `go test ./...`, and every CI job (none of
// which pass `-tags realcli`). This untagged file exists only so the package is a
// well-formed, buildable Go package when the tag is absent — it contains no
// harness logic and never touches a CLI.
//
// The harness lives in realcli.go (the engine, `//go:build realcli`) and
// realcli_test.go (the `go test -tags realcli` entrypoint). See
// docs/verification/epic-14-realcli-smoke.md for what it verifies and the exact
// human command to run it.
package smoke
