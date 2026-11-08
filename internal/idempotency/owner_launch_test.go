package idempotency

import (
	"errors"
	"testing"
	"time"
)

func TestOwnerLaunchUncertainGuardSurvivesCompactionAndCapacity(t *testing.T) {
	now := time.Now()
	st, err := OpenWithOptions(t.TempDir(), Options{TTL: time.Hour, MaxEntries: 1, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PrepareBound("launch:original", "owner_launch", "reserved", "", "exact"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(48 * time.Hour)
	if err := st.Compact(); err != nil {
		t.Fatal(err)
	}
	if _, known := st.Get("launch:original"); !known {
		t.Fatal("uncertain launch lost its replay guard")
	}
	if _, _, err := st.Prepare("new", "owner_launch", "another"); !errors.Is(err, ErrCapacity) {
		t.Fatalf("new work displaced uncertain launch: %v", err)
	}
	if _, existed, err := st.PrepareBound("launch:original", "owner_launch", "ignored", "", "exact"); err != nil || !existed {
		t.Fatalf("exact retry lost: %v", err)
	}
}
