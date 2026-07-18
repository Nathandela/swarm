// Package tui failing-test suite for Epic 7 (TUI: general view + launch form).
//
// FAILING TESTS ONLY. These tests are written against the FROZEN public API the
// implementer must provide:
//
//	type Client interface {                       // narrow, stub-friendly (Attach is Epic 8)
//	    List() ([]protocol.SessionView, error)
//	    Launch(protocol.LaunchReq) (string, error)
//	    Kill(id string) error
//	    Delete(id string) error
//	    Subscribe() (<-chan protocol.Event, error)
//	}
//	type AgentInfo struct { Name string; Installed, InRange bool; InstallHint string; Options []adapter.OptionSpec }
//	type DetectFunc func() []AgentInfo
//	func New(c Client, detect DetectFunc) tea.Model
//
// Every test runs against the fake Client below — no live daemon, no socket
// (E7.7). Golden files are generated/refreshed with `go test ./internal/tui/
// -update` (the -update flag is provided by charmbracelet/x/exp/golden, used via
// teatest.RequireEqualOutput) and land at testdata/<TestName>.golden. Goldens are
// ANSI-stripped and time-normalized so the acceptance record is readable and
// wall-clock-independent; a human reviews them against docs/design/ui-preview.html
// (the approved look). Color/highlight are NOT asserted in unit tests (termenv
// strips color without a TTY) — tests assert text, layout, and marker glyphs.
package tui

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	teatest "github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// ---------------------------------------------------------------------------
// Fake Client — the only Client the whole suite ever uses (E7.7: stub-only).
// ---------------------------------------------------------------------------

type fakeClient struct {
	mu       sync.Mutex
	sessions []protocol.SessionView
	events   chan protocol.Event
	listErr  error

	launched  []protocol.LaunchReq
	killed    []string
	deleted   []string
	launchID  string
	launchErr error // when set, Launch returns it (B1 error-surfacing tests)
}

func newFakeClient(sessions ...protocol.SessionView) *fakeClient {
	return &fakeClient{
		sessions: sessions,
		events:   make(chan protocol.Event, 32),
		launchID: "endpoint/new-1",
	}
}

func (f *fakeClient) List() ([]protocol.SessionView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]protocol.SessionView, len(f.sessions))
	copy(out, f.sessions)
	return out, nil
}

func (f *fakeClient) Launch(r protocol.LaunchReq) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.launched = append(f.launched, r)
	if f.launchErr != nil {
		return "", f.launchErr
	}
	return f.launchID, nil
}

func (f *fakeClient) Kill(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, id)
	return nil
}

func (f *fakeClient) Delete(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeClient) Subscribe() (<-chan protocol.Event, error) { return f.events, nil }

// emit pushes a status-change event onto the subscribe stream (drives V-2/V-5).
func (f *fakeClient) emit(e protocol.Event) { f.events <- e }

func (f *fakeClient) launchReqs() []protocol.LaunchReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]protocol.LaunchReq(nil), f.launched...)
}

func (f *fakeClient) killedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.killed...)
}

func (f *fakeClient) deletedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

// ---------------------------------------------------------------------------
// Session builders — one per display group.
// ---------------------------------------------------------------------------

var testNow = time.Now()

func mkSession(id, agent, cwd string, g status.Group, st status.Status, summary string, ago time.Duration) protocol.SessionView {
	return protocol.SessionView{
		EndpointID:   "endpoint",
		ID:           id,
		Agent:        agent,
		Cwd:          cwd,
		Status:       st,
		Group:        g,
		Summary:      summary,
		LastActivity: testNow.Add(-ago),
		CreatedAt:    testNow.Add(-ago - time.Hour),
	}
}

func sNeedsInput(id, agent, cwd, summary string, ago time.Duration) protocol.SessionView {
	return mkSession(id, agent, cwd, status.GroupNeedsInput,
		status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionPermission},
		summary, ago)
}

func sWorking(id, agent, cwd, summary string, ago time.Duration) protocol.SessionView {
	return mkSession(id, agent, cwd, status.GroupWorking,
		status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone},
		summary, ago)
}

func sReview(id, agent, cwd, summary string, ago time.Duration) protocol.SessionView {
	return mkSession(id, agent, cwd, status.GroupReadyForReview,
		status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone},
		summary, ago)
}

func sCompleted(id, agent, cwd, summary string, ago time.Duration) protocol.SessionView {
	return mkSession(id, agent, cwd, status.GroupCompleted,
		status.Status{Process: status.ProcessExited, Turn: status.TurnIdle, Interaction: status.InteractionNone},
		summary, ago)
}

// ---------------------------------------------------------------------------
// DetectFunc builders.
// ---------------------------------------------------------------------------

// claudeSchema is the single-option stub schema the launch tests render. One
// option keeps launch-form Tab navigation deterministic (field order per L-1:
// directory, agent, options..., prompt, worktree).
func claudeSchema() []adapter.OptionSpec {
	return []adapter.OptionSpec{
		{Key: "model", Label: "Model", Type: "choice", Choices: []string{"opus", "sonnet"}, Default: "opus"},
	}
}

// detectMixed: claude installed+in-range (the default pick, carries the schema),
// codex installed but OUT of range (upgrade hint), gemini NOT installed (install
// hint). Exercises L-2 greying on both axes.
func detectMixed() DetectFunc {
	return func() []AgentInfo {
		return []AgentInfo{
			{Name: "claude", Installed: true, InRange: true, InstallHint: "", Options: claudeSchema()},
			{Name: "codex", Installed: true, InRange: false, InstallHint: "upgrade codex to >= 1.2.0"},
			{Name: "gemini", Installed: false, InRange: false, InstallHint: "install: npm i -g @google/gemini-cli"},
		}
	}
}

// ---------------------------------------------------------------------------
// Direct-model helpers (deterministic, no goroutines).
// ---------------------------------------------------------------------------

const (
	testCols = 120
	testRows = 40
)

// Keys are bubbletea v2 KeyPressMsg values. Special keys carry a Code constant;
// printable runes carry Code+Text; Ctrl+X carries the ctrl modifier.
var (
	keyDown  = tea.KeyPressMsg{Code: tea.KeyDown}
	keyUp    = tea.KeyPressMsg{Code: tea.KeyUp}
	keyEnter = tea.KeyPressMsg{Code: tea.KeyEnter}
	keyEsc   = tea.KeyPressMsg{Code: tea.KeyEsc}
	keyTab   = tea.KeyPressMsg{Code: tea.KeyTab}
	keyCtrlX = tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}
)

func keyRune(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

// newModel constructs the model and sizes it (first paint needs a width). Per the
// eager-load pin, New performs the initial List() so the model is render-ready and
// navigable immediately after this call.
func newModel(t *testing.T, c Client, d DetectFunc) tea.Model {
	t.Helper()
	m := New(c, d)
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})
	return m
}

// send applies one message, discarding the command.
func send(m tea.Model, msg tea.Msg) tea.Model {
	m2, _ := m.Update(msg)
	return m2
}

// sendType types a string as individual rune key messages.
func sendType(m tea.Model, s string) tea.Model {
	for _, r := range s {
		m = send(m, keyRune(r))
	}
	return m
}

// execCmd runs a command (and, one level deep, any batched children) purely for
// its side effects — used to trigger the fake's Kill/Delete/Launch. Never called
// on Init's (possibly blocking) subscribe command.
func execCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				c()
			}
		}
	}
}

// cmdQuits reports whether cmd (or, one level deep, any batched child) is a quit,
// so an "Esc quits" assertion holds whether the model returns tea.Quit directly or
// batched with other commands.
func cmdQuits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case tea.QuitMsg:
		return true
	case tea.BatchMsg:
		for _, c := range msg {
			if c == nil {
				continue
			}
			if _, ok := c().(tea.QuitMsg); ok {
				return true
			}
		}
	}
	return false
}

// view returns the model's ANSI-stripped current render. In bubbletea v2 a model
// renders to a tea.View; its screen text is in View.Content (the model returns it
// via tea.NewView / SetContent).
func view(m tea.Model) string { return stripANSI(m.View().Content) }

// ansiRe matches a CSI escape sequence: ESC [ , parameter bytes (0x30-0x3F,
// including the private `<`, `=`, `>`, `?` prefixes), intermediate bytes
// (0x20-0x2F), and a final byte (0x40-0x7E). Covers lipgloss SGR color as well as
// the cursor/mode control sequences the program stream carries.
var ansiRe = regexp.MustCompile("\x1b\\[[0-9:;<=>?]*[ -/]*[@-~]")

func stripANSI(s string) string  { return ansiRe.ReplaceAllString(s, "") }
func stripANSIB(b []byte) []byte { return ansiRe.ReplaceAll(b, nil) }

// elapsedRe matches relative-elapsed tokens ("12m", "1h", "41s", "3d") so goldens
// can be normalized to a wall-clock-independent placeholder.
var elapsedRe = regexp.MustCompile(`\d+[smhd]\b`)

func normalizeTimes(s string) string { return elapsedRe.ReplaceAllString(s, "<t>") }

// lineContaining returns the first rendered line containing sub (or "").
func lineContaining(s, sub string) string {
	for _, ln := range strings.Split(s, "\n") {
		if strings.Contains(ln, sub) {
			return ln
		}
	}
	return ""
}

func homeDir(t *testing.T) string {
	t.Helper()
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		t.Fatalf("cannot resolve home dir: %v", err)
	}
	return h
}

// ---------------------------------------------------------------------------
// teatest helpers (used where event delivery through Subscribe must be driven).
// ---------------------------------------------------------------------------

func startTM(t *testing.T, m tea.Model) *teatest.TestModel {
	t.Helper()
	return teatest.NewTestModel(t, m, teatest.WithInitialTermSize(testCols, testRows))
}

// waitContains blocks until the (ANSI-stripped) cumulative program output
// contains sub, or fails after a bounded wait.
func waitContains(t *testing.T, tm *teatest.TestModel, sub string) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(stripANSIB(b), []byte(sub))
	}, teatest.WithDuration(3*time.Second), teatest.WithCheckInterval(10*time.Millisecond))
}

// quitTM quits the program, failing the test if the shutdown itself errors.
func quitTM(t *testing.T, tm *teatest.TestModel) {
	t.Helper()
	if err := tm.Quit(); err != nil {
		t.Fatalf("quit: %v", err)
	}
}

// finalView quits-and-reads the final single-screen render, ANSI-stripped.
func finalView(t *testing.T, tm *teatest.TestModel) string {
	t.Helper()
	fm := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
	return stripANSI(fm.View().Content)
}
