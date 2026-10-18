package idempotency

import (
	"errors"
	"testing"
	"time"
)

func TestUnresolvedSemanticSendSurvivesExpiryAndRestart(t *testing.T) {
	for _, phase := range []Phase{PhaseExecuting, PhaseOutcomeUnknown} {
		for _, compact := range []bool{false, true} {
			name := string(phase) + "/admission"
			if compact {
				name = string(phase) + "/compact"
			}
			t.Run(name, func(t *testing.T) {
				now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
				opts := Options{TTL: 24 * time.Hour, MaxEntries: 1, Clock: func() time.Time { return now }}
				dir := t.TempDir()
				s, err := OpenWithOptions(dir, opts)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = s.f.Close() })
				if _, _, err := s.PrepareBound("op1", "semantic_send", "session", "instance", "hash"); err != nil {
					t.Fatal(err)
				}
				if err := s.Begin("op1"); err != nil {
					t.Fatal(err)
				}
				if phase == PhaseOutcomeUnknown {
					if err := s.ResolveOutcomeUnknown("op1"); err != nil {
						t.Fatal(err)
					}
				}
				now = now.Add(25 * time.Hour)
				if compact {
					if err := s.Compact(); err != nil {
						t.Fatal(err)
					}
				}
				if _, _, err := s.PrepareBound("op2", "semantic_send", "session", "instance", "other"); !errors.Is(err, ErrCapacity) {
					t.Fatalf("unresolved guard was evicted to admit another effect: %v", err)
				}
				if err := s.f.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = OpenWithOptions(dir, opts)
				if err != nil {
					t.Fatal(err)
				}
				rec, existed, err := s.PrepareBound("op1", "semantic_send", "session", "instance", "hash")
				if err != nil || !existed || rec.Phase != phase {
					t.Fatalf("uncertain operation became a fresh write after restart: existed=%v phase=%s err=%v", existed, rec.Phase, err)
				}
			})
		}
	}
}
