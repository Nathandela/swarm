package daemon

// R6 (bd agents-tracker-hggx.7) FAILING-FIRST (TDD RED, GG-5): EmitStructuredGap becomes
// REAL. structuredgap.go's own header records exactly this moment: "the spool-boundary
// detection (ADR-010's spool) does not exist yet, so EmitStructuredGap returns
// ErrStructuredGapUnimplemented and appends nothing"; structuredgap_test.go's stub test
// pins that behavior "until the spool-boundary detection... lands". R6 IS that landing
// (internal/shim's HookSpool -- see internal/shim/r6_gap_test.go -- and internal/skeleton's
// drain, r6_hookdrain_test.go, which is the actual caller of EmitStructuredGap on a proven
// gap). This file pins the seam's OWN, package-local half: a real emission appends the
// journal record playbook 6.1 describes, for ANY (sessionID, reason) -- who calls it, and
// under what proven condition, is skeleton's obligation, not this package's.
//
// NO NEW SYMBOLS beyond what structuredgap.go already declares (StructuredGapEvent,
// journal.TypeStructuredGap, EmitStructuredGap). This is a pure behavioral RED: it compiles
// today and fails because EmitStructuredGap still, unconditionally, returns
// ErrStructuredGapUnimplemented and appends nothing.
//
// internal/daemon cannot import internal/protocol (protocol already imports daemon --
// internal/skeleton's own package doc says so), so the session-capability DEGRADE half of
// playbook 6.1 ("disables structured_chat for that session instance") is necessarily out of
// this package's reach and is skeleton's obligation (r6_hookdrain_test.go), which holds the
// only type (protocol.SessionCapabilities) that rule can be expressed against. This file's
// job ends at "the journal gains an honest structured_gap record."

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/journal"
)

// TestEmitStructuredGap_AppendsARealJournalRecord is the R6 landing: once spool-boundary
// detection exists (elsewhere), EmitStructuredGap must actually append -- not keep returning
// the stub error the R1 skeleton deliberately left in place until this moment.
func TestEmitStructuredGap_AppendsARealJournalRecord(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))

	before, err := d.JournalReadFrom(0)
	if err != nil {
		t.Fatalf("JournalReadFrom before: %v", err)
	}

	start := time.Now()
	if err := d.EmitStructuredGap("s1", "spool cursor gap at seq 42"); err != nil {
		t.Fatalf("EmitStructuredGap: %v, want nil — R6 lands real emission, the stub is retired", err)
	}

	after, err := d.JournalReadFrom(0)
	if err != nil {
		t.Fatalf("JournalReadFrom after: %v", err)
	}
	if len(after.Events) != len(before.Events)+1 {
		t.Fatalf("journal grew by %d record(s), want exactly 1", len(after.Events)-len(before.Events))
	}

	rec := after.Events[len(after.Events)-1]
	if rec.Type != journal.TypeStructuredGap {
		t.Fatalf("appended record type = %q, want %q", rec.Type, journal.TypeStructuredGap)
	}
	if rec.SessionID != "s1" {
		t.Fatalf("appended record session_id = %q, want %q (the enclosing journal.Record carries it, not the payload)", rec.SessionID, "s1")
	}

	var ev StructuredGapEvent
	if err := json.Unmarshal(rec.Payload, &ev); err != nil {
		t.Fatalf("decode StructuredGapEvent payload: %v", err)
	}
	if ev.Reason != "spool cursor gap at seq 42" {
		t.Errorf("event reason = %q, want %q", ev.Reason, "spool cursor gap at seq 42")
	}
	if ev.TS.Before(start.Add(-time.Second)) || ev.TS.After(time.Now().Add(time.Second)) {
		t.Errorf("event ts %v is not within this test's execution window (start %v)", ev.TS, start)
	}
}

// TestEmitStructuredGap_EveryCallAppendsItsOwnBoundary: two gaps on two sessions (or two
// gaps on the same session across two separate detections) are two separate, honest
// records -- emission is never coalesced or deduplicated away, because each one names a
// DIFFERENT proven boundary and playbook 6.1 forbids silently bridging any of them.
func TestEmitStructuredGap_EveryCallAppendsItsOwnBoundary(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))

	if err := d.EmitStructuredGap("s1", "first gap"); err != nil {
		t.Fatalf("EmitStructuredGap (1st): %v", err)
	}
	if err := d.EmitStructuredGap("s2", "second gap"); err != nil {
		t.Fatalf("EmitStructuredGap (2nd): %v", err)
	}

	res, err := d.JournalReadFrom(0)
	if err != nil {
		t.Fatalf("JournalReadFrom: %v", err)
	}
	var gaps []journalGapRow
	for _, rec := range res.Events {
		if rec.Type == journal.TypeStructuredGap {
			gaps = append(gaps, journalGapRow{sessionID: rec.SessionID})
		}
	}
	if len(gaps) != 2 {
		t.Fatalf("journal holds %d structured_gap record(s), want 2 (one per EmitStructuredGap call)", len(gaps))
	}
	if gaps[0].sessionID != "s1" || gaps[1].sessionID != "s2" {
		t.Fatalf("structured_gap session ids = %v, want [s1, s2] in call order", gaps)
	}
}

type journalGapRow struct{ sessionID string }
