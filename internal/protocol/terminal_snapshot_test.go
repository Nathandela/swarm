package protocol

// FAILING-FIRST protocol test for the A7 renderer slice B terminal-snapshot wire
// type. terminal_subscribe requests the server-rendered terminal snapshot stream
// for a session; terminal_snapshot carries one sanitized, server-rendered snapshot
// (session id + plain-text lines + grid dims) to the phone — mirroring the
// journal_subscribe/journal_event pair. This test pins the wire round-trip; the
// GG-7 bidi drift check (protocolmd_bidi_test.go) pins that protocol.md documents
// the new field set.

import "testing"

// TestTerminalSnapshot_RoundTrip: a Control carrying a server-rendered terminal
// snapshot round-trips through the JSON codec with its TerminalSnapshot payload
// (session/lines/cols/rows) intact.
func TestTerminalSnapshot_RoundTrip(t *testing.T) {
	in := Control{
		Op: OpTerminalSnapshot,
		Terminal: &TerminalSnapshot{
			Session: "s1",
			Lines:   []string{"hello", "world"},
			Cols:    80,
			Rows:    24,
		},
	}
	b, err := EncodeControl(in)
	if err != nil {
		t.Fatalf("EncodeControl: %v", err)
	}
	got, err := DecodeControl(b)
	if err != nil {
		t.Fatalf("DecodeControl: %v", err)
	}
	if got.Op != OpTerminalSnapshot {
		t.Fatalf("op = %q; want %q", got.Op, OpTerminalSnapshot)
	}
	if got.Terminal == nil {
		t.Fatalf("Terminal payload dropped by the codec (nil after round-trip)")
	}
	if got.Terminal.Session != "s1" {
		t.Errorf("Terminal.Session = %q; want %q", got.Terminal.Session, "s1")
	}
	if got.Terminal.Cols != 80 || got.Terminal.Rows != 24 {
		t.Errorf("Terminal cols/rows = %d/%d; want 80/24", got.Terminal.Cols, got.Terminal.Rows)
	}
	want := []string{"hello", "world"}
	if len(got.Terminal.Lines) != len(want) {
		t.Fatalf("Terminal.Lines len = %d; want %d", len(got.Terminal.Lines), len(want))
	}
	for i, line := range got.Terminal.Lines {
		if line != want[i] {
			t.Errorf("Terminal.Lines[%d] = %q; want %q", i, line, want[i])
		}
	}
}
