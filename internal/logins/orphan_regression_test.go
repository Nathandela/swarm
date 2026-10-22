package logins

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAstraOwnTemporaryCrashDoesNotBrickReconcile(t *testing.T) {
	s := testStore(t)
	id := strings.Repeat("a", 32)
	dir := filepath.Join(s.path, "saved", id)
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, ".login-tmp-"+strings.Repeat("b", 32)), []byte(`{"partial":"synthetic"}`), 0600); e != nil {
		t.Fatal(e)
	}
	if e := s.Reconcile(); e != nil {
		t.Fatalf("crash during own durableReplace permanently blocks every future daemon startup: %v", e)
	}
}

func TestUnknownOrUnsafeOrphanDoesNotBlockUnrelatedLogin(t *testing.T) {
	for _, kind := range []string{"unknown", "bad-temp-name", "linked-temp"} {
		t.Run(kind, func(t *testing.T) {
			s := testStore(t)
			a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
			activate(t, s, a)
			id := strings.Repeat("c", 32)
			dir := filepath.Join(s.path, "saved", id)
			if e := os.Mkdir(dir, 0700); e != nil {
				t.Fatal(e)
			}
			name := "native-future-file"
			if kind == "bad-temp-name" {
				name = ".login-tmp-not-an-owned-id"
			}
			path := filepath.Join(dir, name)
			if kind == "linked-temp" {
				path = filepath.Join(dir, ".login-tmp-"+strings.Repeat("d", 32))
				target := filepath.Join(s.path, "unrelated-private-file")
				if e := os.WriteFile(target, []byte("preserve"), 0600); e != nil {
					t.Fatal(e)
				}
				if e := os.Link(target, path); e != nil {
					t.Fatal(e)
				}
			} else if e := os.WriteFile(path, []byte("preserve"), 0600); e != nil {
				t.Fatal(e)
			}
			if e := s.Reconcile(); e != nil {
				t.Fatalf("orphan blocked healthy provider: %v", e)
			}
			if _, e := s.Runtime(ProviderCodex); e != nil {
				t.Fatalf("healthy native runtime refused: %v", e)
			}
			if _, e := os.Lstat(path); e != nil {
				t.Fatal("unknown or unsafe orphan was deleted")
			}
		})
	}
}

func TestKnownExhaustedInitialActivationRefusesBeforePending(t *testing.T) {
	for _, until := range []time.Time{{}, time.Now().Add(time.Hour)} {
		s := testStore(t)
		a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
		b := add(t, s, ProviderCodex, "B", codexCredential("b", "b"))
		rt := activate(t, s, a)
		r := snapshot(t, s)
		if _, e := s.UpdateAvailability(r.Revision, b.ID, AuthReady, true, until); e != nil {
			t.Fatal(e)
		}
		r = snapshot(t, s)
		if _, e := s.PrepareActivation(b.ID); !errors.Is(e, ErrIneligible) {
			t.Fatalf("known exhausted destination prepared: %v", e)
		}
		if _, e := s.BeginSwitch(r.Revision, ProviderCodex, b.ID, "manual", nil); !errors.Is(e, ErrIneligible) {
			t.Fatalf("known exhausted destination activated: %v", e)
		}
		if len(snapshot(t, s).Switches) != 0 {
			t.Fatal("known unusable target created a stop obligation")
		}
		if e := s.ValidateRuntime(rt); e != nil {
			t.Fatalf("healthy fleet admission changed: %v", e)
		}
		r = snapshot(t, s)
		if _, _, e := s.Relogin(r.Revision, b.ID, codexCredential("b", "fresh")); e != nil {
			t.Fatal(e)
		}
		activate(t, s, b)
	}
}
func TestAstraRemoveDoesNotOrphanUnknownNativeFile(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "old"))
	if e := os.WriteFile(filepath.Join(s.path, "saved", a.ID, "native-future-sidefile"), []byte("synthetic"), 0600); e != nil {
		t.Fatal(e)
	}
	r := snapshot(t, s)
	_, e := s.Remove(r.Revision, a.ID)
	if e == nil {
		t.Fatal("expected unknown-file refusal")
	}
	if len(snapshot(t, s).Logins) == 0 {
		t.Fatal("refusal removed membership first; next Reconcile is permanently blocked by unreferenced unknown file")
	}
	if snapshot(t, s).Revision != r.Revision {
		t.Fatal("refused removal mutated registry")
	}
	if e = s.Reconcile(); e != nil {
		t.Fatalf("unknown native file blocked later startup: %v", e)
	}
	raw, e := os.ReadFile(filepath.Join(s.path, "saved", a.ID, "native-future-sidefile"))
	if e != nil || string(raw) != "synthetic" {
		t.Fatal("refused removal changed unknown bytes")
	}
}
