package vt

import (
	"strings"
	"testing"
	"time"
)

// vt-fuzz (Wave R0): pins the fix for the unbounded CHT/CBT tab-stop hang
// documented in docs/verification/r0-red/vt-fuzz-red.txt. x/vt's CSI 'I'
// (CHT) and 'Z' (CBT) handlers loop over their raw, untrusted numeric
// parameter with no bound tied to the grid, so a single large parameter can
// spin for an attacker-chosen amount of wall time while FeedChecked holds
// e.mu. clampCsiParams defends against this by capping consecutive CSI
// parameter digits before the bytes ever reach the upstream parser.

// TestFeed_LongCsiParamStaysBounded asserts that a Feed call carrying a CSI
// CHT parameter far larger than any real terminal would use returns well
// within a small deadline, one byte at a time (the worst case: every byte
// lands at split index 0, so nothing but the clamp can bound it — see
// TestClampCsiParams_DropAtIndexZero for the same edge case isolated from
// Feed/the parser).
func TestFeed_LongCsiParamStaysBounded(t *testing.T) {
	// 20 digits: interpreted whole by the unclamped upstream parser this is a
	// parameter far beyond any tab-stop count, i.e. an effectively unbounded
	// loop (see the red evidence's timing table: an 11-digit parameter alone
	// already ran 9-15s).
	input := []byte("\x1b[" + strings.Repeat("9", 20) + "I")

	e := NewEmulator(80, 24)
	t.Cleanup(func() { _ = e.Close() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, b := range input {
			e.Feed([]byte{b})
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Feed of a %d-digit CSI CHT parameter, one byte at a time, did not return within 2s", len(input)-3)
	}
}

// TestClampCsiParams_DropAtIndexZero pins the reviewed blocker: when the
// first dropped byte of a Feed call is at index 0 (the case for any
// one-byte-at-a-time Feed, e.g. a PTY read loop flushing a child's output
// byte by byte), clampCsiParams must still drop it. A prior version used
// out != nil as its "a drop happened" sentinel, but append(nil, p[:0]...)
// itself evaluates to nil, so a drop at index 0 was silently invisible and
// the whole unclamped input was forwarded.
func TestClampCsiParams_DropAtIndexZero(t *testing.T) {
	e := NewEmulator(80, 24)
	t.Cleanup(func() { _ = e.Close() })

	// Feed the first five parameter digits (exactly at the cap) so the guard's
	// digit count already sits at the boundary going into the next call.
	if got := e.clampCsiParams([]byte("\x1b[12345")); string(got) != "\x1b[12345" {
		t.Fatalf("setup call unexpectedly modified: %q", got)
	}

	// The 6th digit, one over cap, arrives alone as its own call -- byte
	// index 0 -- exactly what a PTY read loop flushing a child's output one
	// byte at a time produces. It must still be dropped.
	if got := e.clampCsiParams([]byte("6")); string(got) != "" {
		t.Fatalf("clampCsiParams(%q) with the guard already at cap = %q, want empty (over-cap digit at index 0 must be dropped)", "6", got)
	}
}

// TestClampCsiParams_Table exercises clampCsiParams directly across the
// cases the reviewed bug and its neighbors depend on: dropping at a
// mid-slice index still works; a fresh parameter under the cap is
// untouched; and splitting an over-cap parameter across two calls (as
// FuzzFeedSplitConsistency does) clamps the same regardless of where the
// split falls, because guard state persists across calls.
func TestClampCsiParams_Table(t *testing.T) {
	cases := []struct {
		name  string
		feeds []string
		want  string
	}{
		{
			name:  "under cap forwarded unchanged",
			feeds: []string{"\x1b[12345I"},
			want:  "\x1b[12345I",
		},
		{
			name:  "over cap drops trailing digits, keeps final byte",
			feeds: []string{"\x1b[1234567I"},
			want:  "\x1b[12345I",
		},
		{
			name:  "drop at index 0 of the call",
			feeds: []string{"\x1b[123456", "7I"},
			want:  "\x1b[12345I",
		},
		{
			name:  "drop at a mid-slice index",
			feeds: []string{"\x1b[123", "4567I"},
			want:  "\x1b[12345I",
		},
		{
			name:  "split entirely inside the cap forwards both parts unchanged",
			feeds: []string{"\x1b[12", "345I"},
			want:  "\x1b[12345I",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEmulator(80, 24)
			t.Cleanup(func() { _ = e.Close() })

			var got strings.Builder
			for _, part := range tc.feeds {
				got.Write(e.clampCsiParams([]byte(part)))
			}
			if got.String() != tc.want {
				t.Errorf("feeds %q clamped to %q, want %q", tc.feeds, got.String(), tc.want)
			}
		})
	}
}
