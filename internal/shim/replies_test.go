package shim

// Emulator query-reply carry-forward (bead agents-tracker-b2l note; ADR
// dependency from Epic 2): agent CLIs emit terminal queries (here a DSR,
// "\x1b[6n") and BLOCK waiting for the reply on their stdin. The shim's emulator
// generates the correct reply; the shim MUST pipe that reply back into the PTY
// master so the agent receives it (rather than discarding it as the Epic 2
// fuzz-guard drain did). This end-to-end test proves the whole chain:
// agent-output -> shim reads master -> emulator generates reply -> shim writes
// reply to master -> agent reads it from stdin.
//
// The DSR helper sets its tty raw (a CPR reply carries no newline, so a
// canonical read would block), emits the query, and prints DSR_OK only if it
// actually receives the report. If the shim failed to pipe replies back, the
// helper would time out and print DSR_TIMEOUT.

import (
	"strings"
	"testing"
	"time"
)

func TestEmulatorReplies_DSRPipedBackToPTY(t *testing.T) {
	cfg := helperConfig(t, modeDSR, nil, nil)
	r := waitRun(t, runShimAsync(cfg), 15*time.Second)
	if r.err != nil {
		t.Fatalf("Run: %v", r.err)
	}
	tr := readTranscript(t, cfg.SessionDir)
	if strings.Contains(tr, "DSR_TIMEOUT") {
		t.Fatalf("agent never received its cursor-position report — the shim did not pipe emulator replies back to the PTY master")
	}
	if strings.Contains(tr, "DSR_RAWFAIL") {
		t.Skipf("could not set raw mode on the PTY slave in this environment; transcript:\n%s", tr)
	}
	if !strings.Contains(tr, "DSR_OK") {
		t.Fatalf("expected DSR_OK in transcript (proof the reply reached the agent); got:\n%s", tr)
	}
}
