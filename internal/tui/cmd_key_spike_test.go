package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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

// TestRename_FastEditingKeys is the production contract for native Command-key
// events and their compatibility fallbacks. All cursor positions are rune
// indexes: deleting left of the cursor must preserve the complete Unicode suffix.
func TestRename_FastEditingKeys(t *testing.T) {
	const unicodeName = "aé🐝bc"
	end := utf8.RuneCountInString(unicodeName)
	tests := []struct {
		name       string
		buffer     string
		cursor     int
		key        tea.KeyPressMsg
		wantBuf    string
		wantCursor int
		repeat     int
	}{
		{name: "native command left", buffer: unicodeName, cursor: 3, key: tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModSuper}, wantBuf: unicodeName, wantCursor: 0},
		{name: "xterm compatible command left", buffer: unicodeName, cursor: 3, key: tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModMeta}, wantBuf: unicodeName, wantCursor: 0},
		{name: "legacy home", buffer: unicodeName, cursor: 3, key: tea.KeyPressMsg{Code: tea.KeyHome}, wantBuf: unicodeName, wantCursor: 0},
		{name: "legacy ctrl a", buffer: unicodeName, cursor: 3, key: tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl}, wantBuf: unicodeName, wantCursor: 0},
		{name: "native command right", buffer: unicodeName, cursor: 2, key: tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModSuper}, wantBuf: unicodeName, wantCursor: end},
		{name: "xterm compatible command right", buffer: unicodeName, cursor: 2, key: tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModMeta}, wantBuf: unicodeName, wantCursor: end},
		{name: "legacy end", buffer: unicodeName, cursor: 2, key: tea.KeyPressMsg{Code: tea.KeyEnd}, wantBuf: unicodeName, wantCursor: end},
		{name: "legacy ctrl e", buffer: unicodeName, cursor: 2, key: tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl}, wantBuf: unicodeName, wantCursor: end},
		{name: "native command backspace preserves unicode suffix", buffer: unicodeName, cursor: 3, key: tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModSuper}, wantBuf: "bc", wantCursor: 0},
		{name: "legacy ctrl u preserves unicode suffix", buffer: unicodeName, cursor: 3, key: tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}, wantBuf: "bc", wantCursor: 0},
		{name: "native command backspace at end clears buffer", buffer: unicodeName, cursor: end, key: tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModSuper}, wantBuf: "", wantCursor: 0},
		{name: "legacy ctrl u at end clears buffer", buffer: unicodeName, cursor: end, key: tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}, wantBuf: "", wantCursor: 0},
		{name: "command left at start is idempotent", buffer: unicodeName, cursor: 0, key: tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModSuper}, wantBuf: unicodeName, wantCursor: 0, repeat: 2},
		{name: "command right at end is idempotent", buffer: unicodeName, cursor: end, key: tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModMeta}, wantBuf: unicodeName, wantCursor: end, repeat: 2},
		{name: "command backspace at start is idempotent", buffer: unicodeName, cursor: 0, key: tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModSuper}, wantBuf: unicodeName, wantCursor: 0, repeat: 2},
		{name: "ctrl u on empty buffer is idempotent", buffer: "", cursor: 0, key: tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}, wantBuf: "", wantCursor: 0, repeat: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rm := renameEditorAt(t, tt.buffer, tt.cursor)
			repeat := tt.repeat
			if repeat == 0 {
				repeat = 1
			}
			for range repeat {
				rm = send(rm, tt.key).(rootModel)
			}
			if rm.general.editBuf != tt.wantBuf || rm.general.editCursor != tt.wantCursor {
				t.Fatalf("buffer/cursor = %q/%d, want %q/%d", rm.general.editBuf, rm.general.editCursor, tt.wantBuf, tt.wantCursor)
			}
			if !utf8.ValidString(rm.general.editBuf) {
				t.Fatalf("rename edit split UTF-8: %q", rm.general.editBuf)
			}
		})
	}
}

// TestRename_PlainEditingKeysRemainSingleRuneOperations protects the existing
// editor while fast operations are matched before their unmodified key codes.
func TestRename_PlainEditingKeysRemainSingleRuneOperations(t *testing.T) {
	const unicodeName = "aé🐝bc"
	tests := []struct {
		name       string
		cursor     int
		key        tea.KeyPressMsg
		wantBuf    string
		wantCursor int
	}{
		{name: "left moves one rune", cursor: 3, key: keyLeft, wantBuf: unicodeName, wantCursor: 2},
		{name: "right moves one rune", cursor: 3, key: keyRight, wantBuf: unicodeName, wantCursor: 4},
		{name: "backspace deletes one unicode rune", cursor: 3, key: keyBackspace, wantBuf: "aébc", wantCursor: 2},
		{name: "left at start is no-op", cursor: 0, key: keyLeft, wantBuf: unicodeName, wantCursor: 0},
		{name: "right at end is no-op", cursor: 5, key: keyRight, wantBuf: unicodeName, wantCursor: 5},
		{name: "backspace at start is no-op", cursor: 0, key: keyBackspace, wantBuf: unicodeName, wantCursor: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rm := renameEditorAt(t, unicodeName, tt.cursor)
			rm = send(rm, tt.key).(rootModel)
			if rm.general.editBuf != tt.wantBuf || rm.general.editCursor != tt.wantCursor {
				t.Fatalf("buffer/cursor = %q/%d, want %q/%d", rm.general.editBuf, rm.general.editCursor, tt.wantBuf, tt.wantCursor)
			}
		})
	}
}

// TestRename_PrintableTextStillInsertsWithModifiers prevents exact-modifier
// matching from becoming a blanket modifier filter. Shifted characters,
// Option-produced text, and multi-rune IME text are still ordinary input when
// Bubble Tea supplies non-empty Text.
func TestRename_PrintableTextStillInsertsWithModifiers(t *testing.T) {
	tests := []struct {
		name string
		key  tea.KeyPressMsg
		want string
	}{
		{name: "shifted character", key: tea.KeyPressMsg{Code: 'a', Text: "A", Mod: tea.ModShift}, want: "aAb"},
		{name: "option produced unicode", key: tea.KeyPressMsg{Code: 'é', Text: "é", Mod: tea.ModAlt}, want: "aéb"},
		{name: "multi rune IME text", key: tea.KeyPressMsg{Text: "漢字"}, want: "a漢字b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rm := renameEditorAt(t, "ab", 1)
			rm = send(rm, tt.key).(rootModel)
			if rm.general.editBuf != tt.want || rm.general.editCursor != utf8.RuneCountInString(tt.want)-1 {
				t.Fatalf("printable input buffer/cursor = %q/%d, want %q/%d", rm.general.editBuf, rm.general.editCursor, tt.want, utf8.RuneCountInString(tt.want)-1)
			}
		})
	}
}

// TestRename_UnsupportedModifiedEditingKeysAreNoOps ensures an unrecognized
// modified navigation/deletion gesture cannot silently degrade to the plain
// one-rune operation. Fast bindings intentionally require an exact modifier.
func TestRename_UnsupportedModifiedEditingKeysAreNoOps(t *testing.T) {
	const unicodeName = "aé🐝bc"
	tests := []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{name: "super shift left", key: tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModSuper | tea.ModShift}},
		{name: "meta shift left", key: tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModMeta | tea.ModShift}},
		{name: "option left", key: tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModAlt}},
		{name: "ctrl left", key: tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl}},
		{name: "super shift right", key: tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModSuper | tea.ModShift}},
		{name: "meta shift right", key: tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModMeta | tea.ModShift}},
		{name: "option right", key: tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModAlt}},
		{name: "ctrl right", key: tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModCtrl}},
		{name: "meta backspace", key: tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModMeta}},
		{name: "super shift backspace", key: tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModSuper | tea.ModShift}},
		{name: "option backspace", key: tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModAlt}},
		{name: "ctrl backspace", key: tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModCtrl}},
		{name: "modified home", key: tea.KeyPressMsg{Code: tea.KeyHome, Mod: tea.ModMeta}},
		{name: "modified end", key: tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModSuper}},
		{name: "ctrl shift a", key: tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl | tea.ModShift}},
		{name: "ctrl shift e", key: tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl | tea.ModShift}},
		{name: "ctrl shift u", key: tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl | tea.ModShift}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rm := renameEditorAt(t, unicodeName, 3)
			rm = send(rm, tt.key).(rootModel)
			if rm.general.editBuf != unicodeName || rm.general.editCursor != 3 {
				t.Fatalf("unsupported %s changed buffer/cursor to %q/%d", tt.key.Keystroke(), rm.general.editBuf, rm.general.editCursor)
			}
		})
	}
}

func TestRename_FastNavigationKeepsLongNameViewportVisible(t *testing.T) {
	const (
		name          = "abcdef-é🐝-uvwxyz"
		viewportWidth = 7
	)
	end := utf8.RuneCountInString(name)
	rm := renameEditorAt(t, name, end)

	assertViewport := func(label string, rm rootModel, cursorAtStart bool) {
		t.Helper()
		s, ok := rm.general.sessionByID(rm.general.editID)
		if !ok {
			t.Fatal("edited session disappeared")
		}
		cell := rm.general.nameCell(s, viewportWidth)
		contentWidth := viewportWidth - 1 // nameCell reserves the column separator
		if got := lipgloss.Width(cell); got > contentWidth {
			t.Fatalf("%s viewport width = %d, want <= %d: %q", label, got, contentWidth, cell)
		}
		if !utf8.ValidString(cell) {
			t.Fatalf("%s viewport split UTF-8: %q", label, cell)
		}
		if cursorAtStart && !strings.HasPrefix(cell, "█") {
			t.Fatalf("%s viewport must show cursor at left edge: %q", label, cell)
		}
		if !cursorAtStart && !strings.HasSuffix(cell, "█") {
			t.Fatalf("%s viewport must show cursor at right edge: %q", label, cell)
		}
	}

	assertViewport("initial end", rm, false)
	rm = send(rm, tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModSuper}).(rootModel)
	assertViewport("command left", rm, true)
	rm = send(rm, tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModMeta}).(rootModel)
	assertViewport("command right", rm, false)

	// Prefix deletion also relocates the cursor to the start of the preserved
	// suffix, so its viewport must immediately follow that new cursor.
	rm = renameEditorAt(t, name, 7) // after "abcdef-"
	rm = send(rm, tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModSuper}).(rootModel)
	if rm.general.editBuf != "é🐝-uvwxyz" || rm.general.editCursor != 0 {
		t.Fatalf("prefix deletion buffer/cursor = %q/%d, want é🐝-uvwxyz/0", rm.general.editBuf, rm.general.editCursor)
	}
	assertViewport("command backspace", rm, true)
	s, _ := rm.general.sessionByID(rm.general.editID)
	if cell := rm.general.nameCell(s, viewportWidth); !strings.Contains(cell, "é🐝") {
		t.Fatalf("prefix-delete viewport must retain complete Unicode suffix: %q", cell)
	}
}

func TestRename_StatusAdvertisesFastEditingKeys(t *testing.T) {
	rm := renameEditorAt(t, "name", 4)
	status := rm.generalStatus()
	for _, token := range []string{"⌘←/→", "⌘⌫"} {
		if !strings.Contains(status, token) {
			t.Errorf("rename status %q must advertise %q", status, token)
		}
	}
	lowerStatus := strings.ToLower(status)
	for _, token := range []string{"home/end", "ctrl+u", "save", "cancel"} {
		if !strings.Contains(lowerStatus, token) {
			t.Errorf("rename status %q must advertise %q", status, token)
		}
	}
}

func TestRename_DoesNotEnableGlobalKeyboardEnhancements(t *testing.T) {
	s := sWorking("endpoint/s1", "codex", "~/Code/x", "building", 0)
	s.Name = "name"
	rm := newModel(t, newFakeClient(s), detectMixed()).(rootModel)
	before := rm.View().KeyboardEnhancements
	rm = send(rm, keyRune('e')).(rootModel)
	after := rm.View().KeyboardEnhancements
	if after != before {
		t.Fatalf("entering rename changed global keyboard enhancements from %+v to %+v", before, after)
	}
	if after.ReportEventTypes {
		t.Fatalf("rename requested global keyboard event reporting: %+v", after)
	}
}

// renameEditorAt enters the real inline editor, then places its rune cursor for
// a single production update. Direct cursor placement is test arrangement only;
// the key under test always flows through rootModel.Update/updateRename.
func renameEditorAt(t *testing.T, name string, cursor int) rootModel {
	t.Helper()
	if cursor < 0 || cursor > utf8.RuneCountInString(name) {
		t.Fatalf("invalid test cursor %d for %q", cursor, name)
	}
	s := sWorking("endpoint/s1", "codex", "~/Code/x", "building", 0)
	s.Name = name
	rm := send(newModel(t, newFakeClient(s), detectMixed()), keyRune('e')).(rootModel)
	if !rm.general.editing {
		t.Fatal("failed to enter rename editor")
	}
	rm.general.editCursor = cursor
	return rm
}
