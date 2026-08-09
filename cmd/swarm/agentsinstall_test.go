package main

// FAILING-FIRST suite for ADR-010 Phase 4 PIECE 1: `swarm agents install` (D3).
//
// One embedded template source renders TWO slash-command documents
// (/swarm-handoff, /swarm-delegate) into every target CLI's convention. The
// FROZEN API the implementer must provide (mirrors the spawnStateDir precedent
// in spawn.go, so a test can point "home" at a temp dir):
//
//	// agentsInstallHome resolves the user's home directory the install writes
//	// under; package-level indirection so a test never touches the real $HOME.
//	var agentsInstallHome = func() (string, error) { return os.UserHomeDir() }
//
//	// runAgents dispatches `swarm agents <verb>`; anything but "install" prints
//	// usage to stderr and exits 2 (main.go wires `case "agents":`).
//	func runAgents(args []string, stdout, stderr io.Writer) int
//
//	// runAgentsInstall is `swarm agents install [--dry-run]`. An unknown flag is
//	// misuse (exit 2, the runSpawn/runSend convention). Success is exit 0.
//	func runAgentsInstall(args []string, stdout, stderr io.Writer) int
//
// Install targets (D3, user-global):
//
//	claude -> <home>/.claude/commands/swarm-handoff.md
//	          <home>/.claude/commands/swarm-delegate.md
//	codex  -> <home>/.codex/prompts/swarm-handoff.md
//	          <home>/.codex/prompts/swarm-delegate.md
//	agy, opencode -> no documented command/prompt-file convention (checked
//	          against docs/research/inter-session-orchestration-landscape.md
//	          and internal/adapter/agy, internal/adapter/opencode): SKIPPED,
//	          reported on stdout as "<cli>: skipped: no known command
//	          convention", nothing written under either name.
//
// Behavior:
//   - Every run (dry or not) ALWAYS regenerates the two files per known CLI
//     (they are generated content); it NEVER touches any other file already
//     present in the same directory.
//   - --dry-run performs no filesystem writes; it prints every path it WOULD
//     write, so an agent can inspect before touching $HOME.
//   - A real run prints one line per file actually written, containing that
//     file's path.
//   - The rendered content of both files carries the shared cheat sheet
//     (cheatSheetLines below) and the POINTERS an authored handoff/delegation
//     document must carry ($SWARM_SESSION_ID, the transcript path, git
//     branch/state, issue ids) — verbatim substrings asserted below. The two
//     variants differ ONLY in intent wording (handoffIntentPhrase vs.
//     delegateIntentPhrase) and the spawn command line they name
//     (handoffSpawnLine vs. delegateSpawnLine, the latter recommending
//     --worktree per D2's default).
//
// RED today: runAgents, runAgentsInstall, and agentsInstallHome do not exist,
// so the package fails to compile on the undefined production symbols.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Frozen content contract — golden-ish key-line assertions (not full-file
// golden diffs): the implementer's template must produce these substrings.
// ---------------------------------------------------------------------------

const (
	// The variant-specific "spawn the new session" line each document names.
	// Delegate recommends --worktree (D2: "delegate: --worktree recommended").
	handoffSpawnLine  = "swarm spawn --cli <target-cli> --handoff <handoff-file>"
	delegateSpawnLine = "swarm spawn --cli <target-cli> --delegate <handoff-file> --worktree"

	// The intent wording distinguishing the two variants (D2/D3: "differ only
	// in intent wording and the flag they name").
	handoffIntentPhrase  = "handoff document"
	delegateIntentPhrase = "delegation document"
)

// cheatSheetLines are the observation-loop verbs BOTH variants must teach
// verbatim, so an agent reading either command file learns the whole surface
// (D3) without a second RPC or a second doc to consult.
//
// The --until example is pinned to a SINGLE runnable value: parseWatchUntil
// (agentverbs.go) takes exactly one --until value, and an unquoted "|" between
// two values is a shell pipe, not an argument — the four-way form is not a line
// a copy-pasting agent could run. The other three values the flag accepts are
// pinned separately, in prose, as watchUntilProseValues (2026-08-07 RED-review
// contract fix 1).
var cheatSheetLines = []string{
	"swarm watch <id> --until ready_for_review",
	"swarm peek <id>",
	`swarm send <id> --text "..."`,
	"swarm send <id> --key enter",
	"swarm ls --json",
	"swarm kill <id>",
}

// unrunnableWatchLine is the pipe-delimited --until form the RED review flagged:
// not runnable, and must never appear in the doc (contract fix 1).
const unrunnableWatchLine = "swarm watch <id> --until needs_input|ready_for_review"

// watchUntilProseValues are the --until values besides the pinned cheat-sheet
// example (ready_for_review) that parseWatchUntil also accepts. The doc must
// mention them in prose near the cheat sheet so an agent learns the whole flag
// without a second doc (contract fix 1).
var watchUntilProseValues = []string{"needs_input", "completed", "change"}

// watchTimeoutExitPhrase pins that the doc names watch's TIMEOUT exit code
// specifically (watchTimeoutExit = 2, agentverbs.go) rather than a blanket claim
// about what exit 2 means everywhere in the CLI — misuse ALSO exits 2 for
// several other verbs (sendpeek.go's misuseExit), so a blanket "exit 2 = misuse"
// line would be actively wrong for watch (contract fix 2).
const watchTimeoutExitPhrase = "watch exits 2 on timeout"

// blanketExitTwoMisuseClaim is the wrong generalization the doc must never make
// (contract fix 2).
const blanketExitTwoMisuseClaim = "exit 2 = misuse"

// sendNoSubmitMention pins that the cheat sheet teaches send's draft mode
// (sendpeek.go's --no-submit: leave the text unsent instead of submitting it —
// contract fix 3).
const sendNoSubmitMention = "--no-submit"

// pointerSubstrings are the handoff-document POINTERS (D2) both variants must
// instruct the authoring agent to record.
var pointerSubstrings = []string{
	"$SWARM_SESSION_ID",
	"transcript",
	"git branch",
	"issue",
}

// installFile names one expected generated file by a short test label.
type installFile struct {
	label   string // e.g. "claude-handoff"
	relpath string // relative to home
}

// expectedInstallFiles is the frozen set of paths `swarm agents install`
// writes under a given home (D3: claude + codex, both handoff + delegate).
func expectedInstallFiles() []installFile {
	return []installFile{
		{"claude-handoff", filepath.Join(".claude", "commands", "swarm-handoff.md")},
		{"claude-delegate", filepath.Join(".claude", "commands", "swarm-delegate.md")},
		{"codex-handoff", filepath.Join(".codex", "prompts", "swarm-handoff.md")},
		{"codex-delegate", filepath.Join(".codex", "prompts", "swarm-delegate.md")},
	}
}

// firstLine is the document's opening line, for invocation-title diagnostics.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// withInstallHome points agentsInstallHome at dir for the duration of the
// test (the spawnStateDir override precedent, spawn_test.go).
func withInstallHome(t *testing.T, dir string) {
	t.Helper()
	prev := agentsInstallHome
	agentsInstallHome = func() (string, error) { return dir, nil }
	t.Cleanup(func() { agentsInstallHome = prev })
}

// ---------------------------------------------------------------------------
// Content + write-target tests.
// ---------------------------------------------------------------------------

func TestAgentsInstall_WritesClaudeAndCodexCommandFiles(t *testing.T) {
	home := t.TempDir()
	withInstallHome(t, home)

	var stdout, stderr bytes.Buffer
	if exit := runAgentsInstall(nil, &stdout, &stderr); exit != 0 {
		t.Fatalf("runAgentsInstall exit = %d, want 0 (stderr: %s)", exit, stderr.String())
	}

	for _, f := range expectedInstallFiles() {
		path := filepath.Join(home, f.relpath)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: file not written at %s: %v", f.label, path, err)
		}
		text := string(body)

		for _, line := range cheatSheetLines {
			if !strings.Contains(text, line) {
				t.Errorf("%s: missing cheat-sheet line %q", f.label, line)
			}
		}
		for _, sub := range pointerSubstrings {
			if !strings.Contains(text, sub) {
				t.Errorf("%s: missing pointer substring %q", f.label, sub)
			}
		}

		if strings.Contains(text, unrunnableWatchLine) {
			t.Errorf("%s: doc must not carry the unrunnable pipe form %q", f.label, unrunnableWatchLine)
		}
		for _, v := range watchUntilProseValues {
			if !strings.Contains(text, v) {
				t.Errorf("%s: missing --until value %q in prose near the watch cheat-sheet line", f.label, v)
			}
		}
		if !strings.Contains(text, watchTimeoutExitPhrase) {
			t.Errorf("%s: missing the watch-timeout exit code phrase %q", f.label, watchTimeoutExitPhrase)
		}
		if strings.Contains(text, blanketExitTwoMisuseClaim) {
			t.Errorf("%s: doc must not claim a blanket %q", f.label, blanketExitTwoMisuseClaim)
		}
		if !strings.Contains(text, sendNoSubmitMention) {
			t.Errorf("%s: missing the send draft-mode mention %q", f.label, sendNoSubmitMention)
		}

		// The temp-file line must be runnable on BSD mktemp too (darwin substitutes
		// only TRAILING X's; a .md suffix is taken literally, so every invocation
		// would collide on one fixed path). Trailing X's, no suffix.
		if strings.Contains(text, "XXXXXX.md") {
			t.Errorf("%s: mktemp template carries a suffix after the X's — literal on BSD mktemp", f.label)
		}
		if !strings.Contains(text, "mktemp /tmp/swarm-") || !strings.Contains(text, "-XXXXXX`") {
			t.Errorf("%s: missing a runnable trailing-X mktemp line", f.label)
		}

		// Each CLI's copy must name ITS invocation form: claude command files are
		// invoked /swarm-<slug>, codex prompt files /prompts:swarm-<slug>
		// (docs/research/inter-session-orchestration-landscape.md).
		slug := "handoff"
		if strings.Contains(f.label, "delegate") {
			slug = "delegate"
		}
		wantTitle := "# /swarm-" + slug
		if strings.HasPrefix(f.label, "codex") {
			wantTitle = "# /prompts:swarm-" + slug
		}
		if !strings.HasPrefix(text, wantTitle+"\n") {
			t.Errorf("%s: document must open with %q, got %q", f.label, wantTitle, firstLine(text))
		}

		isHandoff := strings.Contains(f.label, "handoff")
		switch {
		case isHandoff && !strings.Contains(text, handoffSpawnLine):
			t.Errorf("%s: missing handoff spawn line %q", f.label, handoffSpawnLine)
		case isHandoff && !strings.Contains(text, handoffIntentPhrase):
			t.Errorf("%s: missing handoff intent phrase %q", f.label, handoffIntentPhrase)
		case isHandoff && strings.Contains(text, delegateIntentPhrase):
			t.Errorf("%s: handoff document must not carry the delegate intent phrase", f.label)
		case !isHandoff && !strings.Contains(text, delegateSpawnLine):
			t.Errorf("%s: missing delegate spawn line %q", f.label, delegateSpawnLine)
		case !isHandoff && !strings.Contains(text, delegateIntentPhrase):
			t.Errorf("%s: missing delegate intent phrase %q", f.label, delegateIntentPhrase)
		case !isHandoff && strings.Contains(text, handoffIntentPhrase):
			t.Errorf("%s: delegate document must not carry the handoff intent phrase", f.label)
		}
	}

	out := stdout.String()
	for _, f := range expectedInstallFiles() {
		path := filepath.Join(home, f.relpath)
		if !strings.Contains(out, path) {
			t.Errorf("stdout must report writing %s, got:\n%s", path, out)
		}
	}
}

func TestAgentsInstall_DryRunWritesNothingButPrintsPaths(t *testing.T) {
	home := t.TempDir()
	withInstallHome(t, home)

	var stdout, stderr bytes.Buffer
	if exit := runAgentsInstall([]string{"--dry-run"}, &stdout, &stderr); exit != 0 {
		t.Fatalf("dry-run exit = %d, want 0 (stderr: %s)", exit, stderr.String())
	}

	out := stdout.String()
	for _, f := range expectedInstallFiles() {
		path := filepath.Join(home, f.relpath)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s: --dry-run must not write %s (stat err: %v)", f.label, path, err)
		}
		if !strings.Contains(out, path) {
			t.Errorf("dry-run stdout must report the path it WOULD write: %s, got:\n%s", path, out)
		}
	}

	// Stronger than "no files": --dry-run must not even MkdirAll the target
	// directories before deciding what it would write (contract fix 4).
	for _, dir := range []string{
		filepath.Join(home, ".claude"),
		filepath.Join(home, ".claude", "commands"),
		filepath.Join(home, ".codex"),
		filepath.Join(home, ".codex", "prompts"),
	} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("--dry-run must not create directory %s (stat err: %v)", dir, err)
		}
	}
}

func TestAgentsInstall_UnknownConventionCLIsSkipped(t *testing.T) {
	home := t.TempDir()
	withInstallHome(t, home)

	var stdout, stderr bytes.Buffer
	if exit := runAgentsInstall(nil, &stdout, &stderr); exit != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", exit, stderr.String())
	}

	out := stdout.String()
	for _, cli := range []string{"agy", "opencode"} {
		if !strings.Contains(out, cli) || !strings.Contains(out, "skipped") || !strings.Contains(out, "no known command convention") {
			t.Errorf("stdout must report %q as \"skipped: no known command convention\", got:\n%s", cli, out)
		}
	}

	// A skip must write NOTHING: home must carry exactly the claude/codex dirs
	// this run created, never a guessed agy/opencode path.
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("read home dir: %v", err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	sort.Strings(got)
	want := []string{".claude", ".codex"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("home dir entries = %v, want exactly %v (a skipped CLI writes nothing)", got, want)
	}
}

func TestAgentsInstall_NeverTouchesOtherFilesInTargetDir(t *testing.T) {
	home := t.TempDir()
	withInstallHome(t, home)

	const otherContent = "# not swarm's file\nkeep me untouched\n"
	claudeDir := filepath.Join(home, ".claude", "commands")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	otherClaudePath := filepath.Join(claudeDir, "my-other-command.md")
	if err := os.WriteFile(otherClaudePath, []byte(otherContent), 0o644); err != nil {
		t.Fatal(err)
	}

	codexDir := filepath.Join(home, ".codex", "prompts")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	otherCodexPath := filepath.Join(codexDir, "unrelated.md")
	if err := os.WriteFile(otherCodexPath, []byte(otherContent), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if exit := runAgentsInstall(nil, &stdout, &stderr); exit != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", exit, stderr.String())
	}

	for _, p := range []string{otherClaudePath, otherCodexPath} {
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("unrelated file %s must survive install: %v", p, err)
		}
		if string(got) != otherContent {
			t.Errorf("unrelated file %s was modified by install, got %q", p, got)
		}
	}

	entries, err := os.ReadDir(claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		names[e.Name()] = true
	}
	for _, want := range []string{"my-other-command.md", "swarm-handoff.md", "swarm-delegate.md"} {
		if !names[want] {
			t.Errorf("%s missing from %s after install, dir has %v", want, claudeDir, entries)
		}
	}
	if len(entries) != 3 {
		t.Errorf("%s has %d entries after install, want exactly 3 (the pre-existing file plus the two generated ones): %v",
			claudeDir, len(entries), entries)
	}
}

func TestAgentsInstall_OverwritesExistingGeneratedFile(t *testing.T) {
	home := t.TempDir()
	withInstallHome(t, home)

	claudeDir := filepath.Join(home, ".claude", "commands")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stalePath := filepath.Join(claudeDir, "swarm-handoff.md")
	if err := os.WriteFile(stalePath, []byte("STALE PRE-REGENERATION CONTENT"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if exit := runAgentsInstall(nil, &stdout, &stderr); exit != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", exit, stderr.String())
	}

	got, err := os.ReadFile(stalePath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if strings.Contains(text, "STALE PRE-REGENERATION CONTENT") {
		t.Error("install must always regenerate a previously-installed file; stale content survived")
	}
	if !strings.Contains(text, handoffSpawnLine) {
		t.Error("the overwritten file must carry the fresh rendered content")
	}
}

func TestAgentsInstall_UnknownFlagIsMisuse(t *testing.T) {
	home := t.TempDir()
	withInstallHome(t, home)

	var stdout, stderr bytes.Buffer
	if exit := runAgentsInstall([]string{"--bogus"}, &stdout, &stderr); exit != 2 {
		t.Fatalf("runAgentsInstall([--bogus]) exit = %d, want 2 (stderr: %s)", exit, stderr.String())
	}
}

// TestAgentsInstall_HomeResolutionErrorExitsOneWritesNothing — an injectable-home
// failure (a real $HOME lookup can fail, e.g. no passwd entry) is a daemon/file-I/O
// class error, not misuse: exit 1 (the runSpawn convention), the failure named on
// stderr, nothing written (contract fix 5).
func TestAgentsInstall_HomeResolutionErrorExitsOneWritesNothing(t *testing.T) {
	prev := agentsInstallHome
	wantErr := errors.New("cannot resolve home: $HOME not set")
	agentsInstallHome = func() (string, error) { return "", wantErr }
	t.Cleanup(func() { agentsInstallHome = prev })

	var stdout, stderr bytes.Buffer
	if exit := runAgentsInstall(nil, &stdout, &stderr); exit != 1 {
		t.Fatalf("runAgentsInstall exit = %d, want 1 when home resolution fails (stderr: %s)", exit, stderr.String())
	}
	if !strings.Contains(stderr.String(), wantErr.Error()) {
		t.Errorf("stderr must name the home-resolution failure %q, got %q", wantErr.Error(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("a home-resolution failure must write nothing (no paths printed), got %q", stdout.String())
	}
}

// TestAgentsInstall_WriteFailureMidwayExitsOneNamesFailureOnStderr — a write that
// fails partway through the four targets (not the very first one) must still exit 1
// and name the failing PATH on stderr, so an agent scripting the install can tell
// which target failed rather than guessing from a bare error (contract fix 5).
//
// The SECOND target (claude-delegate) is made unwritable via a read-only directory:
// the FIRST target (claude-handoff) is pre-created so its write only needs to
// truncate an existing file (no directory write permission required), while the
// second target is a brand-new file the read-only directory refuses to create.
func TestAgentsInstall_WriteFailureMidwayExitsOneNamesFailureOnStderr(t *testing.T) {
	home := t.TempDir()
	withInstallHome(t, home)

	claudeDir := filepath.Join(home, ".claude", "commands")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(claudeDir, "swarm-handoff.md")
	if err := os.WriteFile(firstPath, []byte("placeholder"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(claudeDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(claudeDir, 0o755) }) // let t.TempDir clean up

	var stdout, stderr bytes.Buffer
	if exit := runAgentsInstall(nil, &stdout, &stderr); exit != 1 {
		t.Fatalf("runAgentsInstall exit = %d, want 1 when a write fails midway (stderr: %s)", exit, stderr.String())
	}

	if got, err := os.ReadFile(firstPath); err != nil || string(got) == "placeholder" {
		t.Errorf("the first target must still be (over)written before the second fails; got %q, err %v", got, err)
	}

	failingPath := filepath.Join(claudeDir, "swarm-delegate.md")
	if !strings.Contains(stderr.String(), failingPath) {
		t.Errorf("stderr must name the failing file %s, got %q", failingPath, stderr.String())
	}
}

// ---------------------------------------------------------------------------
// Dispatch wiring (main.go's `case "agents":`).
// ---------------------------------------------------------------------------

// agentsUsage tests pin AGENTS-SPECIFIC usage (mentioning the "install"
// subcommand), not just the generic top-level usage block a bare unknown verb
// falls through to — a naive `case "agents":` that dispatches straight to
// runAgents but never actually documents "install" would still print SOME
// "usage" string and pass the weaker pre-fix assertion (contract fix 8).
func TestDispatch_AgentsNoSubcommandPrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := dispatch([]string{"agents"}, &stdout, &stderr); exit != 2 {
		t.Fatalf("dispatch([agents]) exit = %d, want 2", exit)
	}
	out := strings.ToLower(stderr.String())
	if !strings.Contains(out, "usage") {
		t.Errorf("dispatch([agents]) stderr = %q, want a usage message", stderr.String())
	}
	if !strings.Contains(out, "install") {
		t.Errorf("dispatch([agents]) stderr = %q, want the agents-specific usage naming \"install\"", stderr.String())
	}
}

func TestDispatch_AgentsUnknownSubcommandPrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := dispatch([]string{"agents", "bogus"}, &stdout, &stderr); exit != 2 {
		t.Fatalf("dispatch([agents bogus]) exit = %d, want 2", exit)
	}
	out := strings.ToLower(stderr.String())
	if !strings.Contains(out, "usage") {
		t.Errorf("dispatch([agents bogus]) stderr = %q, want a usage message", stderr.String())
	}
	if !strings.Contains(out, "install") {
		t.Errorf("dispatch([agents bogus]) stderr = %q, want the agents-specific usage naming \"install\"", stderr.String())
	}
}

// TestUsage_ListsAgentsVerb pins that `swarm agents` is discoverable from bare
// `swarm`/an unknown top-level verb, alongside spawn/ls/watch/kill/send/peek
// (agentverbs_test.go's TestUsage_ListsAgentVerbs, spawn_test.go's
// TestUsage_ListsSpawnVerb, sendpeek_test.go's TestUsage_ListsSteeringVerbs —
// contract fix 8).
func TestUsage_ListsAgentsVerb(t *testing.T) {
	if !strings.Contains(usage, "swarm agents") {
		t.Errorf("usage does not document `swarm agents`; got:\n%s", usage)
	}
}

// TestDispatch_AgentsInstallNeedsNoDaemon proves `swarm agents install` is
// wired as a direct dispatch case (like `remote`), NOT through
// dispatchAgentVerb — this verb writes local files only and must work with no
// daemon socket to dial at all.
func TestDispatch_AgentsInstallNeedsNoDaemon(t *testing.T) {
	home := t.TempDir()
	withInstallHome(t, home)

	var stdout, stderr bytes.Buffer
	if exit := dispatch([]string{"agents", "install", "--dry-run"}, &stdout, &stderr); exit != 0 {
		t.Fatalf("dispatch([agents install --dry-run]) exit = %d, want 0 (stderr: %s)", exit, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "commands", "swarm-handoff.md")); !os.IsNotExist(err) {
		t.Fatalf("dry-run through dispatch must not write files (stat err: %v)", err)
	}
}
