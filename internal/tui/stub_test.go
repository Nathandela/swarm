package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// E7.7 — every TUI unit test runs against a protocol stub. This is enforced by
// construction (the whole suite only ever constructs New with *fakeClient), and
// pinned here at compile time: the fake satisfies the narrow tui.Client interface,
// and New accepts it. No live daemon, no socket.
var _ Client = (*fakeClient)(nil)

func TestStub_NewAcceptsFakeClientNoDaemon(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "compiling", time.Minute))

	var m tea.Model = New(f, detectMixed())
	if m == nil {
		t.Fatal("New returned nil model")
	}
	// Init must return a command (it wires the Subscribe stream) without needing a
	// real daemon; we do not run it here (it may block on the event channel).
	_ = m.Init()

	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})
	if m.View().Content == "" {
		t.Fatal("model rendered nothing against the stub client")
	}
}
