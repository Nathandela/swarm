// Package submitframe is the shared submit-boundary framing rule (bead
// agents-tracker-abyz, ADR-010 Amendment 1 A2, Phase 0). FAILING-FIRST (TDD RED,
// GG-5): these tests pin the frozen production API before it exists, so until a
// submitframe.go lands the only errors from this package must be "undefined" for
// the new symbols below.
//
// FROZEN CONTRACT:
//
//	const Gap = 150 * time.Millisecond
//
//	func IsSubmit(b byte) bool
//	func IsSubmitOnly(buf []byte) bool
//	func FrameLen(buf []byte, max int) int
//
// This package has NO imports beyond stdlib: it is a leaf, extracted so both
// internal/phonecore (the coalescer's frameLen/isSubmit) and the new daemon-side
// send_input writer can depend on ONE copy of the rule instead of two that could
// drift apart.
//
// THE RULE, ported from internal/phonecore/coalesce.go's frameLen/isSubmit and
// internal/remotegw/lease.go's isSubmitOnly/submitGap (both call sites now
// delegate here -- phonecore's frameLen wraps FrameLen, remotegw uses
// IsSubmitOnly and Gap directly -- so the daemon-side send_input writer becomes
// the third caller of ONE copy of the rule):
//
//   - A PTY write must never mix text and a submit byte (CR/LF) in one frame.
//     Claude Code's TUI reads text+CR arriving in a single write as a multi-line
//     PASTE: the CR is inserted into the input box as a literal newline instead
//     of submitting, the prompt sits there unsent, and the next turn's text is
//     appended to the same unsent draft. Nothing reports it on either side
//     (bead agents-tracker-r3p, spike-SA finding #1).
//   - FrameLen returns the length of a MAXIMAL RUN of submit bytes or a maximal
//     run of non-submit bytes -- never a mixture -- capped at the caller's max.
//     A run, not one byte per frame: a held Enter is a ~30 Hz stream of submits,
//     and one frame per byte would drain slower than it arrives.
//   - Gap is the minimum spacing a caller must sleep before a submit-only frame
//     that closely follows a text frame, so a store-and-forward hop (or any hop
//     whose batching compresses two separately-emitted frames back together)
//     does not recreate the mixed write at the PTY. It is spike S-A's measured
//     value: the harness that made a real Claude Code submit reliably wrote the
//     text, slept 150ms, then wrote the CR.
//
// This file contains NO implementation.
package submitframe

import (
	"bytes"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Gap
// ---------------------------------------------------------------------------

// TestGap_Is150Milliseconds pins the constant spike-SA measured
// (docs/verification/spike-SA.md finding #1), ported verbatim from
// remotegw.submitGap so a caller cannot silently retune it.
func TestGap_Is150Milliseconds(t *testing.T) {
	if Gap != 150*time.Millisecond {
		t.Errorf("Gap = %v, want 150ms (spike-SA finding #1: text, sleep 150ms, then the CR)", Gap)
	}
}

// ---------------------------------------------------------------------------
// IsSubmit
// ---------------------------------------------------------------------------

// TestIsSubmit pins which single bytes count as "run what I typed": CR and LF,
// ported from phonecore.isSubmit, and nothing else -- in particular not ESC,
// not a NUL, not an ordinary printable byte.
func TestIsSubmit(t *testing.T) {
	cases := []struct {
		name string
		b    byte
		want bool
	}{
		{"carriage_return", '\r', true},
		{"line_feed", '\n', true},
		{"letter", 'a', false},
		{"digit", '5', false},
		{"space", ' ', false},
		{"tab", '\t', false},
		{"escape", 0x1b, false},
		{"nul", 0x00, false},
		{"high_bit", 0xff, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsSubmit(c.b); got != c.want {
				t.Errorf("IsSubmit(%q) = %v, want %v", c.b, got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// IsSubmitOnly
// ---------------------------------------------------------------------------

// TestIsSubmitOnly pins the whole-buffer classification WriteDataIn's gap
// decision is built on (ported from remotegw.isSubmitOnly): empty is never
// submit-only (there is nothing to gate), a buffer of nothing but CR/LF is, and
// any admixture of ordinary bytes -- leading, trailing, or the whole buffer --
// is not.
func TestIsSubmitOnly(t *testing.T) {
	cases := []struct {
		name string
		buf  []byte
		want bool
	}{
		{"nil", nil, false},
		{"empty", []byte{}, false},
		{"single_cr", []byte("\r"), true},
		{"single_lf", []byte("\n"), true},
		{"cr_lf_run", []byte("\r\n\r\n"), true},
		{"pure_text", []byte("hello"), false},
		{"text_then_submit", []byte("hello\r"), false},
		{"submit_then_text", []byte("\rhello"), false},
		{"submit_in_middle", []byte("he\rllo"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsSubmitOnly(c.buf); got != c.want {
				t.Errorf("IsSubmitOnly(%q) = %v, want %v", c.buf, got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FrameLen -- maximal-run splitting
// ---------------------------------------------------------------------------

// TestFrameLen_MaximalRun ports the behavioral vectors from
// internal/phonecore/r3p_submit_boundary_test.go's frame-boundary assertions:
// FrameLen(buf, max) is the length of the leading maximal run of one class
// (submit or non-submit), capped at max, never crossing into the other class.
func TestFrameLen_MaximalRun(t *testing.T) {
	cases := []struct {
		name string
		buf  []byte
		max  int
		want int
	}{
		// The exact shape PhoneSurface's "Send line" produces in one call
		// (TestR3PCoalescer_SubmitNeverSharesAFrameWithTheTextItSubmits): the
		// text run stops dead at the CR, it does not absorb it.
		{"text_stops_before_submit", []byte("git status\r"), 4096, len("git status")},
		// What is left after that first run is sliced off: the CR alone.
		{"submit_alone_after_text_consumed", []byte("\r"), 4096, 1},
		// A run of nothing but CR is one frame regardless of length, as long as
		// it fits under the cap -- the held-Enter case phonecore's
		// TestR3PCoalescer_HeldEnterStaysUnderTheRelayQuota exists for.
		{"pure_cr_run", []byte("\r\r\r\r\r"), 4096, 5},
		// LF and CR are BOTH submit bytes (IsSubmit), so a run mixing them is
		// still one homogeneous submit run, not two frames.
		{"cr_lf_mixed_submit_run", []byte("\r\n\r\n"), 4096, 4},
		// A single byte of either class is a run of length 1.
		{"single_text_byte", []byte("a"), 4096, 1},
		{"single_submit_byte", []byte("\r"), 4096, 1},
		// The boundary is the FIRST transition, however early it falls.
		{"one_text_byte_then_submit", []byte("a\r"), 4096, 1},
		// The cap applies to a text run: an oversize burst is split at max and
		// nowhere else (phonecore's oversize-unit note beside MaxInputPayload).
		{"capped_text_run", bytes.Repeat([]byte("a"), 5000), 4096, 4096},
		// The cap applies equally to a submit run.
		{"capped_submit_run", bytes.Repeat([]byte("\r"), 10), 3, 3},
		// A run that exactly fills the cap is not truncated short of it.
		{"run_exactly_at_cap", []byte("ab"), 2, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FrameLen(c.buf, c.max); got != c.want {
				t.Errorf("FrameLen(%q, %d) = %d, want %d", c.buf, c.max, got, c.want)
			}
		})
	}
}

// TestFrameLen_WalkConsumesWholeBufferInMaximalRuns drives FrameLen the way a
// caller actually does -- ADR-010 Amendment 1 A2's daemon-side send_input
// writer, mirroring phonecore's own drain loop (coalesce.go's drain): call
// FrameLen on what remains, slice it off, repeat while bytes remain. It covers
// the "empty-adjacent" case the vector table above does not: a run that lands
// EXACTLY at the end of the buffer must hand back a zero-length remainder
// without the loop calling FrameLen on it, and no run may ever come back
// length 0 (that would spin the caller forever).
func TestFrameLen_WalkConsumesWholeBufferInMaximalRuns(t *testing.T) {
	// Mixed runs, including two submit runs back to back with a text run
	// between them, and a submit run that itself mixes CR and LF.
	buf := []byte("ab\r\ncd\r\r\nefgh")
	want := [][]byte{
		[]byte("ab"),
		[]byte("\r\n"),
		[]byte("cd"),
		[]byte("\r\r\n"),
		[]byte("efgh"),
	}

	const max = 4096
	remaining := buf
	var runs [][]byte
	for len(remaining) > 0 {
		n := FrameLen(remaining, max)
		if n <= 0 {
			t.Fatalf("FrameLen returned %d on a non-empty buffer %q -- a non-positive run length loops the caller forever", n, remaining)
		}
		if n > len(remaining) {
			t.Fatalf("FrameLen returned %d, longer than the %d bytes remaining", n, len(remaining))
		}
		runs = append(runs, append([]byte(nil), remaining[:n]...))
		remaining = remaining[n:]
		if len(runs) > len(buf) {
			t.Fatalf("walk did not terminate after %d runs over a %d-byte buffer", len(runs), len(buf))
		}
	}

	if len(runs) != len(want) {
		t.Fatalf("walk produced %d runs %q, want %d runs %q", len(runs), runs, len(want), want)
	}
	for i, run := range runs {
		if !bytes.Equal(run, want[i]) {
			t.Errorf("run %d = %q, want %q", i, run, want[i])
		}
		// Every run is homogeneous: every byte in it agrees with the first on
		// submit-vs-not. This is the property FrameLen exists to guarantee.
		submit := IsSubmit(run[0])
		for j, b := range run {
			if IsSubmit(b) != submit {
				t.Errorf("run %d = %q is not homogeneous: byte %d (%q) disagrees with the run's own first byte", i, run, j, b)
			}
		}
	}

	// Nothing was lost or reordered: reassembling the runs reproduces buf.
	var got []byte
	for _, run := range runs {
		got = append(got, run...)
	}
	if !bytes.Equal(got, buf) {
		t.Fatalf("reassembled runs = %q, want %q -- the walk lost or reordered bytes", got, buf)
	}
}

// TestFrameLen_RunsAgreeWithIsSubmitOnly ties FrameLen's output to
// IsSubmitOnly, since a daemon-side writer (ADR-010 A2) uses IsSubmitOnly on
// exactly the runs FrameLen hands it to decide whether Gap applies. A run
// FrameLen calls "submit" must be one IsSubmitOnly calls true, and vice versa,
// or the two halves of the extracted rule disagree with each other.
func TestFrameLen_RunsAgreeWithIsSubmitOnly(t *testing.T) {
	buf := []byte("ab\r\ncd\r\r\nefgh")
	const max = 4096
	remaining := buf
	for len(remaining) > 0 {
		n := FrameLen(remaining, max)
		run := remaining[:n]
		wantSubmit := IsSubmit(run[0])
		if got := IsSubmitOnly(run); got != wantSubmit {
			t.Errorf("IsSubmitOnly(%q) = %v, want %v to match the run's own first byte", run, got, wantSubmit)
		}
		remaining = remaining[n:]
	}
}
