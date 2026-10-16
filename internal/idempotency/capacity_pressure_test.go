package idempotency

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCapacityPressurePreservesProtectedPhasesAndExactOutcomeAfterRestart(t *testing.T) {
	for _, phase := range []Phase{PhasePrepared, PhaseExecuting, PhaseCompleted, PhaseFailed, PhaseOutcomeUnknown} {
		t.Run(string(phase), func(t *testing.T) {
			now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
			opts := Options{TTL: 24 * time.Hour, MaxEntries: 2, Clock: func() time.Time { return now }}
			dir := t.TempDir()
			s, err := OpenWithOptions(dir, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.f.Close() }()
			if _, _, err := s.PrepareBound("protected", "composer_send", "session", "instance", "request-hash"); err != nil {
				t.Fatal(err)
			}
			outcome := []byte(`{"ok":false,"code":"outcome_unknown","message":"exact prior outcome"}`)
			switch phase {
			case PhaseExecuting:
				err = s.Begin("protected")
			case PhaseCompleted:
				err = s.Complete("protected", outcome)
			case PhaseFailed:
				err = s.Fail("protected", outcome)
			case PhaseOutcomeUnknown:
				err = s.ResolveOutcomeUnknown("protected")
			}
			if err != nil {
				t.Fatal(err)
			}
			protected, _ := s.Get("protected")
			now = now.Add(time.Minute)
			if _, _, err := s.Prepare("unrelated", "kill", "other-session"); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(dir, logFile))
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 32; i++ {
				if _, existed, err := s.Prepare(fmt.Sprintf("pressure-%d", i), "kill", "other-session"); !errors.Is(err, ErrCapacity) || existed {
					t.Fatalf("full store accepted unrelated operation: existed=%v err=%v", existed, err)
				}
			}
			after, err := os.ReadFile(filepath.Join(dir, logFile))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("refused pressure changed the durable log: %v", err)
			}
			if err := s.Compact(); err != nil {
				t.Fatal(err)
			}
			if err := s.f.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenWithOptions(dir, opts)
			if err != nil {
				t.Fatal(err)
			}
			replay, existed, err := s.PrepareBound("protected", "composer_send", "session", "instance", "request-hash")
			if err != nil || !existed || replay.Phase != protected.Phase || !bytes.Equal(replay.Outcome, protected.Outcome) ||
				replay.RequestHash != protected.RequestHash || replay.SessionInstance != protected.SessionInstance || !replay.UpdatedAt.Equal(protected.UpdatedAt) {
				t.Fatalf("protected retry changed after pressure/restart: %+v existed=%v err=%v", replay, existed, err)
			}
		})
	}
}

func TestCapacityAdmissionReclaimsOnlyTTLExpiredGuards(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	s, err := OpenWithOptions(t.TempDir(), Options{TTL: 24 * time.Hour, MaxEntries: 2, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.f.Close() }()
	if _, _, err := s.Prepare("old", "take_control", "session"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(23 * time.Hour)
	if _, _, err := s.Prepare("fresh", "take_control", "session"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if _, _, err := s.Prepare("new", "take_control", "session"); !errors.Is(err, ErrCapacity) {
		t.Fatal("a guard exactly at its TTL was discarded")
	}
	now = now.Add(time.Nanosecond)
	if _, existed, err := s.Prepare("new", "take_control", "session"); err != nil || existed {
		t.Fatalf("expired guard did not release admission capacity: existed=%v err=%v", existed, err)
	}
	if _, ok := s.Get("old"); ok {
		t.Fatal("TTL-expired guard still occupies capacity")
	}
	if _, ok := s.Get("fresh"); !ok {
		t.Fatal("admission discarded an unrelated unexpired guard")
	}
}

func TestRedriveCannotBypassNewOperationCapacity(t *testing.T) {
	s, err := OpenWithOptions(t.TempDir(), Options{MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.f.Close() }()
	if _, _, err := s.Prepare("existing", "launch", "first-session"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Redrive("new", "launch", "other-session"); !errors.Is(err, ErrCapacity) {
		t.Fatal("Redrive admitted a new operation past capacity")
	}
	if _, err := s.Redrive("existing", "launch", "replacement-session"); err != nil {
		t.Fatalf("existing operation lost its reserved slot: %v", err)
	}
}

func TestCompactionPreservesLegacyUnexpiredGuardsAboveAdmissionLimit(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	s, err := OpenWithOptions(dir, Options{Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.f.Close() }()
	for i := 0; i < 3; i++ {
		op := fmt.Sprintf("accepted-%d", i)
		if _, _, err := s.Prepare(op, "kill", "session"); err != nil {
			t.Fatal(err)
		}
		if err := s.Complete(op, []byte(fmt.Sprintf(`{"accepted":%d}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.f.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenWithOptions(dir, Options{TTL: 24 * time.Hour, MaxEntries: 2, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(); err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 3 {
		t.Fatal("compaction evicted a legacy unexpired guard to meet the admission limit")
	}
	if err := s.f.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenWithOptions(dir, Options{TTL: 24 * time.Hour, MaxEntries: 2, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		op := fmt.Sprintf("accepted-%d", i)
		rec, existed, err := s.Prepare(op, "kill", "session")
		if err != nil || !existed || rec.Phase != PhaseCompleted || string(rec.Outcome) != fmt.Sprintf(`{"accepted":%d}`, i) {
			t.Fatalf("legacy exact outcome lost after compaction/restart: %+v existed=%v err=%v", rec, existed, err)
		}
	}
	if _, _, err := s.Prepare("new", "kill", "session"); !errors.Is(err, ErrCapacity) {
		t.Fatal("over-cap legacy state admitted another operation")
	}
	now = now.Add(24*time.Hour + time.Nanosecond)
	if _, _, err := s.Prepare("new", "kill", "session"); err != nil {
		t.Fatalf("expired legacy guards did not release capacity: %v", err)
	}
}

func TestCapacityCompactionFailureRefusesNewOperationUntilRecovery(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	s, err := OpenWithOptions(dir, Options{TTL: 24 * time.Hour, MaxEntries: 1, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.f.Close() }()
	if _, _, err := s.Prepare("old", "kill", "session"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, logFile))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(25 * time.Hour)
	originalRename := renameLog
	defer func() { renameLog = originalRename }()
	storageErr := errors.New("compaction rename failed")
	renameLog = func(string, string) error { return storageErr }
	if _, _, err := s.Prepare("new", "kill", "session"); !errors.Is(err, storageErr) {
		t.Fatalf("new operation admitted without durable capacity recovery: %v", err)
	}
	if _, ok := s.Get("new"); ok {
		t.Fatal("failed compaction claimed the new operation")
	}
	after, err := os.ReadFile(filepath.Join(dir, logFile))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed capacity compaction changed the accepted log: %v", err)
	}
	renameLog = originalRename
	if _, _, err := s.Prepare("new", "kill", "session"); err != nil {
		t.Fatalf("capacity recovery failed after storage recovered: %v", err)
	}
}

func TestRepeatedTransitionsBoundLogAndPreserveLatestOutcome(t *testing.T) {
	dir := t.TempDir()
	opts := Options{MaxEntries: 2}
	s, err := OpenWithOptions(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.f.Close() }()
	if _, _, err := s.Prepare("retry", "launch", "first-session"); err != nil {
		t.Fatal(err)
	}
	var outcome []byte
	for i := 0; i < 20; i++ {
		if _, err := s.Redrive("retry", "launch", fmt.Sprintf("session-%d", i)); err != nil {
			t.Fatal(err)
		}
		if err := s.Begin("retry"); err != nil {
			t.Fatal(err)
		}
		outcome = []byte(fmt.Sprintf(`{"failed_attempt":%d}`, i))
		if err := s.Fail("retry", outcome); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, logFile))
		if err != nil || bytes.Count(raw, []byte{'\n'}) > 2*opts.MaxEntries {
			t.Fatalf("repeated transitions grew the append log: lines=%d err=%v", bytes.Count(raw, []byte{'\n'}), err)
		}
	}
	if err := s.f.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenWithOptions(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if rec, exists, err := s.Prepare("retry", "launch", "unused"); err != nil || !exists || rec.Phase != PhaseFailed || !bytes.Equal(rec.Outcome, outcome) {
		t.Fatalf("latest guarded outcome lost on restart: %+v existed=%v err=%v", rec, exists, err)
	}
}

func TestAutomaticCompactionDirSyncFailureBlocksAcknowledgementUntilRecovery(t *testing.T) {
	dir := t.TempDir()
	opts := Options{MaxEntries: 2}
	s, err := OpenWithOptions(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.f.Close() }()
	if _, _, err := s.Prepare("accepted", "launch", "session"); err != nil {
		t.Fatal(err)
	}
	if err := s.Begin("accepted"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.Complete("accepted", []byte(`{"ok":true}`)); err != nil {
			t.Fatal(err)
		}
	}
	originalSync := syncDir
	defer func() { syncDir = originalSync }()
	storageErr := errors.New("replacement namespace is not durable")
	syncDir = func(string) error { return storageErr }
	if _, _, err := s.Prepare("new", "kill", "other-session"); !errors.Is(err, storageErr) {
		t.Fatalf("automatic compaction did not reach the injected namespace failure: %v", err)
	}
	if _, _, err := s.Prepare("new", "kill", "other-session"); !errors.Is(err, storageErr) {
		t.Fatalf("retry acknowledged a guard without a durable replacement namespace: %v", err)
	}
	if _, _, err := s.Prepare("accepted", "launch", "session"); !errors.Is(err, storageErr) {
		t.Fatalf("cached acknowledgement bypassed the pending namespace barrier: %v", err)
	}
	if reopened, err := OpenWithOptions(dir, opts); !errors.Is(err, storageErr) {
		if reopened != nil {
			_ = reopened.f.Close()
		}
		t.Fatalf("process reopen forgot an unresolved namespace barrier: %v", err)
	}
	if _, ok := s.Get("new"); ok {
		t.Fatal("failed namespace recovery claimed a new guard")
	}
	syncDir = originalSync
	if _, _, err := s.Prepare("new", "kill", "other-session"); err != nil {
		t.Fatalf("recovered directory barrier did not release admission: %v", err)
	}
	if err := s.f.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenWithOptions(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, existed, err := s.Prepare("new", "kill", "other-session"); err != nil || !existed {
		t.Fatalf("acknowledged post-recovery guard was lost on restart: existed=%v err=%v", existed, err)
	}
}

func TestAutomaticCompactionFailurePreservesExecutionBoundary(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprintf("terminal-%v", terminal), func(t *testing.T) {
			s, err := OpenWithOptions(t.TempDir(), Options{MaxEntries: 2})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.f.Close() }()
			if _, _, err := s.Prepare("protected", "composer_send", "session"); err != nil {
				t.Fatal(err)
			}
			if terminal {
				if err := s.Begin("protected"); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := s.Prepare("other", "kill", "other-session"); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if !terminal || i == 0 {
					if err := s.Complete("other", nil); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, _ := s.Get("protected")
			originalRename := renameLog
			defer func() { renameLog = originalRename }()
			storageErr := errors.New("automatic compaction rename failed")
			renameLog = func(string, string) error { return storageErr }
			transition := func() error {
				if terminal {
					return s.Complete("protected", []byte(`{"ok":true}`))
				}
				return s.Begin("protected")
			}
			if err := transition(); !errors.Is(err, storageErr) {
				t.Fatalf("transition bypassed failed compaction: %v", err)
			}
			if after, _ := s.Get("protected"); after.Phase != before.Phase || !bytes.Equal(after.Outcome, before.Outcome) {
				t.Fatalf("failed durable transition changed execution authority: before=%+v after=%+v", before, after)
			}
			renameLog = originalRename
			if err := transition(); err != nil {
				t.Fatalf("transition failed after storage recovered: %v", err)
			}
		})
	}
}

func TestOpenRequiresDurableLogNamespace(t *testing.T) {
	for _, parent := range []bool{false, true} {
		t.Run(fmt.Sprintf("parent-%v", parent), func(t *testing.T) {
			state := t.TempDir()
			dir := filepath.Join(state, "idempotency")
			originalSync := syncDir
			defer func() { syncDir = originalSync }()
			storageErr := errors.New("directory durability unavailable")
			failPath := dir
			if parent {
				failPath = state
			}
			syncDir = func(path string) error {
				if path == failPath {
					return storageErr
				}
				return originalSync(path)
			}
			if s, err := OpenWithOptions(dir, Options{MaxEntries: 2}); !errors.Is(err, storageErr) {
				if s != nil {
					_ = s.f.Close()
				}
				t.Fatalf("opened without establishing a durable log namespace: %v", err)
			}
			syncDir = originalSync
			s, err := OpenWithOptions(dir, Options{MaxEntries: 2})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.f.Close() }()
			if _, _, err := s.Prepare("after-recovery", "kill", "session"); err != nil {
				t.Fatalf("reopened store could not accept after namespace recovery: %v", err)
			}
		})
	}
}
