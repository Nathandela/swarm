package codex

// FAILING-FIRST for ADR-010 Amendment 5 F1: the codex adapter characterizes WHERE its
// rollouts live. Codex files one rollout per thread under
// ~/.codex/sessions/YYYY/MM/DD/rollout-<stamp>-<id>.jsonl, and both the day and the
// stamp are the thread's creation time in UTC -- which a codex thread id, a UUIDv7,
// carries in its first 48 bits. Measured 2026-09-02 on four real rollouts: every name's
// stamp equalled its id's embedded time to the second and its directory was that UTC day.

import (
	"fmt"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
)

// uuidv7At composes a canonical UUIDv7 whose 48-bit prefix is when's Unix millisecond,
// the way codex mints thread ids.
func uuidv7At(when time.Time) string {
	ms := fmt.Sprintf("%012x", when.UnixMilli())
	return ms[:8] + "-" + ms[8:12] + "-7000-8000-000000000000"
}

func datedLayout(t *testing.T) adapter.DatedTranscriptLayout {
	t.Helper()
	layout, ok := adapter.AsDatedTranscriptLayout(newAdapter())
	if !ok {
		t.Fatal("the codex adapter is not an adapter.DatedTranscriptLayout; hands-off cannot locate its rollouts (ADR-010 Amendment 5 F1)")
	}
	return layout
}

func TestTranscriptDay_ReadsTheUTCDayOutOfAUUIDv7ThreadID(t *testing.T) {
	layout := datedLayout(t)
	for _, tc := range []struct {
		name string
		when time.Time
	}{
		// rollout-2026-09-02T00-32-54-01a05f88-7ab7-....jsonl sits under sessions/2026/09/02,
		// and 0x01a05f887ab7 ms is 2026-09-02T00:32:54Z.
		{"measured rollout", time.Date(2026, 9, 2, 0, 32, 54, 0, time.UTC)},
		{"last millisecond of a day", time.Date(2026, 8, 31, 23, 59, 59, 999e6, time.UTC)},
		{"first millisecond of a day", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			day, ok := layout.TranscriptDay(uuidv7At(tc.when))
			want := time.Date(tc.when.Year(), tc.when.Month(), tc.when.Day(), 0, 0, 0, 0, time.UTC)
			if !ok || !day.Equal(want) || day.Location() != time.UTC {
				t.Fatalf("TranscriptDay = (%v, %v), want (%v, true) in UTC", day, ok, want)
			}
		})
	}
}

// TestTranscriptDay_RefusesIDsThatCarryNoDay: a day is only ever read out of a canonical
// UUIDv7. Anything else -- a v4 id, whose bits are random; a shape that is not canonical;
// a traversal -- reports false, so the resolver refuses by name instead of listing a
// directory named after garbage.
func TestTranscriptDay_RefusesIDsThatCarryNoDay(t *testing.T) {
	layout := datedLayout(t)
	for _, id := range []string{
		"",
		"f41b0e35-6fa4-4c8b-bfea-8687b311255b", // v4: random bits, no timestamp
		"01a05f88-7ab7-4000-8000-000000000000", // a v7 prefix under a v4 version nibble
		"01A05F88-7AB7-7000-8000-000000000000", // not canonical (uppercase)
		"01a05f88-7ab7-7000-8000-00000000000",  // one character short
		"../../etc/passwd",
	} {
		if day, ok := layout.TranscriptDay(id); ok || !day.IsZero() {
			t.Errorf("TranscriptDay(%q) = (%v, %v), want (zero, false)", id, day, ok)
		}
	}
}
