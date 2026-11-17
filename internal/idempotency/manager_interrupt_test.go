package idempotency

import (
	"errors"
	"testing"
	"time"
)

func TestManagerInterruptExecutingRecordSurvivesCompaction(t *testing.T) {
	now := time.Now()
	s, err := OpenWithOptions(t.TempDir(), Options{TTL: time.Hour, MaxEntries: 1, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.f.Close() }()
	if _, _, err = s.PrepareBound("stop", "manager_interrupt", "manager", "instance", "exact-request"); err != nil {
		t.Fatal(err)
	}
	if err = s.Begin("stop"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(48 * time.Hour)
	if err = s.Compact(); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.PrepareBound("another", "manager_interrupt", "manager", "instance", "another-request"); !errors.Is(err, ErrCapacity) {
		t.Fatal("unknown native stop expired into available capacity", err)
	}
}
