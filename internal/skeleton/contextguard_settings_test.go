package skeleton

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestContextGuardSettings_DefaultsAndValidation(t *testing.T) {
	dir := t.TempDir()
	s := openContextGuardSettingsStore(dir)
	got, err := s.ContextGuardSettings()
	if err != nil {
		t.Fatalf("ContextGuardSettings: %v", err)
	}
	if got.SchemaVersion != 1 || got.Revision != 0 || got.AutoCompact.Enabled || got.AutoCompact.ThresholdPercent != 80 {
		t.Fatalf("default settings = %#v; want schema 1, revision 0, disabled/80", got)
	}
	for _, threshold := range []int{39, 40, 95, 96} {
		// A fresh store makes this a validation test rather than a stale-CAS test.
		s := openContextGuardSettingsStore(t.TempDir())
		_, err := s.SetContextGuardSettings(0, protocol.ContextGuardAutoCompact{ThresholdPercent: threshold})
		if (threshold >= 40 && threshold <= 95) != (err == nil) {
			t.Errorf("threshold %d error = %v; valid range is inclusive 40..95", threshold, err)
		}
	}
}

func TestContextGuardSettings_RoundTripRestartAndPrivateMode(t *testing.T) {
	dir := t.TempDir()
	s := openContextGuardSettingsStore(dir)
	want, err := s.SetContextGuardSettings(0, protocol.ContextGuardAutoCompact{Enabled: true, ThresholdPercent: 95})
	if err != nil {
		t.Fatalf("SetContextGuardSettings: %v", err)
	}
	if want.Revision != 1 {
		t.Fatalf("revision = %d, want 1", want.Revision)
	}
	fi, err := os.Stat(filepath.Join(dir, contextGuardSettingsFile))
	if err != nil {
		t.Fatalf("stat settings file: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("settings file mode = %o, want 0600", fi.Mode().Perm())
	}
	restarted := openContextGuardSettingsStore(dir)
	got, err := restarted.ContextGuardSettings()
	if err != nil {
		t.Fatalf("restarted ContextGuardSettings: %v", err)
	}
	if got != want {
		t.Fatalf("restart settings = %#v, want %#v", got, want)
	}
	// An identical update is an idempotent no-op, including its revision.
	again, err := restarted.SetContextGuardSettings(want.Revision, want.AutoCompact)
	if err != nil || again != want {
		t.Fatalf("identical update = %#v, %v; want %#v, nil", again, err, want)
	}
}

func TestContextGuardSettings_RejectsCorruptAndFutureFilesWithoutOverwrite(t *testing.T) {
	for _, raw := range []string{
		"not json",
		`{"schema_version":2,"revision":7,"auto_compact":{"enabled":true,"threshold_percent":80}}`,
		`{"schema_version":1,"schema_version":1,"revision":0,"auto_compact":{"enabled":false,"threshold_percent":80}}`,
		`{"schema_version":1,"revision":0,"auto_compact":{"enabled":false,"enabled":true,"threshold_percent":80}}`,
		`{"schema_version":1,"revision":0,"auto_compact":{"enabled":false,"threshold_percent":80,"threshold_percent":90}}`,
		`{"schema_version":1,"revision":0,"auto_compact":{"enabled":false,"threshold_percent":80},"unexpected":true}`,
		`{"schema_version":1,"revision":0,"auto_compact":{"enabled":false,"threshold_percent":80,"unexpected":true}}`,
		`{"schema_version":1,"revision":0,"auto_compact":{"enabled":false,"threshold_percent":80}} {}`,
	} {
		t.Run(raw[:1], func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, contextGuardSettingsFile)
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			s := openContextGuardSettingsStore(dir)
			got, err := s.ContextGuardSettings()
			if !errors.Is(err, protocol.ErrContextGuardSettingsUnavailable) {
				t.Fatalf("ContextGuardSettings error = %v, want stable unavailable", err)
			}
			if got.AutoCompact.Enabled || got.AutoCompact.ThresholdPercent != 80 {
				t.Fatalf("unavailable settings = %#v, want disabled/80", got)
			}
			if _, err := s.SetContextGuardSettings(0, protocol.ContextGuardAutoCompact{ThresholdPercent: 80}); !errors.Is(err, protocol.ErrContextGuardSettingsUnavailable) {
				t.Fatalf("SetContextGuardSettings error = %v, want stable unavailable", err)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || string(after) != raw {
				t.Fatalf("unavailable store overwrote evidence: got %q, err %v; want %q", after, readErr, raw)
			}
		})
	}
}

func TestContextGuardSettings_CASConcurrentAndFailedWriteDoesNotPublish(t *testing.T) {
	dir := t.TempDir()
	s := openContextGuardSettingsStore(dir)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, threshold := range []int{70, 90} {
		wg.Add(1)
		go func(threshold int) {
			defer wg.Done()
			_, err := s.SetContextGuardSettings(0, protocol.ContextGuardAutoCompact{ThresholdPercent: threshold})
			results <- err
		}(threshold)
	}
	wg.Wait()
	close(results)
	var success, stale int
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, protocol.ErrContextGuardSettingsStaleRevision) {
			stale++
		}
	}
	if success != 1 || stale != 1 {
		t.Fatalf("concurrent setters: success=%d stale=%d; want one of each", success, stale)
	}
	before, err := s.ContextGuardSettings()
	if err != nil {
		t.Fatal(err)
	}
	s.write = func(string, []byte) error { return errors.New("injected write failure") }
	if _, err := s.SetContextGuardSettings(before.Revision, protocol.ContextGuardAutoCompact{Enabled: true, ThresholdPercent: 85}); err == nil {
		t.Fatal("write failure accepted")
	}
	after, err := s.ContextGuardSettings()
	if err != nil || after != before {
		t.Fatalf("failed write published memory=%#v err=%v; want %#v", after, err, before)
	}
}

func TestContextGuardSettings_PersistenceFaultBoundaries(t *testing.T) {
	dir := t.TempDir()
	s := openContextGuardSettingsStore(dir)
	before, err := s.SetContextGuardSettings(0, protocol.ContextGuardAutoCompact{Enabled: true, ThresholdPercent: 80})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, contextGuardSettingsFile)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// A failure before rename leaves both the old file and memory intact.
	s.write = func(path string, data []byte) error {
		return writeContextGuardSettingsWithOps(path, data, contextGuardSettingsWriteOps{
			rename: func(string, string) error { return errors.New("injected rename failure") },
		})
	}
	if _, err := s.SetContextGuardSettings(before.Revision, protocol.ContextGuardAutoCompact{ThresholdPercent: 81}); err == nil {
		t.Fatal("pre-rename failure accepted")
	}
	got, err := s.ContextGuardSettings()
	if err != nil || got != before {
		t.Fatalf("pre-rename failure published memory %#v, %v; want %#v", got, err, before)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(original) {
		t.Fatalf("pre-rename failure changed file %q, %v; want %q", after, err, original)
	}

	// After rename, a directory-sync error leaves crash durability unknowable. The
	// store fails closed and does not publish memory or accept a later overwrite.
	s.write = func(path string, data []byte) error {
		return writeContextGuardSettingsWithOps(path, data, contextGuardSettingsWriteOps{
			syncDir: func(string) error { return errors.New("injected directory sync failure") },
		})
	}
	if _, err := s.SetContextGuardSettings(before.Revision, protocol.ContextGuardAutoCompact{ThresholdPercent: 81}); err == nil {
		t.Fatal("post-rename directory-sync failure accepted")
	}
	if _, err := s.ContextGuardSettings(); !errors.Is(err, protocol.ErrContextGuardSettingsUnavailable) {
		t.Fatalf("post-rename failure error = %v, want unavailable", err)
	}
	if _, err := s.SetContextGuardSettings(before.Revision, protocol.ContextGuardAutoCompact{ThresholdPercent: 82}); !errors.Is(err, protocol.ErrContextGuardSettingsUnavailable) {
		t.Fatalf("post-rename failure allowed overwrite: %v", err)
	}
}
