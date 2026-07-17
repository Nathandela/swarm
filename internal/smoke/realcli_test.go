//go:build realcli

// =============================================================================
//  REAL-CLI SMOKE / CHARACTERIZATION HARNESS  (Epic 14 T13, bead agents-tracker-54z)
// =============================================================================
//
// WHAT THIS DOES
//   Drives the REAL `claude` and `codex` CLIs through the frozen adapter contract
//   (Command/Resume), captures their real hook/event stream + PTY output, and
//   ASSERTS the adapters' descriptors/SignalSources match reality — failing loudly
//   on drift. On drift it RE-RECORDS the adapter fixtures
//   (internal/adapter/{claude,codex}/testdata/*.json) so they track the real
//   format again (T-6). It resolves the Epic 11 deferrals D1 (characterize the
//   real Codex app-server typed-event stream) and D2 (confirm the real Claude hook
//   value names) plus the bead-54z VERIFY list.
//
// !!! BILLABLE — GATED — NEVER IN CI — NEVER AUTOMATIC !!!
//   Running this LAUNCHES the real CLIs, which is a BILLABLE, auth-requiring
//   action. It is protected by TWO gates that must BOTH be satisfied, so it can
//   never run at normal `go test ./...` time or in any CI job:
//     1. the `//go:build realcli` build tag — no CI job passes `-tags realcli`,
//        and the untagged `go build ./...` / `go test ./...` exclude this file;
//     2. the SWARM_REALCLI=1 environment opt-in — without it every subtest SKIPS,
//        so even an accidental `go test -tags realcli ./...` launches nothing.
//   Each subtest additionally SKIPS when its CLI is not found on PATH.
//
// EXACT HUMAN COMMAND (run only when you intend to spend money):
//
//     SWARM_REALCLI=1 go test -tags realcli -run TestRealCLISmoke -v ./internal/smoke
//
//   Optional tuning env (all have safe defaults):
//     SWARM_REALCLI_TIMEOUT           per-run wall clock (default 45s)
//     SWARM_REALCLI_CLAUDE_PROMPT     initial prompt driving Claude to a tool use
//     SWARM_REALCLI_CLAUDE_APPROVE    keystrokes sent to approve Claude's prompt
//     SWARM_REALCLI_CODEX_PROMPT      initial prompt for Codex
//     SWARM_REALCLI_CODEX_APPSERVER   argv for the D1 app-server capture
//                                     (e.g. "codex app-server"); empty means D1 skipped
//     SWARM_REALCLI_CODEX_INIT        path to a JSON-RPC handshake file for the
//                                     app-server's stdin (the handshake is itself
//                                     a VERIFY item — supply it once confirmed)
//     SWARM_REALCLI_UPDATE_FIXTURES   "always" | "on-drift" (default) | "never"
//
//   VERIFY compile-only (NEVER run) — how this file is checked without billing:
//     go build -tags realcli ./...   # compiles realcli.go
//     go vet   -tags realcli ./...   # type-checks this test file too
//   Both must be clean; neither launches a CLI.
// =============================================================================

package smoke

import (
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/adapter/codex"
)

// Fixture paths, relative to this package dir (go test runs with cwd here).
const (
	claudeFixturePath = "../adapter/claude/testdata/claude.json"
	codexFixturePath  = "../adapter/codex/testdata/codex.json"
)

// requireOptIn enforces the SWARM_REALCLI=1 runtime gate (gate #2 above). Without
// it every subtest skips, so a billable launch is impossible by accident.
func requireOptIn(t *testing.T) {
	t.Helper()
	if os.Getenv("SWARM_REALCLI") != "1" {
		t.Skip("real-CLI smoke is BILLABLE and opt-in: set SWARM_REALCLI=1 to run it deliberately")
	}
}

// runTimeout reads the per-run wall-clock bound from the environment.
func runTimeout() time.Duration {
	if v := os.Getenv("SWARM_REALCLI_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 45 * time.Second
}

// envOr returns the env value for key, or def when unset/empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// wantUpdate reports whether a drifted fixture should be re-recorded, per
// SWARM_REALCLI_UPDATE_FIXTURES: "never" disables writes, "always" forces one, and
// the default ("on-drift") records exactly when drift was observed.
func wantUpdate(drift bool) bool {
	switch os.Getenv("SWARM_REALCLI_UPDATE_FIXTURES") {
	case "never":
		return false
	case "always":
		return true
	default:
		return drift
	}
}

// TestRealCLISmoke is the single human-run entrypoint. Its subtests each drive one
// real CLI and characterize it against the adapter contract.
func TestRealCLISmoke(t *testing.T) {
	requireOptIn(t)
	t.Run("claude", testClaudeRealCLI)
	t.Run("codex", testCodexRealCLI)
}

// testClaudeRealCLI launches the real Claude Code CLI via the adapter's Command,
// captures the REAL hook callbacks over the production `swarm hook` transport, and
// verifies the D2 items: the hook event names the adapter declares, the
// Notification subtype field name, and the `Session <uuid>` conversation-id
// surface. On drift it re-records claude.json.
func testClaudeRealCLI(t *testing.T) {
	a := claude.New()
	det := detectCLI(a)
	if !det.Found {
		t.Skipf("claude not found on PATH; install + authenticate it to run this")
	}
	if !det.InRange {
		t.Errorf("DRIFT: claude version %q is outside the adapter's supported range %+v (L-2)",
			det.Version, a.SupportedVersions())
	}
	t.Logf("claude detected: version=%q inRange=%v", det.Version, det.InRange)

	// A prompt that drives Claude to a permission-gated tool use, plus scripted
	// keystrokes to approve it, so PreToolUse / Notification / PermissionRequest /
	// PostToolUse / Stop have a chance to fire. The exact approval keystrokes are
	// UI-dependent; the human can override them via env.
	prompt := envOr("SWARM_REALCLI_CLAUDE_PROMPT", "Run the shell command: echo swarm-realcli-probe")
	approve := envOr("SWARM_REALCLI_CLAUDE_APPROVE", "\r")

	argv, err := a.Command(adapter.LaunchSpec{
		Cwd:           t.TempDir(),
		Options:       map[string]string{},
		InitialPrompt: prompt,
	})
	if err != nil {
		t.Fatalf("claude Command: %v", err)
	}
	t.Logf("claude launch argv: %v", argv)

	timeout := runTimeout()
	res, err := runScenario(scenario{
		argv:        argv,
		cwd:         t.TempDir(),
		captureHook: true,
		timeout:     timeout,
		input: []scriptedInput{
			{delay: timeout / 3, data: approve},
			{delay: timeout / 3, data: "\x03"}, // Ctrl-C to exit the TUI cleanly
		},
	})
	if err != nil {
		t.Fatalf("claude scenario: %v", err)
	}

	drift := false

	// D2 (i): every hook event the CLI actually fired must be one the adapter
	// declares — an undeclared event is a missing signal source.
	declared := declaredHookEvents(a)
	observed := observedEvents(res.callbacks)
	t.Logf("claude hook events observed: %v (declared: %v)", observed, sortedKeys(declared))
	for _, ev := range observed {
		if !declared[ev] {
			drift = true
			t.Errorf("DRIFT: claude fired hook event %q which the adapter does not declare in SignalSources", ev)
		}
	}
	if len(observed) == 0 {
		t.Logf("NOTE: no hook callbacks captured — Claude may not have reached a tool use; " +
			"tune SWARM_REALCLI_CLAUDE_PROMPT/APPROVE and re-run to exercise the permission path")
	}

	// D2 (ii): if a Notification fired, its real payload must carry the field the
	// adapter reads its subtype from (currently "notification_type").
	if field := notificationSubtypeField(a); field != "" {
		if cb, ok := firstCallbackFor(res.callbacks, "Notification"); ok {
			if _, present := cb.Payload[field]; !present {
				drift = true
				t.Errorf("DRIFT: Claude Notification payload has no %q field; adapter reads the subtype from it. payload keys=%v",
					field, sortedKeys(mapKeys(cb.Payload)))
			} else {
				t.Logf("confirmed: Claude Notification carries subtype field %q=%q", field, cb.Payload[field])
			}
		} else {
			t.Logf("NOTE: no Notification fired this run; the %q subtype field stays VERIFY-pending", field)
		}
	}

	// bead-54z: the `Session <uuid>` conversation-id surface must be extractable
	// from the real capture (this is what Resume-as-new-session keys off).
	if id, ok := a.ExtractConversationID(res.grid, res.pty); ok {
		t.Logf("confirmed: extracted Claude conversation id %q from the live capture", id)
		verifyResume(t, a, id)
	} else {
		t.Logf("NOTE: no conversation id extracted from this capture (no `%sxxxx` marker seen); "+
			"conversation-id surface stays VERIFY-pending", "Session ")
	}

	if wantUpdate(drift) {
		fx := adapter.Fixture{
			SchemaVersion: adapter.FixtureSchemaVersion,
			CLI:           a.Name(),
			Version:       versionOr(det.Version, "0.0.0-realcli"),
			Scenario:      "idle-after-permission",
			PTYCapture:    res.pty,
			HookPayloads:  callbacksToHookPayloads(res.callbacks),
		}
		if err := recordFixture(claudeFixturePath, fx); err != nil {
			t.Errorf("re-record claude fixture: %v", err)
		} else {
			t.Logf("RE-RECORDED %s from the live capture (review the git diff before committing)", claudeFixturePath)
		}
	}
}

// testCodexRealCLI launches the real Codex CLI via the adapter's Command, captures
// its PTY, and characterizes the VERIFY items: launch flags, the resume subcommand
// form, and the conversation-id surface. When SWARM_REALCLI_CODEX_APPSERVER is set
// it additionally captures the real app-server typed-event stream (D1) and asserts
// the adapter's declared JSON-RPC method names against it. On drift it re-records
// codex.json.
func testCodexRealCLI(t *testing.T) {
	a := codex.New()
	det := detectCLI(a)
	if !det.Found {
		t.Skipf("codex not found on PATH; install + authenticate it to run this")
	}
	if !det.InRange {
		t.Errorf("DRIFT: codex version %q is outside the adapter's supported range %+v (L-2)",
			det.Version, a.SupportedVersions())
	}
	t.Logf("codex detected: version=%q inRange=%v", det.Version, det.InRange)

	// bead-54z: launch flags — --model / --sandbox must compose and not error the
	// launch. read-only sandbox keeps a probe run from touching the filesystem.
	prompt := envOr("SWARM_REALCLI_CODEX_PROMPT", "print the word swarm-realcli-probe and stop")
	argv, err := a.Command(adapter.LaunchSpec{
		Cwd:           t.TempDir(),
		Options:       map[string]string{"sandbox": "read-only"},
		InitialPrompt: prompt,
	})
	if err != nil {
		t.Fatalf("codex Command: %v", err)
	}
	t.Logf("codex launch argv: %v", argv)

	timeout := runTimeout()
	res, err := runScenario(scenario{
		argv:    argv,
		cwd:     t.TempDir(),
		timeout: timeout,
		input: []scriptedInput{
			{delay: timeout / 3, data: "\x03"}, // Ctrl-C to exit
		},
	})
	if err != nil {
		t.Fatalf("codex scenario: %v", err)
	}

	drift := false

	// bead-54z: the conversation-id surface. Codex's live typed-event producer is
	// deferred (D1), so the interactive PTY may not carry a threadId; this is a
	// report, not a hard failure.
	if id, ok := a.ExtractConversationID(res.grid, res.pty); ok {
		t.Logf("confirmed: extracted Codex conversation id %q from the live capture", id)
		verifyResume(t, a, id)
	} else {
		t.Logf("NOTE: no threadId/conversation-id in the interactive PTY (expected — the typed-event " +
			"producer is the deferred D1 item); characterize it via SWARM_REALCLI_CODEX_APPSERVER")
	}

	// D1: characterize the real app-server typed-event stream, if the human points
	// the harness at it. The exact invocation + handshake are themselves VERIFY
	// items, so both come from the environment rather than a guessed protocol.
	var appServerStream []byte
	if raw := os.Getenv("SWARM_REALCLI_CODEX_APPSERVER"); raw != "" {
		appArgv := strings.Fields(raw)
		var initPayload []byte
		if p := os.Getenv("SWARM_REALCLI_CODEX_INIT"); p != "" {
			if b, rerr := os.ReadFile(p); rerr == nil {
				initPayload = b
			} else {
				t.Logf("NOTE: could not read SWARM_REALCLI_CODEX_INIT %q: %v", p, rerr)
			}
		}
		appServerStream, err = captureAppServer(appArgv, initPayload, timeout)
		if err != nil {
			t.Errorf("codex app-server capture: %v", err)
		} else {
			methods := jsonRPCMethods(appServerStream)
			declared := declaredEventMethods(a)
			t.Logf("codex app-server JSON-RPC methods observed: %v (adapter declares: %v)",
				methods, sortedKeys(declared))
			// Drift = the adapter declares a method the real stream never carried.
			for m := range declared {
				if !containsStr(methods, m) {
					drift = true
					t.Errorf("DRIFT: adapter declares Codex event method %q but the real app-server stream did not carry it", m)
				}
			}
			// Also surface any real method the adapter does not yet declare.
			for _, m := range methods {
				if !declared[m] && looksLikeStatusMethod(m) {
					t.Logf("NOTE: real app-server carried method %q not declared by the adapter — consider mapping it", m)
				}
			}
		}
	} else {
		t.Logf("NOTE: SWARM_REALCLI_CODEX_APPSERVER unset — D1 (typed-event stream) stays VERIFY-pending this run")
	}

	if wantUpdate(drift) {
		// Prefer the app-server JSON-RPC stream (the fixture's native shape); fall
		// back to the interactive PTY when no app-server capture was taken.
		capture := appServerStream
		hooks := jsonRPCHookPayloads(appServerStream)
		if len(capture) == 0 {
			capture = res.pty
			hooks = nil
		}
		if len(capture) == 0 {
			t.Logf("NOTE: nothing captured to record for codex; skipping fixture rewrite")
			return
		}
		fx := adapter.Fixture{
			SchemaVersion: adapter.FixtureSchemaVersion,
			CLI:           a.Name(),
			Version:       versionOr(det.Version, "0.0.0-realcli"),
			Scenario:      "idle-after-turn-completed",
			PTYCapture:    capture,
			HookPayloads:  hooks,
		}
		if err := recordFixture(codexFixturePath, fx); err != nil {
			t.Errorf("re-record codex fixture: %v", err)
		} else {
			t.Logf("RE-RECORDED %s from the live capture (review the git diff before committing)", codexFixturePath)
		}
	}
}

// verifyResume composes the adapter's resume argv from a captured id and confirms
// the resume subcommand/flag is recognized by the real CLI (a short, bounded
// launch). It reports rather than hard-fails on ambiguity, since resume UIs vary.
func verifyResume(t *testing.T, a adapter.Adapter, id string) {
	t.Helper()
	argv, err := a.Resume(adapter.ResumeSpec{Cwd: t.TempDir(), ConversationID: id})
	if err != nil {
		t.Errorf("Resume compose: %v", err)
		return
	}
	if len(argv) == 0 {
		t.Errorf("DRIFT: Resume returned empty argv for a non-empty conversation id %q", id)
		return
	}
	t.Logf("resume argv composed from the live id: %v", argv)

	// A brief bounded launch: if the CLI rejects the resume verb/flag outright, the
	// capture carries an "unknown command"/"unrecognized" marker — that is drift.
	res, err := runScenario(scenario{
		argv:    argv,
		cwd:     t.TempDir(),
		timeout: 12 * time.Second,
		input:   []scriptedInput{{delay: 8 * time.Second, data: "\x03"}},
	})
	if err != nil {
		t.Logf("NOTE: resume launch could not be started: %v", err)
		return
	}
	low := strings.ToLower(string(res.pty))
	for _, marker := range []string{"unknown command", "unrecognized subcommand", "unknown subcommand", "unexpected argument"} {
		if strings.Contains(low, marker) {
			t.Errorf("DRIFT: real CLI rejected the composed resume argv (%v): saw %q in its output", argv, marker)
			return
		}
	}
	t.Logf("resume verb/flag appears accepted by the real CLI")
}

// looksLikeStatusMethod is a cheap filter so the "undeclared method" note surfaces
// turn/exec-style status events, not JSON-RPC lifecycle noise (initialize, etc.).
func looksLikeStatusMethod(m string) bool {
	l := strings.ToLower(m)
	return strings.Contains(l, "turn") || strings.Contains(l, "exec") ||
		strings.Contains(l, "approval") || strings.Contains(l, "command")
}

// --- small test-local helpers ----------------------------------------------

// sortedKeys returns the keys of a set as a sorted slice (for stable log output).
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// mapKeys returns a set of the keys of a string map.
func mapKeys(m map[string]string) map[string]bool {
	set := make(map[string]bool, len(m))
	for k := range m {
		set[k] = true
	}
	return set
}

// containsStr reports whether xs contains s.
func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// versionOr returns v when non-empty, else def (so a re-recorded fixture always
// has a non-empty version even if detection could not parse one).
func versionOr(v, def string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return def
}
