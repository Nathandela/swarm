package journal

// FAILING-FIRST (RED) guard for finding DHI-2 (remote Phase-1 review). The daemon's
// atomic-snapshot resume (R-JRN.4, Resume.Roster) needs a dedicated record type for
// the SYNTHETIC roster entries it emits — journal.TypeRoster — which is NEVER written
// to the append log: the roster is a live-set snapshot the daemon materializes at read
// time, not a journalled transition.
//
// WHY THIS FAILS TODAY: journal.go declares no TypeRoster constant. This file
// references TypeRoster (undefined), so package journal fails to COMPILE — an
// undefined-only RED.
//
// CONTRACT PINNED: TypeRoster == "roster"; a roster-typed record is snapshot-only, so
// the ordinary Append/ReadFrom event stream never surfaces one, and a bare journal
// (which has no session source) yields an empty Resume.Roster — the daemon, not the
// journal, populates the roster.

import "testing"

// TestTypeRoster_ExistsAndIsSnapshotOnly_DHI2 is a light guard that the constant
// exists with the agreed value and that the append/event path never produces or
// carries a roster-typed record.
func TestTypeRoster_ExistsAndIsSnapshotOnly_DHI2(t *testing.T) {
	if TypeRoster != "roster" {
		t.Fatalf("journal.TypeRoster = %q; want %q", TypeRoster, "roster")
	}

	dir := t.TempDir()
	j, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = j.Close() }()

	// A normal lifecycle append.
	if _, err := j.Append(Record{Type: TypeLaunched, SessionID: "s1"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	res, err := j.ReadFrom(0)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}

	// The journal has no daemon session source, so its snapshot roster is empty: the
	// roster is populated by the daemon's JournalReadFrom, never by the append log.
	if len(res.Roster) != 0 {
		t.Fatalf("bare journal ReadFrom populated %d roster records; roster is snapshot-only (daemon-populated)", len(res.Roster))
	}

	// A roster-typed record must NEVER appear in the appended event stream.
	for _, r := range res.Events {
		if r.Type == TypeRoster {
			t.Fatalf("event stream carried a roster-typed record at cursor %d; TypeRoster is synthetic and never appended", r.Cursor)
		}
	}
}
