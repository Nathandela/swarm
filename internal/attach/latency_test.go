package attach

import (
	"sort"
	"testing"
	"time"
)

// E8.5 / N-2 — attach passthrough adds < 10 ms local latency at p95, measured as
// a keystroke echo round trip over >= 1000 samples.
//
// METHOD (recorded per N-2): the fake session echoes every Input byte straight
// back onto Frames, so the measured interval is exactly the passthrough's own
// overhead — feed a keystroke on In, the loop reads it and calls Session.Input,
// the echo returns on Frames, the loop writes it to Out — with no network or PTY
// in the path. We stamp each keystroke, detect its echo on Out, and record the
// delta. The assertion is on p95 of >= 1000 samples, per N-2. N-2 IS this
// client-side ADDED latency — the passthrough's own overhead — which is what this
// test measures. The true end-to-end keystroke budget over a live shim is E14.4 and
// is not asserted here or anywhere yet (there is no e2e latency assertion).
func TestPassthrough_KeystrokeEchoLatencyP95(t *testing.T) {
	if testing.Short() {
		t.Skip("latency budget is a full-run assertion")
	}
	const samples = 1000

	term := newFakeTerm(80, 24)
	sess := newFakeSession([]byte("READY"))
	sess.echo = true // round-trip: Input -> Frames

	ch := runInBackground(Config{Term: term, Session: sess})
	eventually(t, func() bool { return len(term.outBytes()) > 0 }) // snapshot painted

	// Each keystroke is a distinct 4-byte marker so its echo is unambiguous on Out.
	rec := &latencyRecorder{out: term.out, seen: make(map[string]time.Time)}
	baseline := len(term.outBytes())
	rec.baseline = baseline

	durations := make([]time.Duration, 0, samples)
	for i := 0; i < samples; i++ {
		marker := latencyMarker(i)
		start := time.Now()
		term.feed(marker)
		d, ok := rec.waitEcho(marker, start, 2*time.Second)
		if !ok {
			t.Fatalf("keystroke %d never echoed back within 2s", i)
		}
		durations = append(durations, d)
	}

	p95 := percentile(durations, 0.95)
	if p95 >= 10*time.Millisecond {
		t.Fatalf("keystroke echo p95 = %v, want < 10ms (N-2)", p95)
	}

	term.feed([]byte{DefaultDetachKey})
	_ = waitResult(t, ch)
}

// latencyMarker is a unique 4-byte keystroke marker (avoids the detach key byte).
func latencyMarker(i int) []byte {
	return []byte{'K', byte(i >> 8), byte(i), '\n'}
}

type latencyRecorder struct {
	out      *lockedBuffer
	baseline int
	seen     map[string]time.Time
}

// waitEcho blocks until marker appears in Out beyond the baseline, returning the
// elapsed time since start. It advances the baseline past the observed echo so the
// next marker's search is not confused by earlier output.
func (r *latencyRecorder) waitEcho(marker []byte, start time.Time, timeout time.Duration) (time.Duration, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		full := r.out.Bytes()
		if idx := indexFrom(full, marker, r.baseline); idx >= 0 {
			r.baseline = idx + len(marker)
			return time.Since(start), true
		}
		time.Sleep(time.Millisecond)
	}
	return 0, false
}

// indexFrom finds sub in b at or after offset from.
func indexFrom(b, sub []byte, from int) int {
	if from < 0 || from > len(b) {
		from = 0
	}
	for i := from; i+len(sub) <= len(b); i++ {
		match := true
		for j := range sub {
			if b[i+j] != sub[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// percentile returns the p-quantile (0<=p<=1) of ds using nearest-rank.
func percentile(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := int(p*float64(len(sorted)-1) + 0.5)
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}
