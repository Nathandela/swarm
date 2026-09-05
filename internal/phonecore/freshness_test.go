package phonecore

import (
	"testing"
	"time"
)

// PB-APP-8/PB-APP-11: cached state becomes stale from the newest accepted,
// authenticated machine timestamp, even while the relay keeps the stream open.
func TestPBAPP11_CachedStateFreshnessExpiresAfterFiveMinutes(t *testing.T) {
	if FreshnessBudget != 5*time.Minute {
		t.Fatalf("FreshnessBudget = %v, want section 6.0's 5m", FreshnessBudget)
	}
	core, err := Resume(Config{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_750_000_000, 0)
	if err := core.Mutate(func(st *State) { st.LastHeardAt = now.Add(-FreshnessBudget).UnixMilli() }); err != nil {
		t.Fatal(err)
	}
	if core.MachineSilentAt(now) {
		t.Fatal("machine is silent at exactly the freshness boundary")
	}
	if !core.MachineSilentAt(now.Add(time.Millisecond)) {
		t.Fatal("cached state stayed live after the freshness budget elapsed")
	}
}
