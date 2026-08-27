package protocol

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func TestStampViewCarriesEffectiveGroupEnteredAt(t *testing.T) {
	activity := time.Date(2026, 8, 27, 16, 0, 0, 0, time.UTC)
	v := stampView("ep", persist.Meta{ID: "s1", LastActivity: activity}, status.GroupWorking, false, false, time.Time{})
	if !v.GroupEnteredAt.Equal(activity) {
		t.Fatalf("legacy view group_entered_at = %v, want last_activity fallback %v", v.GroupEnteredAt, activity)
	}

	explicit := activity.Add(time.Hour)
	v = stampView("ep", persist.Meta{ID: "s1", LastActivity: activity, GroupEnteredAt: explicit}, status.GroupWorking, false, false, time.Time{})
	if !v.GroupEnteredAt.Equal(explicit) {
		t.Fatalf("view group_entered_at = %v, want explicit %v", v.GroupEnteredAt, explicit)
	}
}
