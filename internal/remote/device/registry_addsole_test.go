package device

// FAILING-FIRST (TDD RED, GG-5) tests for the atomic single-device primitive AddSole
// (re-audit finding C1). BeginPairing enforces single-device v1 with a non-atomic
// Count()>0 check-then-Add: two concurrent owner pairings both observe Count()==0 at
// entry and both Add seconds later, leaving TWO devices and bricking the gateway ("want
// exactly one"). AddSole closes that race: under the registry mutex it rejects a commit
// when a DIFFERENT device already exists, so the second of two racing enrollments loses.
// The general Add stays uncapped (the registry's multi-device tests are unaffected).

import (
	"sync"
	"testing"
)

// TestRegistry_AddSole_RejectsDifferentDevice pins the sequential contract: the first
// AddSole enrolls; a SECOND AddSole for a DIFFERENT device is rejected and changes
// nothing; re-adding the SAME device is an idempotent upsert.
func TestRegistry_AddSole_RejectsDifferentDevice(t *testing.T) {
	reg, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}

	first := fullRecord(t, 0xA1, CapFull, 1)
	if err := reg.AddSole(first); err != nil {
		t.Fatalf("AddSole(first) error: %v", err)
	}
	if got := reg.Count(); got != 1 {
		t.Fatalf("Count after first AddSole = %d, want 1", got)
	}

	second := fullRecord(t, 0xA2, CapFull, 1) // a DIFFERENT device id
	if err := reg.AddSole(second); err == nil {
		t.Fatalf("AddSole(second) = nil error; want rejection while a different device is paired")
	}
	if got := reg.Count(); got != 1 {
		t.Fatalf("Count after rejected AddSole = %d, want 1 (unchanged)", got)
	}
	if _, ok := reg.Get(first.DeviceID); !ok {
		t.Fatalf("first device vanished after a rejected AddSole")
	}
	if _, ok := reg.Get(second.DeviceID); ok {
		t.Fatalf("rejected second device was admitted")
	}

	// Re-adding the SAME device is an idempotent upsert, not a rejection.
	if err := reg.AddSole(first); err != nil {
		t.Fatalf("AddSole(first) re-add error: %v", err)
	}
	if got := reg.Count(); got != 1 {
		t.Fatalf("Count after idempotent re-AddSole = %d, want 1", got)
	}
}

// TestRegistry_AddSole_ConcurrentCommitsPickOne models finding C1 directly: two enrollments
// that BOTH passed a Count()==0 pre-check race to commit their DIFFERENT devices via
// AddSole. Exactly ONE must win and Count must stay 1 -- never 2 (which bricks the gateway).
func TestRegistry_AddSole_ConcurrentCommitsPickOne(t *testing.T) {
	reg, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}

	a := fullRecord(t, 0xB1, CapFull, 1)
	b := fullRecord(t, 0xB2, CapFull, 1)

	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	start := make(chan struct{})
	for _, rec := range []Record{a, b} {
		wg.Add(1)
		go func(r Record) {
			defer wg.Done()
			<-start // release both commits as simultaneously as the scheduler allows
			if err := reg.AddSole(r); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}(rec)
	}
	close(start)
	wg.Wait()

	if ok != 1 {
		t.Fatalf("AddSole succeeded %d times under a concurrent race; want exactly 1", ok)
	}
	if got := reg.Count(); got != 1 {
		t.Fatalf("Count after racing enrollments = %d; want 1 (a 2 would brick the gateway)", got)
	}
}
