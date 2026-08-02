package schema

// JournalReseed is the machine->phone JOURNAL RESEED (PB-SYNC-2 / PB-SYNC-8): the ATOMIC
// roster+events snapshot that repairs the journal channel after a gap, carried as ONE
// sealed frame on the EXISTING machine->phone stream.
//
// WHY ONE FRAME AND NOT N ROSTER RECORDS. PB-SYNC-3 requires the repair to be committed
// atomically with the matching transport watermark, and N independent roster frames cannot
// be: a process death between frames 3 and 4 leaves the phone with half a snapshot and a
// watermark that says it has the whole thing. One frame's own arrival seq IS the matching
// watermark, exactly as ReconcileRecord's arrival certifies its JournalCeiling.
//
// WHY IT CARRIES ITS OWN CURSOR. The daemon emits roster records with Cursor DELIBERATELY
// UNSET (0) -- "a roster record is a set member keyed by SessionID, NOT a point in the
// cursor-ordered event stream" (internal/daemon/journal.go) -- while the phone's
// SessionCache drops any record whose Cursor is below the highest applied one. Merging a
// reseed into a live cache therefore discards every roster record the moment one event has
// advanced the cursor, and the designated repair channel becomes a silent no-op. Cursor
// here is the snapshot BOUNDARY (JournalResume.Cursor), and it REPLACES the cache cursor
// rather than being merged into it.
//
// NO FIELD MAY CARRY omitempty, for the reason ReconcileRecord states: a legitimately-empty
// roster (a machine with no live sessions) must stay distinguishable on the wire from a
// producer that never published the field, or a phone reading "absent" as "empty" cannot
// tell a real repair from a malformed one.
type JournalReseed struct {
	// Roster is the machine's COMPLETE live session set as of Cursor. It replaces the
	// phone's cached set; a session absent here has gone away while the phone was not
	// listening, and leaving it behind is how a killed session stays on the roster forever.
	Roster []JournalRecord `json:"roster"`
	// Events are the journal records after the phone's last known cursor and up to Cursor,
	// applied ON TOP of Roster -- the "atomic roster+events snapshot" of PB-SYNC-2.
	Events []JournalRecord `json:"events"`
	// Cursor is the snapshot boundary the roster is current as of. It REPLACES the phone's
	// cache cursor (PB-SYNC-8); it is never merged, never max'd.
	Cursor uint64 `json:"cursor"`
}
