package vt

// WAVE R8 / SLICE S5 -- THE ADVERSARIAL CORPUS ON THE REAL ASSEMBLED READ PATH.
//
// r8_corpus_test.go feeds hand-built Snaps straight into SnapText, which is the right way
// to test SnapText's own choke-point duty and the wrong way to test the pipeline: a Snap
// built by hand never went through the emulator, so it cannot catch a defect in how the
// PARSER hands bytes to the grid, and it cannot catch a sequence the parser ACTS ON rather
// than passes through. This file drives the composition the daemon actually runs --
//
//	raw hostile PTY bytes -> Emulator.Feed -> Emulator.Snapshot -> DecodeSnapshot -> SnapText
//
// -- which is exactly internal/daemon/terminalrender.go's renderEmulator, minus the loop's
// debouncing. Wave R5, R6 and R7 each lost a round to a defect only the real composition
// revealed; the sanitizer is the one seam in R8 where that costs a phone screen showing an
// attacker's sentence.
//
// The assertions are on the EXACT visible row, not on the absence of escape bytes. "No
// control byte survived" is true of a sanitizer that drops the row.

import (
	"strings"
	"testing"
)

// r8Render is the daemon's renderEmulator, inlined: it is the pipeline under test.
func r8Render(t *testing.T, cols, rows int, chunks ...[]byte) []string {
	t.Helper()
	e := NewEmulator(cols, rows)
	t.Cleanup(func() { _ = e.Close() })
	for _, c := range chunks {
		e.Feed(c)
	}
	b, err := e.Snapshot()
	if err != nil {
		t.Fatalf("Emulator.Snapshot: %v", err)
	}
	snap, err := DecodeSnapshot(b)
	if err != nil {
		t.Fatalf("DecodeSnapshot: %v", err)
	}
	return SnapText(snap)
}

// r8Row0 is the first rendered row with its right padding removed, which is what a reader
// of the phone screen sees.
func r8Row0(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimRight(lines[0], " ")
}

// TestR8RealPath_HostileSequencesRenderOnlyTheirVisibleText walks the sequence classes an
// attacker reaches for when the target is the CHROME AROUND the grid rather than the grid:
// a clipboard write, a window title, a masked hyperlink, a binary device-control payload, a
// printer-controller divert. Each asserts the exact visible row AND the absence of the
// payload the sequence was carrying, because a sanitizer that turns OSC 52 into its own
// literal text has moved the exfiltration onto the screen rather than removed it.
func TestR8RealPath_HostileSequencesRenderOnlyTheirVisibleText(t *testing.T) {
	cases := []struct {
		name    string
		spoof   string
		raw     string
		wantRow string
		forbid  []string
	}{
		{
			name:    "osc52_clipboard_write",
			spoof:   "writes the renderer's system clipboard; the payload must not reach the phone even as text",
			raw:     "\x1b]52;c;cGF5bG9hZA==\x07OK",
			wantRow: "OK",
			forbid:  []string{"52;c", "cGF5bG9hZA"},
		},
		{
			name:    "osc0_window_title",
			spoof:   "rewrites the host window title: attacker-controlled text asserted by the chrome, not the session",
			raw:     "\x1b]0;SWARM: owner approved this action\x07real",
			wantRow: "real",
			forbid:  []string{"owner approved"},
		},
		{
			name:    "osc8_hyperlink_mask",
			spoof:   "binds display text to an arbitrary URL",
			raw:     "\x1b]8;;https://evil.example\x1b\\Click\x1b]8;;\x1b\\",
			wantRow: "Click",
			forbid:  []string{"evil.example", "https"},
		},
		{
			name:    "dcs_sixel_payload",
			spoof:   "an unbounded binary channel inside a device-control string",
			raw:     "\x1bPq#0;2;0;0;0#0~~@@vv@@~~@@~~$\x1b\\after",
			wantRow: "after",
			forbid:  []string{"vv@@", "#0;2"},
		},
		{
			// MEASURED, AND A DISCLOSED DIVERGENCE RATHER THAN A LEAK. This emulator does not
			// implement the media-copy (printer) sequences, so it renders their text instead of
			// diverting it: the phone shows `divertedvisible` where an xterm shows `visible`.
			// That is a FIDELITY divergence between the owner's surface and the phone's, not an
			// injection -- the phone receives literal text and runs no parser. The expectation
			// is pinned to the measured behaviour so a future change that starts honouring
			// CSI 5i has to come past this test and say so.
			name:    "csi_printer_controller_is_not_honoured",
			spoof:   "CSI 5i diverts everything after it on a real terminal; here it must stay inert literal text",
			raw:     "\x1b[5idiverted\x1b[4ivisible",
			wantRow: "divertedvisible",
			forbid:  []string{"\x1b", "\u009b"},
		},
		{
			name:    "apc_kitty_graphics",
			spoof:   "APC is acted on by Kitty-family terminals and is unbounded",
			raw:     "\x1b_Ga=T,f=100;PAYLOAD\x1b\\shown",
			wantRow: "shown",
			forbid:  []string{"PAYLOAD", "a=T"},
		},
		{
			// MEASURED, AND THE SECOND DISCLOSED DIVERGENCE. This emulator does not treat the
			// 8-bit C1 introducers as sequence starts, so an OSC written with U+009D/U+009C
			// renders as literal text rather than being consumed. A terminal running with S8C1T
			// would consume it, so owner and phone see different rows -- but the phone sees
			// strictly MORE, inert, and the C1 bytes themselves are gone. Teaching the emulator
			// 8-bit C1 would ADD a second parser path for hostile bytes, which is the opposite of
			// what this wave is for, so the divergence is DISCLOSED, not closed.
			name:    "c1_eight_bit_osc_is_not_honoured",
			spoof:   "the same OSC 52 with no ESC anywhere in it: one byte, U+009D",
			raw:     "\u009d52;c;cGF5bG9hZA==\u009cOK",
			wantRow: "52;c;cGF5bG9hZA==OK",
			forbid:  []string{"\u009d", "\u009c", "\x1b"},
		},
		{
			name:    "truncated_osc_then_resync",
			spoof:   "a half sequence at a chunk boundary is where a stateful stripper resynchronises wrongly",
			raw:     "\x1b]52;c;AAAA",
			wantRow: "",
			forbid:  []string{"AAAA", "52;c"},
		},
		{
			name:    "csi_parameter_overflow",
			spoof:   "an absurd parameter run is the classic parser integer path",
			raw:     "\x1b[99999999999999999999999mtext",
			wantRow: "text",
			forbid:  []string{"99999"},
		},
		{
			name:    "sgr_then_reset_keeps_text",
			spoof:   "the benign control case: styling must not cost the text",
			raw:     "\x1b[1;31mALERT\x1b[0m",
			wantRow: "ALERT",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lines := r8Render(t, 40, 3, []byte(c.raw))
			if got := r8Row0(lines); got != c.wantRow {
				t.Errorf("ADR-017 T1/T4 (real path): row 0 = %q, want %q\n  raw   = %q\n  spoof = %s",
					got, c.wantRow, c.raw, c.spoof)
			}
			joined := strings.Join(lines, "\n")
			for _, f := range c.forbid {
				if strings.Contains(joined, f) {
					t.Errorf("ADR-017 T1: the payload %q of a %s reached the phone as TEXT. Turning a "+
						"control sequence into its own literal moves the exfiltration onto the screen; it "+
						"does not remove it. grid=%q", f, c.name, joined)
				}
			}
		})
	}
}

// TestR8RealPath_SplitSequenceCannotSmugglePayload feeds one OSC 52 across three Feed calls,
// splitting it inside the introducer, inside the base64 body and just before the terminator.
// A stripper that resynchronises per-chunk emits the tail of the payload as text.
func TestR8RealPath_SplitSequenceCannotSmugglePayload(t *testing.T) {
	lines := r8Render(t, 40, 3,
		[]byte("\x1b]5"),
		[]byte("2;c;cGF5bG"),
		[]byte("9hZA=="),
		[]byte("\x07VISIBLE"),
	)
	if got := r8Row0(lines); got != "VISIBLE" {
		t.Errorf("a sequence split across chunk boundaries must render only its visible tail: row 0 = %q, want %q", got, "VISIBLE")
	}
	if joined := strings.Join(lines, "\n"); strings.Contains(joined, "cGF5bG") || strings.Contains(joined, "9hZA") {
		t.Errorf("ADR-017 T1: a chunk-split OSC 52 payload leaked as text: %q", joined)
	}
}

// TestR8RealPath_UnicodeSpoofsDoNotSurviveTheRealPipeline is the Unicode half on the real
// path. These runes are PRINTABLE to the parser: they land in real grid cells and are
// carried through the snapshot as legitimate run text, so the only place they can be
// stopped is the phone-facing flattener.
func TestR8RealPath_UnicodeSpoofsDoNotSurviveTheRealPipeline(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantRow string
	}{
		{"rlo_override", "safe\u202etxet.exe", "safetxet.exe"},
		{"zero_width_splice", "ad\u200bmin", "admin"},
		{"soft_hyphen", "pay\u00adpal.com", "paypal.com"},
		{"word_joiner", "a\u2060b", "ab"},
		{"tag_block_sentence", "safe\U000E0001\U000E0074\U000E0065\U000E0078\U000E0074\U000E007Ftext", "safetext"},
		// MEASURED: the emulator gives U+2028/U+2029 no cell at all, so the real path drops
		// them and the row is ten columns, not eleven. That measurement is why the corpus puts
		// both in the DROP class: a space here would add a column nothing counted.
		{"line_separator", "above below", "abovebelow"},
		{"paragraph_separator", "above below", "abovebelow"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := r8Row0(r8Render(t, 40, 3, []byte(c.raw))); got != c.wantRow {
				t.Errorf("ADR-017 T4-c (real path): row 0 = %q, want %q. These runes are PRINTABLE to "+
					"the parser and reach real grid cells, so the flattener is the only place they can be "+
					"stopped.", got, c.wantRow)
			}
		})
	}
}

// TestR8RealPath_TheWindowTitleNeverReachesThePhone is a property the corpus cannot state,
// because it is about what the projection OMITS.
//
// OSC 0/2 is attacker-controlled and the emulator RECORDS it (emulator.go's Title callback);
// SnapText projects Lines only. That omission is load-bearing and undocumented by any test:
// the moment a fallback screen wants a header, the title is the nearest string to hand, and
// it is the one string on the snapshot the session's own output fully controls.
func TestR8RealPath_TheWindowTitleNeverReachesThePhone(t *testing.T) {
	e := NewEmulator(40, 3)
	t.Cleanup(func() { _ = e.Close() })
	e.Feed([]byte("\x1b]0;APPROVED BY OWNER\x07body"))
	b, err := e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	snap, err := DecodeSnapshot(b)
	if err != nil {
		t.Fatalf("DecodeSnapshot: %v", err)
	}
	if snap.Title == "" {
		t.Fatalf("fixture no longer sets a title, so this test proves nothing")
	}
	for i, line := range SnapText(snap) {
		if strings.Contains(line, "APPROVED") {
			t.Errorf("ADR-017 T1: the OSC-0 window title reached the phone-facing projection on row %d "+
				"(%q). The title is fully session-controlled text; a fallback header built from it lets "+
				"the session write the chrome that frames it.", i, line)
		}
	}
}

// TestR8RealPath_DeviceQueriesGenerateNoReplyOnTheRenderPath pins the one place where a
// READ path can become a WRITE path.
//
// CSI 6n (DSR), DA and the mode/color reports make a terminal ANSWER on its input channel.
// The emulator implements those answers and routes them to a swappable sink that DISCARDS
// until SetReplyWriter is called (emulator.go:134-153). internal/daemon's render loop never
// calls SetReplyWriter -- which is what makes "the fallback is a read" true at the byte
// level, and is asserted nowhere. If a future change wires the reply writer to the session
// tap "so queries work", a watching phone becomes a path by which session output writes
// session input.
func TestR8RealPath_DeviceQueriesGenerateNoReplyOnTheRenderPath(t *testing.T) {
	e := NewEmulator(40, 3)
	t.Cleanup(func() { _ = e.Close() })
	e.Feed([]byte("\x1b[6n\x1b[c\x1b[>0c\x1b[?6n"))

	e.reply.mu.Lock()
	w := e.reply.w
	e.reply.mu.Unlock()
	if w != nil {
		t.Fatalf("ADR-017 T4 (watch grants no input authority): a render-path emulator has a reply "+
			"writer installed (%T). Device queries are session OUTPUT that becomes session INPUT; on a "+
			"watched fallback session that is a write path opened by a read.", w)
	}
}

// TestR8RealPath_TheWireSuppliedInitialSnapshotIsTheLiveHostilePath is why the corpus in
// r8_corpus_test.go is about a LIVE path and not only about defence in depth.
//
// internal/daemon/terminalrender.go:140-147 (`renderInitial`) does NOT build its Snap from
// its own emulator. It decodes `stream.Snapshot()` -- bytes produced by ANOTHER process, the
// shim, and carried over a pipe -- and pushes `vt.SnapText(snap)` straight to the phone
// before the emulator is even seeded. `DecodeSnapshot` validates the version number and
// nothing else: it performs no sanitization of run text at all (emulator.go:595-607).
//
// So every rune class the corpus asserts on a hand-built Snap has a live carrier: a Snap
// that reaches the daemon over the wire, from a producer whose sanitization the daemon
// cannot verify, whose run text is flattened by SnapText and appended to a phone. SnapText's
// own doc comment already claims exactly this duty -- "stripping directly rather than
// trusting the producer-side N-6 filter, so hostile bytes that reach a Snap BY ANY PATH
// still cannot smuggle an escape sequence or a Trojan-Source visual spoof to the viewer"
// (render.go:140-143). This test pins the path that sentence is about.
func TestR8RealPath_TheWireSuppliedInitialSnapshotIsTheLiveHostilePath(t *testing.T) {
	// A snapshot as it arrives on the wire: valid JSON, correct version, hostile run text.
	// Nothing between here and the phone but DecodeSnapshot and SnapText.
	wire := []byte(`{"version":1,"cols":40,"rows":1,"cursor_visible":true,"lines":[{"runs":[` +
		`{"text":"deploy\u2028owner approved: yes","width":26}]}]}`)

	snap, err := DecodeSnapshot(wire)
	if err != nil {
		t.Fatalf("DecodeSnapshot rejected a well-formed wire snapshot: %v", err)
	}
	lines := SnapText(snap)
	if len(lines) != 1 {
		t.Fatalf("SnapText returned %d lines, want 1", len(lines))
	}
	if strings.ContainsRune(lines[0], 0x2028) {
		t.Errorf("ADR-017 T4-c: a U+2028 supplied by the PRODUCER (not by this emulator) reached the "+
			"phone-facing projection: %q. renderInitial pushes SnapText over a snapshot decoded from "+
			"another process's bytes, so the producer-side filter is not in this path at all and "+
			"SnapText is the only sanitizer between that producer and the handset.", lines[0])
	}
	if lines[0] != "deployowner approved: yes" {
		t.Errorf("wire-supplied run text sanitized to %q, want %q", lines[0], "deployowner approved: yes")
	}
}
