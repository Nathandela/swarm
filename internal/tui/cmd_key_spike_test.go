package tui

import (
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// TestSpike_CommandKeyWireEncodings characterizes the events Bubble Tea's
// underlying input decoder produces for the two transport families Swarm must
// account for:
//
//   - enhanced Kitty/CSI-u input, which can preserve the macOS Command key as
//     ModSuper; and
//   - legacy terminal mappings, which collapse the same gestures to Home, End,
//     or Ctrl+U before the application sees them.
//
// This is investigation code, not a production keybinding contract.
func TestSpike_CommandKeyWireEncodings(t *testing.T) {
	tests := []struct {
		name string
		wire string
		code rune
		mod  uv.KeyMod
	}{
		// Kitty's standard functional-arrow form collides with xterm modifier 9,
		// which Ultraviolet intentionally exposes as Meta for compatibility.
		{name: "kitty functional super left is xterm meta", wire: "\x1b[1;9D", code: uv.KeyLeft, mod: uv.ModMeta},
		{name: "kitty functional super right is xterm meta", wire: "\x1b[1;9C", code: uv.KeyRight, mod: uv.ModMeta},
		// A CSI-u functional code retains the unambiguous Super bit. Supporting
		// both event shapes makes the model independent of terminal encoding form.
		{name: "csi u super left", wire: "\x1b[57350;9u", code: uv.KeyLeft, mod: uv.ModSuper},
		{name: "csi u super right", wire: "\x1b[57351;9u", code: uv.KeyRight, mod: uv.ModSuper},
		{name: "kitty super backspace", wire: "\x1b[127;9u", code: uv.KeyBackspace, mod: uv.ModSuper},
		{name: "legacy home", wire: "\x1b[H", code: uv.KeyHome},
		{name: "legacy end", wire: "\x1b[F", code: uv.KeyEnd},
		{name: "legacy ctrl a", wire: "\x01", code: 'a', mod: uv.ModCtrl},
		{name: "legacy ctrl e", wire: "\x05", code: 'e', mod: uv.ModCtrl},
		{name: "legacy ctrl u", wire: "\x15", code: 'u', mod: uv.ModCtrl},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var decoder uv.EventDecoder
			n, event := decoder.Decode([]byte(tt.wire))
			if n != len(tt.wire) {
				t.Fatalf("decoded %d bytes, want %d", n, len(tt.wire))
			}
			press, ok := event.(uv.KeyPressEvent)
			if !ok {
				t.Fatalf("event type = %T, want uv.KeyPressEvent", event)
			}
			key := uv.Key(press)
			if key.Code != tt.code || key.Mod != tt.mod {
				t.Fatalf("decoded code/mod = %v/%v (%s), want %v/%v", key.Code, key.Mod, press.Keystroke(), tt.code, tt.mod)
			}
		})
	}
}

// TestSpike_CurrentRenameFastKeyGap records the current v0.12.1 behavior. The
// custom rename editor switches only on key code, so Super+Arrow/Backspace are
// accidentally treated as their one-rune unmodified forms, while Home, End,
// and Ctrl+A/E/U are ignored.
func TestSpike_CurrentRenameFastKeyGap(t *testing.T) {
	newEditor := func() tea.Model {
		s := sWorking("endpoint/s1", "codex", "~/Code/x", "building", 0)
		s.Name = "aébc"
		return send(newModel(t, newFakeClient(s), detectMixed()), keyRune('e'))
	}

	t.Run("modified left moves only one rune", func(t *testing.T) {
		for _, mod := range []tea.KeyMod{tea.ModMeta, tea.ModSuper} {
			m := send(newEditor(), tea.KeyPressMsg{Code: tea.KeyLeft, Mod: mod})
			got := m.(rootModel).general.editCursor
			if got != 3 {
				t.Fatalf("modifier %v cursor = %d, want characterization value 3", mod, got)
			}
		}
	})

	t.Run("home and ctrl a do nothing", func(t *testing.T) {
		for _, key := range []tea.KeyPressMsg{
			{Code: tea.KeyHome},
			{Code: 'a', Mod: tea.ModCtrl},
		} {
			m := send(newEditor(), key)
			got := m.(rootModel).general.editCursor
			if got != 4 {
				t.Fatalf("%s cursor = %d, want characterization value 4", key.Keystroke(), got)
			}
		}
	})

	t.Run("super backspace deletes only one rune", func(t *testing.T) {
		m := send(newEditor(), tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModSuper})
		rm := m.(rootModel)
		if rm.general.editBuf != "aéb" || rm.general.editCursor != 3 {
			t.Fatalf("buffer/cursor = %q/%d, want characterization value aéb/3", rm.general.editBuf, rm.general.editCursor)
		}
	})

	t.Run("ctrl u does nothing", func(t *testing.T) {
		m := send(newEditor(), tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
		rm := m.(rootModel)
		if rm.general.editBuf != "aébc" || rm.general.editCursor != 4 {
			t.Fatalf("buffer/cursor = %q/%d, want characterization value aébc/4", rm.general.editBuf, rm.general.editCursor)
		}
	})
}

// TestSpike_PrototypeFastRenameSemantics exercises a test-only prototype of
// the smallest cross-terminal semantic layer. It proves the desired operations
// are rune-aware without modifying production code.
func TestSpike_PrototypeFastRenameSemantics(t *testing.T) {
	tests := []struct {
		name       string
		cursor     int
		key        tea.KeyPressMsg
		wantBuf    string
		wantCursor int
	}{
		{name: "native command left", cursor: 3, key: tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModSuper}, wantBuf: "aébc", wantCursor: 0},
		{name: "xterm compatible command left", cursor: 3, key: tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModMeta}, wantBuf: "aébc", wantCursor: 0},
		{name: "legacy home", cursor: 3, key: tea.KeyPressMsg{Code: tea.KeyHome}, wantBuf: "aébc", wantCursor: 0},
		{name: "legacy ctrl a", cursor: 3, key: tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl}, wantBuf: "aébc", wantCursor: 0},
		{name: "native command right", cursor: 1, key: tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModSuper}, wantBuf: "aébc", wantCursor: 4},
		{name: "xterm compatible command right", cursor: 1, key: tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModMeta}, wantBuf: "aébc", wantCursor: 4},
		{name: "legacy end", cursor: 1, key: tea.KeyPressMsg{Code: tea.KeyEnd}, wantBuf: "aébc", wantCursor: 4},
		{name: "legacy ctrl e", cursor: 1, key: tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl}, wantBuf: "aébc", wantCursor: 4},
		{name: "native command backspace", cursor: 3, key: tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModSuper}, wantBuf: "c", wantCursor: 0},
		{name: "legacy ctrl u", cursor: 3, key: tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}, wantBuf: "c", wantCursor: 0},
		{name: "delete prefix at start is no-op", cursor: 0, key: tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModSuper}, wantBuf: "aébc", wantCursor: 0},
		{name: "command shift left is reserved", cursor: 3, key: tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModSuper | tea.ModShift}, wantBuf: "aébc", wantCursor: 3},
		{name: "option left is reserved", cursor: 3, key: tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModAlt}, wantBuf: "aébc", wantCursor: 3},
		{name: "option backspace is reserved", cursor: 3, key: tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModAlt}, wantBuf: "aébc", wantCursor: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := generalModel{editing: true, editBuf: "aébc", editCursor: tt.cursor}
			prototypeFastRenameKey(&m, tt.key)
			if m.editBuf != tt.wantBuf || m.editCursor != tt.wantCursor {
				t.Fatalf("buffer/cursor = %q/%d, want %q/%d", m.editBuf, m.editCursor, tt.wantBuf, tt.wantCursor)
			}
			if !utf8.ValidString(m.editBuf) {
				t.Fatalf("prototype split UTF-8: %q", m.editBuf)
			}
		})
	}
}

func prototypeFastRenameKey(m *generalModel, k tea.KeyPressMsg) {
	runeCount := utf8.RuneCountInString(m.editBuf)
	switch {
	case k.Code == tea.KeyHome && k.Mod == 0,
		k.Code == tea.KeyLeft && isPrototypeCommandModifier(k.Mod),
		k.Code == 'a' && k.Mod == tea.ModCtrl:
		m.editCursor = 0
	case k.Code == tea.KeyEnd && k.Mod == 0,
		k.Code == tea.KeyRight && isPrototypeCommandModifier(k.Mod),
		k.Code == 'e' && k.Mod == tea.ModCtrl:
		m.editCursor = runeCount
	case k.Code == tea.KeyBackspace && k.Mod == tea.ModSuper,
		k.Code == 'u' && k.Mod == tea.ModCtrl:
		runes := []rune(m.editBuf)
		if m.editCursor < 0 {
			m.editCursor = 0
		}
		if m.editCursor > len(runes) {
			m.editCursor = len(runes)
		}
		m.editBuf = string(runes[m.editCursor:])
		m.editCursor = 0
	}
}

func isPrototypeCommandModifier(mod tea.KeyMod) bool {
	return mod == tea.ModSuper || mod == tea.ModMeta
}
