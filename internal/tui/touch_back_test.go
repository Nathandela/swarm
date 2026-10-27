package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestTouchBackDefaultRestoreAndApplyCancel(t *testing.T) {
	store := &fakeLayoutStore{layout: BoardLayout{Grouping: "tag", Ordering: "name", TouchBack: true}}
	m := newLayoutModel(t, newFakeClient(sWorking("endpoint/a", "claude", "/repo", "working", time.Minute)), detectMixed(), store)
	rm := m.(rootModel)
	if !rm.touchBack {
		t.Fatal("saved touchBack preference was not restored")
	}
	m = send(m, keyRune('o'))
	rm = m.(rootModel)
	if !rm.options.touchBack {
		t.Fatal("options draft was not seeded from preference")
	}
	rm.options.focus = optionsFocusTouchBack
	if !strings.Contains(rm.options.hint(), "terminal-wide mouse capture can affect text selection") {
		t.Fatal("missing focused terminal selection hint")
	}
	m = send(m, keyDown)
	m = send(m, keyDown)
	m = send(m, keyRight) // turn off
	m = send(m, keyEsc)
	if m.(rootModel).touchBack != true {
		t.Fatal("cancel changed saved preference")
	}
	m = send(m, keyRune('o'))
	m = send(m, keyDown)
	m = send(m, keyDown)
	m = send(m, keyRight)
	m2, cmd := m.Update(keyEnter)
	execCmd(cmd)
	rm = m2.(rootModel)
	if rm.touchBack {
		t.Fatal("apply did not turn preference off")
	}
	if store.layout.TouchBack {
		t.Fatal("apply did not persist disabled preference")
	}
	if !strings.Contains(stripANSI(rm.options.view(100)), "CLI button") {
		t.Fatal("missing CLI button row")
	}
}

func TestTouchBackAttachPreferenceCapturedAtInvocation(t *testing.T) {
	s := sWorking("endpoint/a", "claude", "/repo", "working", time.Minute)
	r := &recordingRunner{}
	m := New(newFakeClient(s), detectMixed(), WithAttachRunner(r.run)).(rootModel)
	m.touchBack = true
	_, cmd := m.openSelected()
	m.touchBack = false
	msg := cmd().(attachDoneMsg)
	if msg.err != nil || len(r.recorded()) != 1 || !r.recorded()[0].touchBack {
		t.Fatalf("attach did not capture touchBack: %+v", r.recorded())
	}
}

func TestTouchBackAsyncAutoAttachCarriesPreference(t *testing.T) {
	r := &recordingRunner{}
	m := New(newFakeClient(), detectMixed(), WithAttachRunner(r.run)).(rootModel)
	m.touchBack = true
	m2, cmd := m.Update(launchResultMsg{id: "endpoint/new", agent: "claude"})
	if got := m2.(rootModel).screen; got != screenAttach {
		t.Fatalf("async launch screen=%v, want attach", got)
	}
	_ = cmd().(attachDoneMsg)
	if calls := r.recorded(); len(calls) != 1 || !calls[0].touchBack {
		t.Fatalf("auto-attach did not carry touchBack: %+v", calls)
	}
}

func TestTouchBackLegacyLayoutDefaultsOffAndFocusHint(t *testing.T) {
	var layout BoardLayout
	if layout.TouchBack {
		t.Fatal("zero layout must remain default-off")
	}
	path := filepath.Join(t.TempDir(), "layout.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"grouping":"repo","ordering":"name"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewFileLayoutStore(path).LoadLayout()
	if err != nil || loaded.TouchBack {
		t.Fatalf("old layout touchBack=%v err=%v, want off", loaded.TouchBack, err)
	}
	o := optionsModel{focus: optionsFocusTouchBack}
	if !strings.Contains(o.hint(), "terminal-wide mouse capture") {
		t.Fatalf("hint: %q", o.hint())
	}
}

func TestTouchBackDisclosureVisibleAtMinimum(t *testing.T) {
	m := New(newFakeClient(), nil).(rootModel)
	m.screen, m.width, m.height = screenOptions, 32, 8
	m.options = optionsModel{focus: optionsFocusTouchBack, touchBack: true}
	v := ansi.Strip(m.View().Content)
	for _, want := range []string{"CLI button", "Touch captures the whole screen.", "Text selection"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q on minimum screen:\n%s", want, v)
		}
	}
	for _, line := range strings.Split(v, "\n") {
		if ansi.StringWidth(line) > 32 {
			t.Errorf("overflow: %q", line)
		}
	}
}

func TestTouchBackFileRoundTrip(t *testing.T) {
	store := NewFileLayoutStore(filepath.Join(t.TempDir(), "layout.json"))
	want := BoardLayout{Grouping: "repo", Ordering: "activity", TouchBack: true}
	if err := store.SaveLayout(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadLayout()
	if err != nil || got != want {
		t.Fatalf("loaded=%+v err=%v want=%+v", got, err, want)
	}
}
