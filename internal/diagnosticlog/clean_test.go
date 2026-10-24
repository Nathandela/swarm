package diagnosticlog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCleanExistingPreviewIsReadOnly(t *testing.T) {
	dir := privateTestDir(t)
	path := filepath.Join(dir, "daemon.log")
	data := strings.Repeat("x", MaxBytes+1)
	if err := os.WriteFile(path, []byte(data), 0640); err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	r, err := CleanExisting(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Before == nil || r.After == nil || r.EligibleBytes != int64(len(data)) || r.Changed {
		t.Fatalf("preview result = %+v", r)
	}
	if r.Before.Size() != beforeInfo.Size() || r.After.Size() != beforeInfo.Size() || !os.SameFile(r.Before, beforeInfo) || !os.SameFile(r.After, beforeInfo) {
		t.Fatalf("preview snapshots = before %v after %v, want original inode and size", r.Before, r.After)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != data || afterInfo.Mode().Perm() != 0640 || !os.SameFile(beforeInfo, afterInfo) {
		t.Fatal("preview changed content, mode, or inode")
	}
}

func TestCleanExistingApplyTruncatesSameInodeWithoutChangingMode(t *testing.T) {
	dir := privateTestDir(t)
	path := filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", MaxBytes+1)), 0640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	r, err := CleanExisting(path, true)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Changed || r.EligibleBytes != MaxBytes+1 || r.Before == nil || r.After == nil || r.Before.Size() != MaxBytes+1 || r.After.Size() != 0 {
		t.Fatalf("apply result = %+v", r)
	}
	if !os.SameFile(before, after) || after.Mode().Perm() != 0640 {
		t.Fatal("apply replaced inode or changed mode")
	}
}

func TestCleanExistingAtCapIsUnchanged(t *testing.T) {
	dir := privateTestDir(t)
	path := filepath.Join(dir, "daemon.log")
	data := strings.Repeat("x", MaxBytes)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := CleanExisting(path, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if r.Changed || r.EligibleBytes != 0 || r.Before.Size() != MaxBytes || r.After.Size() != MaxBytes || string(got) != data {
		t.Fatalf("at-cap result = %+v; bytes=%d", r, len(got))
	}
}

func TestCleanExistingApplyReassessesAfterPreview(t *testing.T) {
	dir := privateTestDir(t)
	path := filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", MaxBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	preview, err := CleanExisting(path, false)
	if err != nil || preview.EligibleBytes != MaxBytes+1 {
		t.Fatalf("preview = %+v, err=%v", preview, err)
	}
	if err := os.WriteFile(path, []byte("maintainer already trimmed"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := CleanExisting(path, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if r.Changed || r.EligibleBytes != 0 || string(got) != "maintainer already trimmed" {
		t.Fatalf("apply reused stale preview: result=%+v content=%q", r, got)
	}
}

func TestCleanExistingPreservesLiveAppendWriter(t *testing.T) {
	dir := privateTestDir(t)
	path := filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", MaxBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	writer, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	if _, err := CleanExisting(path, true); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteString("live\n"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "live\n" {
		t.Fatalf("live append after truncate = %q", got)
	}
}

func TestCleanExistingMissingDoesNotCreate(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "missing", "daemon.log")
	if _, err := CleanExisting(path, true); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing result error = %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing parent created: %v", err)
	}
}

func TestCleanExistingWriteOpenFailurePreservesFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file write permissions")
	}
	dir := privateTestDir(t)
	path := filepath.Join(dir, "daemon.log")
	data := strings.Repeat("x", MaxBytes+1)
	if err := os.WriteFile(path, []byte(data), 0400); err != nil {
		t.Fatal(err)
	}
	if _, err := CleanExisting(path, true); err == nil || errors.Is(err, ErrUnsafe) {
		t.Fatalf("apply error = %v, want ordinary write-open error", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != data || info.Mode().Perm() != 0400 {
		t.Fatal("failed apply changed protected log")
	}
}

func TestCleanExistingRejectsUnsafeTargetsWithoutChangingThem(t *testing.T) {
	dir := privateTestDir(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte(strings.Repeat("x", MaxBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "symlink")
	if err := os.Symlink(outside, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := CleanExisting(symlink, true); !errors.Is(err, ErrUnsafe) {
		t.Errorf("symlink error = %v, want ErrUnsafe", err)
	}
	if err := os.Link(outside, filepath.Join(dir, "hardlink")); err != nil {
		t.Fatal(err)
	}
	if _, err := CleanExisting(outside, true); !errors.Is(err, ErrUnsafe) {
		t.Errorf("hardlink error = %v, want ErrUnsafe", err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CleanExisting(fifo, true); !errors.Is(err, ErrUnsafe) {
		t.Errorf("FIFO error = %v, want ErrUnsafe", err)
	}
	if got, err := os.ReadFile(outside); err != nil || len(got) != MaxBytes+1 {
		t.Errorf("unsafe target changed: len=%d err=%v", len(got), err)
	}
}

func TestCleanExistingRejectsNoncanonicalPathWithoutChangingEitherTarget(t *testing.T) {
	state := privateTestDir(t)
	outside := privateTestDir(t)
	inside := filepath.Join(outside, "inside")
	if err := os.Mkdir(inside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, filepath.Join(state, "alias")); err != nil {
		t.Fatal(err)
	}
	cleanedTarget := filepath.Join(state, "daemon.log")
	resolvedTarget := filepath.Join(outside, "daemon.log")
	for path, data := range map[string]string{
		cleanedTarget:  "cleaned-spelling target",
		resolvedTarget: strings.Repeat("x", MaxBytes+1),
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := state + string(os.PathSeparator) + "alias" + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "daemon.log"
	if filepath.Clean(path) == path {
		t.Fatal("fixture path is unexpectedly canonical")
	}
	if _, err := CleanExisting(path, true); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("noncanonical path error = %v, want ErrUnsafe", err)
	}
	for path, want := range map[string]string{
		cleanedTarget:  "cleaned-spelling target",
		resolvedTarget: strings.Repeat("x", MaxBytes+1),
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("%s changed: length=%d want=%d", path, len(got), len(want))
		}
	}
}

func TestCleanExistingRejectsReplacedPathAndParent(t *testing.T) {
	t.Run("final path", func(t *testing.T) {
		dir := privateTestDir(t)
		path := filepath.Join(dir, "daemon.log")
		if err := os.WriteFile(path, []byte(strings.Repeat("x", MaxBytes+1)), 0600); err != nil {
			t.Fatal(err)
		}
		opened, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_APPEND|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		f := os.NewFile(uintptr(opened), path)
		defer func() { _ = f.Close() }()
		parent, err := os.Lstat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path, path+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := validateCleanTarget(path, f, parent); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("replaced final path error = %v, want ErrUnsafe", err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != "replacement" {
			t.Fatal("replacement file changed")
		}
	})

	t.Run("parent directory", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "state")
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "daemon.log")
		if err := os.WriteFile(path, []byte(strings.Repeat("x", MaxBytes+1)), 0600); err != nil {
			t.Fatal(err)
		}
		opened, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_APPEND|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		f := os.NewFile(uintptr(opened), path)
		defer func() { _ = f.Close() }()
		parent, err := os.Lstat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(dir, dir+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := validateCleanTarget(path, f, parent); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("replaced parent error = %v, want ErrUnsafe", err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != "replacement" {
			t.Fatal("replacement file changed")
		}
	})
}
