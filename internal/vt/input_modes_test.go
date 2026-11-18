package vt

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInputModes_CodexStartupSequenceAndSplits(t *testing.T) {
	stream := "\x1b[?2004h\x1b[>4;0m\x1b[>7u\x1b[?1004h\x1b[?1049h\x1b[>4;0m\x1b[>7u\x1b[?1007l\x1b[?1000h\x1b[?1002h\x1b[?1003h\x1b[?1006h"
	for split := 0; split <= len(stream); split++ {
		e := NewEmulator(80, 23)
		e.Feed([]byte(stream[:split]))
		e.Feed([]byte(stream[split:]))
		snap := snapshotDecode(t, e)
		_ = e.Close()

		if !snap.AltScreen {
			t.Fatalf("split=%d AltScreen = false, want true", split)
		}
		if snap.Mouse == nil || snap.Mouse.Known {
			t.Fatalf("split=%d snap.Mouse = %+v, want Known=false for multi-bit mask", split, snap.Mouse)
		}
		wantMouse := MouseState{
			Tracking: MouseTrackNormal | MouseTrackButton | MouseTrackAny,
			Encoding: MouseEncodeSGR,
			Known:    false,
		}
		if *snap.Mouse != wantMouse {
			t.Fatalf("split=%d snap.Mouse = %+v, want %+v", split, *snap.Mouse, wantMouse)
		}
		if snap.InputModes == nil {
			t.Fatalf("split=%d snap.InputModes = nil", split)
		}
		im := snap.InputModes
		if !im.Known || im.Tracking != 1003 || im.Encoding != 1006 || im.AltScroll == nil || *im.AltScroll || !im.BracketedPaste || !im.FocusEvents || im.CursorKeys {
			t.Fatalf("split=%d unexpected InputModes: %+v", split, im)
		}
	}
}

func TestInputModes_TransitionsResetAndHostileValidation(t *testing.T) {
	e := NewEmulator(80, 24)
	defer func() { _ = e.Close() }()

	// Last-set-wins across mouse tracking modes, and cursor keys.
	e.Feed([]byte("\x1b[?1h\x1b[?1003h\x1b[?1000h\x1b[?1006h"))
	snap := snapshotDecode(t, e)
	if snap.InputModes == nil || !snap.InputModes.Known || snap.InputModes.Tracking != 1000 || snap.InputModes.Encoding != 1006 || !snap.InputModes.CursorKeys {
		t.Fatalf("last-set-wins failed: %+v", snap.InputModes)
	}

	// DECSTR clears CursorKeys while preserving InputModes.Known and active tracking.
	e.Feed([]byte("\x1b[!p"))
	snap = snapshotDecode(t, e)
	if snap.Mouse == nil || snap.Mouse.Known {
		t.Fatalf("DECSTR kept Snap.Mouse authoritative: %+v", snap.Mouse)
	}
	if snap.InputModes == nil || !snap.InputModes.Known || snap.InputModes.CursorKeys || snap.InputModes.Tracking != 1000 {
		t.Fatalf("DECSTR failed to clear CursorKeys or preserve InputModes: %+v", snap.InputModes)
	}

	// Disabling the currently active mouse tracking mode resets Tracking to 0.
	e.Feed([]byte("\x1b[?1002h\x1b[?1003h\x1b[?1003l"))
	snap = snapshotDecode(t, e)
	if snap.InputModes == nil || snap.InputModes.Tracking != 0 {
		t.Fatalf("disabling active mode left Tracking = %d, want 0", snap.InputModes.Tracking)
	}

	// Mid-UTF-8 output boundary preserves InputModes.Known == true.
	e.Feed([]byte("\x1b[?1003h\x1b[?1006h\xe2\x94"))
	snap = snapshotDecode(t, e)
	if snap.InputModes == nil || !snap.InputModes.Known || snap.InputModes.Tracking != 1003 || snap.InputModes.Encoding != 1006 {
		t.Fatalf("mid-UTF-8 boundary broke InputModes: %+v", snap.InputModes)
	}
	e.Feed([]byte("\x80"))

	// RIS resets all input modes and clears AltScroll back to nil.
	e.Feed([]byte("\x1b[?1007h\x1b[?2004h\x1b[?1004h\x1bc"))
	snap = snapshotDecode(t, e)
	if snap.InputModes == nil || !snap.InputModes.Known || snap.InputModes.Tracking != 0 || snap.InputModes.Encoding != 0 || snap.InputModes.AltScroll != nil || snap.InputModes.BracketedPaste || snap.InputModes.FocusEvents || snap.InputModes.CursorKeys {
		t.Fatalf("RIS did not reset all InputModes: %+v", snap.InputModes)
	}

	// Unrelated XTRESTORE (?25r) does not affect InputModes.Known, whereas ?1007r does.
	e.Feed([]byte("\x1b[?25r"))
	snap = snapshotDecode(t, e)
	if snap.InputModes == nil || !snap.InputModes.Known {
		t.Fatalf("unrelated XTRESTORE (?25r) invalidated InputModes: %+v", snap.InputModes)
	}
	e.Feed([]byte("\x1b[?1007r"))
	snap = snapshotDecode(t, e)
	if snap.InputModes == nil || snap.InputModes.Known {
		t.Fatalf("input mode XTRESTORE (?1007r) stayed known: %+v", snap.InputModes)
	}

	// DecodeSnapshot neutralizes out-of-range tracking/encoding integers.
	for _, raw := range []string{
		`{"version":1,"cols":1,"rows":1,"lines":[],"inputModes":{"known":true,"tracking":9999,"encoding":1006}}`,
		`{"version":1,"cols":1,"rows":1,"lines":[],"inputModes":{"known":true,"tracking":1003,"encoding":1234}}`,
		`{"version":1,"cols":1,"rows":1,"lines":[],"inputModes":{"known":true,"tracking":-1}}`,
	} {
		decoded, err := DecodeSnapshot([]byte(raw))
		if err != nil {
			t.Fatalf("DecodeSnapshot(%s): %v", raw, err)
		}
		if decoded.InputModes == nil || decoded.InputModes.Known {
			t.Fatalf("out-of-range InputModes stayed Known for %s: %+v", raw, decoded.InputModes)
		}
	}

	// Non-mouse params in DECSET still count as one combined parameter list without allocating in Feed.
	m := NewMouseTracker(&MouseState{Known: true})
	p := []byte("plain \x1b[?2004h\x1b[?1007l\x1b[?1000;1002;1003;1006h")
	if n := testing.AllocsPerRun(100, func() { m.Feed(p) }); n != 0 {
		t.Fatalf("Feed allocated %.1f times per call", n)
	}
	if !strings.Contains(string(mustMarshalInputModes(t, m.snapshotInputModes())), `"tracking":1003`) {
		t.Fatalf("snapshotInputModes missed combined DECSET tracking: %+v", m.snapshotInputModes())
	}
}

func mustMarshalInputModes(t *testing.T, im *InputModes) []byte {
	t.Helper()
	b, err := json.Marshal(im)
	if err != nil {
		t.Fatalf("Marshal InputModes: %v", err)
	}
	return b
}
