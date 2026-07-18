package tui

// v0.4 P2 — session naming (bd agents-tracker-4e2). Failing-first tests for the TUI
// half: an optional name field on the launch form (positioned right after directory,
// text-editable + paste), a default label composed at submit ("<agent>-<base cwd>"),
// the name shown on board rows and the transition banner (falling back to the agent
// label when unset — version-skew safety), and the name carried into the post-launch
// auto-attach and a resume.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Nathandela/swarm/internal/protocol"
)

// The name field renders between directory and agent (positioned right after
// directory in the L-1 field order).
func TestLaunch_NameFieldRendersAfterDirectory(t *testing.T) {
	m := openLaunch(t, newFakeClient())
	v := view(m)
	di := strings.Index(v, "directory")
	ni := strings.Index(v, "name")
	ai := strings.Index(v, "agent")
	if di < 0 || ni < 0 || ai < 0 {
		t.Fatalf("launch form must render directory, name, and agent labels:\n%s", v)
	}
	if !(di < ni && ni < ai) {
		t.Fatalf("name must sit between directory and agent (dir=%d name=%d agent=%d):\n%s", di, ni, ai, v)
	}
}

// The name field is a text field: typed runes append, Backspace deletes, and a
// bracketed paste lands with newlines stripped (single-line), like directory/prompt.
func TestLaunch_NameFieldEditable(t *testing.T) {
	m := openLaunch(t, newFakeClient()) // focus starts on directory
	m = send(m, keyTab)                 // directory -> name
	if !launchOf(m).isName() {
		t.Fatalf("Tab from directory must focus the name field; focus=%d", launchOf(m).focus)
	}
	m = sendType(m, "backend")
	if got := launchOf(m).name; got != "backend" {
		t.Fatalf("typed name = %q, want backend", got)
	}
	m = send(m, keyBackspace)
	if got := launchOf(m).name; got != "backen" {
		t.Fatalf("after backspace name = %q, want backen", got)
	}
	m = send(m, tea.PasteMsg{Content: "d-refactor\n"})
	if got := launchOf(m).name; got != "backend-refactor" {
		t.Fatalf("after paste name = %q, want backend-refactor (newline stripped)", got)
	}
}

// Submitting with an empty name defaults it to "<agent>-<base of cwd>", composed at
// submit, so every session gets a disambiguating label even when the user types none.
func TestLaunch_DefaultNameComposedAtSubmit(t *testing.T) {
	f := newFakeClient()
	m := openLaunch(t, f)

	dir := t.TempDir()
	for launchOf(m).cwd != "" {
		m = send(m, keyBackspace)
	}
	m = sendType(m, dir)

	_, cmd := m.Update(keyEnter)
	execCmd(cmd)

	reqs := f.launchReqs()
	if len(reqs) != 1 {
		t.Fatalf("expected exactly one launch, got %d", len(reqs))
	}
	want := "claude-" + filepath.Base(dir)
	if reqs[0].Name != want {
		t.Fatalf("default name = %q, want %q", reqs[0].Name, want)
	}
}

// A user-typed name is submitted verbatim (never overridden by the default).
func TestLaunch_TypedNameSubmitted(t *testing.T) {
	f := newFakeClient()
	m := openLaunch(t, f)

	dir := t.TempDir()
	for launchOf(m).cwd != "" {
		m = send(m, keyBackspace)
	}
	m = sendType(m, dir)
	m = send(m, keyTab) // directory -> name
	m = sendType(m, "my-session")

	_, cmd := m.Update(keyEnter)
	execCmd(cmd)

	reqs := f.launchReqs()
	if len(reqs) != 1 {
		t.Fatalf("expected exactly one launch, got %d", len(reqs))
	}
	if reqs[0].Name != "my-session" {
		t.Fatalf("typed name = %q, want my-session", reqs[0].Name)
	}
}

// displayName is the identity shown for a session: the name when set, else the agent
// (the version-skew fallback that keeps the identity column non-blank).
func TestDisplayName_NameThenAgentFallback(t *testing.T) {
	if got := displayName(protocol.SessionView{Agent: "claude", Name: "backend-refactor"}); got != "backend-refactor" {
		t.Fatalf("displayName with a name = %q, want backend-refactor", got)
	}
	if got := displayName(protocol.SessionView{Agent: "codex"}); got != "codex" {
		t.Fatalf("displayName without a name must fall back to the agent = %q, want codex", got)
	}
}

// A named session's board row shows the name (the disambiguator field-test 3 needs).
func TestGeneral_RowShowsSessionName(t *testing.T) {
	s := sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute)
	s.Name = "backend-refactor"
	m := newModel(t, newFakeClient(s), detectMixed())

	row := lineContaining(view(m), "building")
	if row == "" {
		t.Fatalf("no row carried the summary; view:\n%s", view(m))
	}
	if !strings.Contains(row, "backend-refactor") {
		t.Fatalf("row must show the session name:\n%s", row)
	}
}

// A session with no name falls back to the agent label on the board — never a blank
// identity column (an older daemon that predates naming, or a never-named session).
func TestGeneral_RowFallsBackToAgentWhenNameEmpty(t *testing.T) {
	m := newModel(t, newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "building", time.Minute)), detectMixed())

	row := lineContaining(view(m), "building")
	if !strings.Contains(row, "codex") {
		t.Fatalf("an unnamed row must fall back to the agent label:\n%s", row)
	}
}

// The V-5 transition banner names the session by its display name (name when set).
func TestBanner_UsesSessionNameOnTransition(t *testing.T) {
	start := sWorking("endpoint/s1", "claude", "~/Code/x", "building", 2*time.Minute)
	start.Name = "backend-refactor"
	f := newFakeClient(start)
	tm := startTM(t, New(f, detectMixed()))
	waitContains(t, tm, "building")

	ev := sNeedsInput("endpoint/s1", "claude", "~/Code/x", "Permission: run tests?", 0)
	ev.Name = "backend-refactor"
	f.emit(protocol.Event{Session: ev})

	waitContains(t, tm, "backend-refactor needs input")
	quitTM(t, tm)
}

// A successful launch carries the composed name into the auto-attach, so the attach
// chrome hint identifies the new session by its name (not the bare agent).
func TestLaunchResult_AutoAttachCarriesName(t *testing.T) {
	f := newFakeClient()
	r := &recordingRunner{}
	m := New(f, detectMixed(), WithAttachRunner(r.run))
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})

	m2, cmd := m.Update(launchResultMsg{id: "endpoint/new-1", agent: "claude", name: "backend-refactor"})
	if cmd != nil {
		if msg := cmd(); msg != nil {
			m2, _ = m2.Update(msg)
		}
	}
	calls := r.recorded()
	if len(calls) != 1 {
		t.Fatalf("a successful launch must auto-attach exactly once; runner called %d times", len(calls))
	}
	if calls[0].session.Name != "backend-refactor" {
		t.Fatalf("auto-attach session name = %q, want backend-refactor", calls[0].session.Name)
	}
}

// Resuming a named session carries the source's name into the resume launch, so the
// resumed session keeps its label.
func TestResume_CarriesSourceName(t *testing.T) {
	src := sCompleted("endpoint/done1", "claude", "~/Code/x", "exit 0", time.Hour)
	src.Name = "backend-refactor"
	f := newFakeClient(src)
	m := newModel(t, f, detectMixed())

	_, cmd := m.Update(keyRune('r'))
	execCmd(cmd)

	reqs := f.launchReqs()
	if len(reqs) != 1 {
		t.Fatalf("expected exactly one resume launch, got %d", len(reqs))
	}
	if reqs[0].Name != "backend-refactor" {
		t.Fatalf("resume must carry the source session's name; got %q", reqs[0].Name)
	}
}
