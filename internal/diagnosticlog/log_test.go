package diagnosticlog

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestMaintenanceSettlesBusyIntervalsAndJoins(t *testing.T) {
	path := filepath.Join(privateTestDir(t), "daemon.log")
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var failures atomic.Int32
	stop, err := start(path, f, 5*time.Millisecond, func(error) { failures.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for range 3 {
		if err := f.Truncate(MaxBytes + 1); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(time.Second)
		for {
			info, err := f.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if info.Size() == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("maintainer did not settle log")
			}
			time.Sleep(time.Millisecond)
		}
	}
	stop()
	if err := f.Truncate(MaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	info, _ := f.Stat()
	if info.Size() != MaxBytes+1 {
		t.Fatal("maintenance continued after joined stop")
	}
	if failures.Load() != 0 {
		t.Fatalf("maintenance errors=%d", failures.Load())
	}
}

func TestMaintenanceErrorsAreReportedWithoutStoppingOwner(t *testing.T) {
	path := filepath.Join(privateTestDir(t), "daemon.log")
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Truncate(MaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	readonly, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readonly.Close() }()
	errors := make(chan error, 10)
	stop, err := start(path, readonly, 5*time.Millisecond, func(err error) {
		select {
		case errors <- err:
		default:
		}
	})
	if err != nil {
		t.Fatalf("valid owner refused over maintenance error: %v", err)
	}
	defer stop()
	select {
	case <-errors:
	case <-time.After(time.Second):
		t.Fatal("truncate failure was hidden")
	}
	info, _ := f.Stat()
	if info.Size() != MaxBytes+1 {
		t.Fatal("read-only descriptor unexpectedly truncated log")
	}
}

func TestMaintenanceRejectsRedirectedStderrAndUnsafeDescriptors(t *testing.T) {
	dir := privateTestDir(t)
	path := filepath.Join(dir, "daemon.log")
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	other, err := Open(filepath.Join(dir, "unrelated"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	if stop, err := Start(path, other); err == nil {
		stop()
		t.Fatal("maintainer accepted unrelated stderr")
	}
	if err := os.Link(path, filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	if stop, err := Start(path, f); err == nil {
		stop()
		t.Fatal("maintainer accepted multiply linked descriptor")
	}
}

func TestMaintenanceRechecksPathBeforeTruncation(t *testing.T) {
	dir := privateTestDir(t)
	path := filepath.Join(dir, "daemon.log")
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Truncate(MaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".replaced"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := trim(path, f); err == nil {
		t.Fatal("maintenance accepted a replaced path")
	}
	old, _ := f.Stat()
	raw, _ := os.ReadFile(path)
	if old.Size() != MaxBytes+1 || string(raw) != "keep" {
		t.Fatal("maintenance modified a mismatched path")
	}
}

func TestOpenHardensOwnedFileAndRejectsFIFOAndForeignOwner(t *testing.T) {
	dir := privateTestDir(t)
	path := filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(path, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := f.Stat()
	f.Close()
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if f, err := Open(fifo); err == nil {
		f.Close()
		t.Fatal("accepted FIFO")
	}
	foreign, err := os.Open("/etc/hosts")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foreign.Close() }()
	foreignInfo, err := foreign.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if foreignInfo.Sys().(*syscall.Stat_t).Uid == uint32(os.Geteuid()) {
		t.Skip("foreign-owner fixture belongs to test user")
	}
	if err := validate(path, foreign); err == nil || !strings.Contains(err.Error(), "unsafe descriptor") {
		t.Fatalf("foreign descriptor=%v", err)
	}
}

func privateTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}
