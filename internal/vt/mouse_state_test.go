package vt

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestMouseSnapshotKnownOffAndExistingModes(t *testing.T) {
	e := NewEmulator(10, 4)
	defer func() { _ = e.Close() }()
	for _, modes := range []string{"", "\x1b[?1002;1006h"} {
		e.Feed([]byte(modes))
		raw, err := e.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		var snap map[string]json.RawMessage
		if err := json.Unmarshal(raw, &snap); err != nil {
			t.Fatal(err)
		}
		var mouse struct {
			Tracking uint16 `json:"tracking"`
			Encoding uint16 `json:"encoding"`
			Known    bool   `json:"known"`
		}
		if err := json.Unmarshal(snap["mouse"], &mouse); err != nil {
			t.Fatalf("snapshot omits authoritative mouse state: %v", err)
		}
		wantTracking, wantEncoding := uint16(0), uint16(0)
		if modes != "" {
			wantTracking, wantEncoding = MouseTrackButton, MouseEncodeSGR
		}
		if !mouse.Known || mouse.Tracking != wantTracking || mouse.Encoding != wantEncoding {
			t.Fatalf("wrong initial mouse state: %+v", mouse)
		}
	}
}

func TestMouseTrackerEachModeHasItsOwnBit(t *testing.T) {
	for _, tc := range []struct {
		mode               int
		tracking, encoding uint16
	}{
		{9, MouseTrackX10, 0}, {1000, MouseTrackNormal, 0}, {1001, MouseTrackHighlight, 0},
		{1002, MouseTrackButton, 0}, {1003, MouseTrackAny, 0}, {1005, 0, MouseEncodeUTF8},
		{1006, 0, MouseEncodeSGR}, {1015, 0, MouseEncodeURXVT}, {1016, 0, MouseEncodePixel},
	} {
		m := NewMouseTracker(&MouseState{Known: true})
		m.Feed([]byte("\x1b[?" + strconv.Itoa(tc.mode) + "h"))
		if got := m.State(); got != (MouseState{Tracking: tc.tracking, Encoding: tc.encoding, Known: true}) {
			t.Fatalf("mode=%d got %+v", tc.mode, got)
		}
		m.Feed([]byte("\x1b[?" + strconv.Itoa(tc.mode) + "l"))
		if got := m.State(); got != (MouseState{Known: true}) {
			t.Fatalf("mode=%d reset got %+v", tc.mode, got)
		}
	}
}

func TestMouseTrackerModesSplitsAndIntermediateChanges(t *testing.T) {
	stream := "\x1b[?9;1000;1001;1002;1003;1005;1006;1015;1016h"
	want := MouseState{Tracking: 31, Encoding: 15}
	for split := 0; split <= len(stream); split++ {
		m := NewMouseTracker(&MouseState{Known: true})
		m.Feed([]byte(stream[:split]))
		m.Feed([]byte(stream[split:]))
		if m.State() != want {
			t.Fatalf("split=%d got %+v want %+v", split, m.State(), want)
		}
		if !m.Feed([]byte("\x1b[?9;1000;1001;1002;1003;1005;1006;1015;1016l")) || m.State() != (MouseState{}) {
			t.Fatalf("reset masks: %+v", m.State())
		}
		if !m.Feed([]byte("\x1b[?1006h\x1b[?1006l")) || m.State() != (MouseState{}) {
			t.Fatal("net-zero frame lost intermediate state change")
		}
	}
}

func TestMouseTrackerPayloadsAreNotModeCommands(t *testing.T) {
	for _, intro := range []string{"\x1b]", "\x1bP", "\x1b_", "\x1b^", "\x1bX", "\x9d", "\x90", "\x9f", "\x9e", "\x98"} {
		stream := intro + "payload [?1003;1016h [c [?1006r 界🐙" + strings.Repeat("x", 1000) + "\x1b\\"
		for split := 0; split <= len(stream); split++ {
			m := NewMouseTracker(&MouseState{Tracking: MouseTrackNormal, Encoding: MouseEncodeSGR, Known: true})
			m.Feed([]byte(stream[:split]))
			m.Feed([]byte(stream[split:]))
			if got := m.State(); got != (MouseState{Tracking: MouseTrackNormal, Encoding: MouseEncodeSGR, Known: true}) {
				t.Fatalf("intro=%q split=%d parsed payload: %+v", intro, split, got)
			}
			m.Feed([]byte("\x1b[?1003h"))
			if m.State().Tracking != MouseTrackNormal|MouseTrackAny {
				t.Fatal("string terminator did not resume mode observation")
			}
		}
	}
}

func TestMouseTrackerAmbiguousStringEscapesFailClosed(t *testing.T) {
	for _, seq := range []string{
		"\x1b]title\x1b[?1006h\x1b\\",
		"\x1bPtmux;\x1b\x1b[?1006h\x1b\\",
		"\x1b]title\u009c\x1b[?1006h\x1b\\",
	} {
		m := NewMouseTracker(&MouseState{Known: true})
		for i := 0; i < len(seq); i++ {
			m.Feed([]byte(seq[i : i+1]))
		}
		if m.State().Known {
			t.Fatalf("ambiguous string escape stayed authoritative: %q %+v", seq, m.State())
		}
	}
}

func TestMouseTrackerUnknownRestoreQueriesAndReset(t *testing.T) {
	m := NewMouseTracker(nil)
	m.Feed([]byte("\x1b[?1000;1006h"))
	if m.State().Known {
		t.Fatal("legacy unknown snapshot became invented known state")
	}
	m.Feed([]byte("\x1bc\x1b[?1000;1006h"))
	want := MouseState{Tracking: MouseTrackNormal, Encoding: MouseEncodeSGR, Known: true}
	if m.State() != want {
		t.Fatalf("RIS did not establish known baseline: %+v", m.State())
	}
	for _, harmless := range []string{"\x1b[?1000;1006$p", "\x1b[?1000;1006s", "\x1b[?25h", "\x1b[?1049h", "\x1b[?25r"} {
		if m.Feed([]byte(harmless)) || m.State() != want {
			t.Fatalf("nonmutating mode command changed mouse: %q %+v", harmless, m.State())
		}
	}
	for _, unsafe := range []string{"\x1b[?1006r", "\x1b[?r", "\x1b[!p"} {
		m := NewMouseTracker(&want)
		if !m.Feed([]byte(unsafe)) || m.State().Known {
			t.Fatalf("ambiguous restore/reset stayed known: %q %+v", unsafe, m.State())
		}
	}
}

func TestMouseTrackerMalformedModeCommandsFailClosed(t *testing.T) {
	for _, seq := range []string{
		"\x1b[?" + strings.Repeat("0", 10000) + "1006h",
		"\x1b[?" + strings.Repeat("25;", 100) + "1006h",
		"\x1b[?1006:1h", "\x1b[?10\x0006h",
	} {
		m := NewMouseTracker(&MouseState{Known: true})
		for i := range seq {
			m.Feed([]byte(seq[i : i+1]))
		}
		if m.State().Known {
			t.Fatalf("malformed mode remained authoritative: %.50q %+v", seq, m.State())
		}
	}
}

func TestMouseSnapshotCompatibilityAndRawTrackerAgreement(t *testing.T) {
	legacy, err := DecodeSnapshot([]byte(`{"version":1,"cols":1,"rows":1,"lines":[]}`))
	if err != nil || legacy.Mouse != nil {
		t.Fatalf("legacy snapshot lost compatibility: %+v %v", legacy, err)
	}
	unsafe, err := DecodeSnapshot([]byte(`{"version":1,"mouse":{"tracking":512,"encoding":2,"known":true}}`))
	if err != nil || unsafe.Mouse == nil || unsafe.Mouse.Known {
		t.Fatalf("unknown mask bits stayed authoritative: %+v %v", unsafe, err)
	}
	for _, stream := range []string{"\x1b[?1002;1006h", "\x1b[?10\x0006h", "\x1b[?1006s\x1b[?1006r", "\x1b]xx\x1b[?1003h\a\x1b[?1000h"} {
		e := NewEmulator(4, 3)
		m := NewMouseTracker(&MouseState{Known: true})
		for i := range stream {
			p := []byte(stream[i : i+1])
			e.Feed(p)
			m.Feed(p)
		}
		raw, err := e.Snapshot()
		_ = e.Close()
		if err != nil {
			t.Fatal(err)
		}
		snap, err := DecodeSnapshot(raw)
		if err != nil || snap.Mouse == nil || *snap.Mouse != m.State() {
			t.Fatalf("raw/snapshot tracker disagreement: got %+v want %+v err=%v", snap.Mouse, m.State(), err)
		}
	}
}

func TestMouseTrackerFeedAllocationsBounded(t *testing.T) {
	m := NewMouseTracker(&MouseState{Known: true})
	p := []byte("plain \x1b[?1002;1006h\x1b[?1002;1006l\x1b]ignored\x1b[?1000h\a")
	if n := testing.AllocsPerRun(100, func() { m.Feed(p) }); n != 0 {
		t.Fatalf("mode observation allocated %.1f times per frame", n)
	}
}

func TestMouseSnapshotIncompleteOutputBoundaryIsUnknown(t *testing.T) {
	for _, parts := range [][2]string{{"\x1b[?100", "6h"}, {"\x1b]title", "\x1b\\"}, {"\x1bPpayload", "\x1b\\"}, {"\xe7", "\x95\x8c"}} {
		e := NewEmulator(10, 4)
		t.Cleanup(func() { _ = e.Close() })
		e.Feed([]byte(parts[0]))
		snap := snapshotDecode(t, e)
		if snap.Mouse == nil || snap.Mouse.Known {
			t.Fatalf("snapshot lost pending parser context but claimed known state: %q %+v", parts[0], snap.Mouse)
		}
		e.Feed([]byte(parts[1]))
		snap = snapshotDecode(t, e)
		_ = e.Close()
		if snap.Mouse == nil || !snap.Mouse.Known {
			t.Fatalf("completed boundary stayed unknown: %q %+v", parts, snap.Mouse)
		}
	}
}

func TestMouseTrackerAmbiguousHistoryCannotRegainAuthorityByClearingBits(t *testing.T) {
	for _, seq := range []string{"\x1b[?1000h\x1b[?1003h\x1b[?1000l", "\x1b[?1006h\x1b[?1005h\x1b[?1005l"} {
		m := NewMouseTracker(&MouseState{Known: true})
		m.Feed([]byte(seq))
		if m.State().Known {
			t.Fatalf("ambiguous mode history regained authority: %q %+v", seq, m.State())
		}
		m.Feed([]byte("\x1bc"))
		if m.State() != (MouseState{Known: true}) {
			t.Fatalf("RIS failed to reset ambiguous history: %+v", m.State())
		}
	}
	seed := MouseState{Tracking: MouseTrackNormal | MouseTrackAny, Encoding: MouseEncodeSGR, Known: true}
	if NewMouseTracker(&seed).State().Known {
		t.Fatal("ambiguous snapshot seed was authoritative")
	}
	raw, err := json.Marshal(Snap{Version: 1, Mouse: &seed})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := DecodeSnapshot(raw)
	if err != nil || snap.Mouse == nil || snap.Mouse.Known || snap.Mouse.Tracking != seed.Tracking {
		t.Fatalf("ambiguous metadata was not retained as unknown: %+v err=%v", snap, err)
	}
}
