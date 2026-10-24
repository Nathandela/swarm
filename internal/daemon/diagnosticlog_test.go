package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/diagnosticlog"
	"github.com/Nathandela/swarm/internal/persist"
)

func TestDiagnosticLogTruncatesSameInodeAndKeepsInheritedWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	makeDiagnosticLogParentPrivate(t, path)
	a, err := openDaemonLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	b, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	before, _ := a.Stat()
	if err := a.Truncate(8<<20 + 1); err != nil {
		t.Fatal(err)
	}
	c, err := openDaemonLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) || after.Size() != 0 {
		t.Fatalf("log replaced or not trimmed: same=%v size=%d", os.SameFile(before, after), after.Size())
	}
	if _, err := a.WriteString("first\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.WriteString("second\n"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "first\nsecond\n" {
		t.Fatalf("inherited writes=%q err=%v", got, err)
	}
}

func TestDiagnosticLogRefusesUnsafeExistingTargets(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "keep")
			path := filepath.Join(dir, "daemon.log")
			makeDiagnosticLogParentPrivate(t, path)
			if err := os.WriteFile(target, []byte("valuable"), 0644); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(target, path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			f, err := openDaemonLog(path)
			if err == nil {
				_ = f.Close()
				t.Error("unsafe log accepted")
			}
			data, _ := os.ReadFile(target)
			info, _ := os.Stat(target)
			if string(data) != "valuable" || info.Mode().Perm() != 0644 {
				t.Fatal("unsafe target modified")
			}
		})
	}
}

func waitDiagnosticLogSettled(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * diagnosticlog.Interval)
	for {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() <= diagnosticlog.MaxBytes {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("log did not settle: %d", info.Size())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func makeDiagnosticLogParentPrivate(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticLogLiveShimMaintainsWithoutDaemonOrPTYOutput(t *testing.T) {
	cfg := daemonConfig(t)
	makeDiagnosticLogParentPrivate(t, cfg.LogPath)
	d := openDaemon(t, cfg)
	m, pid := launchAnnounce(t, d)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(cfg.LogPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	before, _ := f.Stat()
	for range 3 {
		if err := f.Truncate(diagnosticlog.MaxBytes + 1); err != nil {
			t.Fatal(err)
		}
		waitDiagnosticLogSettled(t, cfg.LogPath)
		if !processAlive(pid) || !processAlive(m.ShimPID) {
			t.Fatal("session did not survive absent daemon")
		}
	}
	after, _ := os.Stat(cfg.LogPath)
	if !os.SameFile(before, after) {
		t.Fatal("surviving shim replaced log inode")
	}
}

func TestDiagnosticLogDaemonMaintenanceStopsOnClose(t *testing.T) {
	cfg := daemonConfig(t)
	makeDiagnosticLogParentPrivate(t, cfg.LogPath)
	d := openDaemon(t, cfg)
	f, err := os.OpenFile(cfg.LogPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Truncate(diagnosticlog.MaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	waitDiagnosticLogSettled(t, cfg.LogPath)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(diagnosticlog.MaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	time.Sleep(diagnosticlog.Interval + 100*time.Millisecond)
	info, _ := f.Stat()
	if info.Size() != diagnosticlog.MaxBytes+1 {
		t.Fatal("daemon maintenance outlived joined Close")
	}
}

func TestDiagnosticLogFailedDaemonOpenStopsMaintenance(t *testing.T) {
	cfg := daemonConfig(t)
	makeDiagnosticLogParentPrivate(t, cfg.LogPath)
	original := scanStoreFn
	scanStoreFn = func(*persist.Store) ([]persist.Meta, error) { return nil, errors.New("injected scan failure") }
	t.Cleanup(func() { scanStoreFn = original })
	if d, err := Open(cfg); err == nil {
		_ = d.Close()
		t.Fatal("injected scan failure was ignored")
	}
	f, err := os.OpenFile(cfg.LogPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Truncate(diagnosticlog.MaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	time.Sleep(diagnosticlog.Interval + 100*time.Millisecond)
	info, _ := f.Stat()
	if info.Size() != diagnosticlog.MaxBytes+1 {
		t.Fatal("diagnostic maintenance survived failed daemon Open")
	}
}
