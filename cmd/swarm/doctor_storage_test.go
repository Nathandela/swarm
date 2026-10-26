package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestStorageScanIncludesManagedDataWithoutFollowingLinks(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"logins/provider", "journal", "unknown"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"logins/provider/history": "native history", "journal/control": "control", "unknown/work": "user work"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(filepath.Join(root, "journal/control"), filepath.Join(root, "journal/control-link")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "large"), make([]byte, 65536), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "external")); err != nil {
		t.Fatal(err)
	}
	r := scanStorage(root, 1000, time.Now().Add(time.Second))
	if r.partial || r.missing {
		t.Fatalf("unexpected scan status: %+v", r)
	}
	if r.native.logical != int64(len("native history")) || r.supervisor.logical != int64(len("control")) || r.other.logical != int64(len("user work")) {
		t.Fatalf("wrong or duplicated file totals: %+v", r)
	}
	if r.links != 1 {
		t.Fatalf("symlink exclusions = %d", r.links)
	}
	if r.totalAllocated() <= 0 {
		t.Fatal("allocated usage missing")
	}
	var allocated int64
	for _, rel := range []string{".", "logins", "logins/provider", "journal", "unknown", "logins/provider/history", "journal/control", "unknown/work", "external"} {
		info, err := os.Lstat(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		allocated += info.Sys().(*syscall.Stat_t).Blocks * 512
	}
	if r.totalAllocated() != allocated {
		t.Fatalf("allocated=%d, want %d including directory/link inodes and one hardlink target", r.totalAllocated(), allocated)
	}
}

func TestStorageSparseFilesAndUnreadableDirectory(t *testing.T) {
	root := t.TempDir()
	f, err := os.Create(filepath.Join(root, "sparse"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(64 << 20); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	r := scanStorage(root, 1000, time.Now().Add(time.Second))
	if r.partial || r.supervisor.logical != 64<<20 {
		t.Fatalf("sparse file logical usage: %+v", r)
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission denial")
	}
	denied := filepath.Join(root, "denied")
	if err = os.Mkdir(denied, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(denied, 0700) })
	r = scanStorage(root, 1000, time.Now().Add(time.Second))
	if !r.partial || storageFinding(r).Status != "warn" {
		t.Fatalf("unreadable directory hidden: %+v", r)
	}
}

func TestStorageScanLimitsAreExplicitAndAbsentRootIsUntouched(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "absent")
	r := scanStorage(missing, 1000, time.Now().Add(time.Second))
	if !r.missing || r.partial {
		t.Fatalf("missing root: %+v", r)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("scan created root: %v", err)
	}
	for _, n := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(root, n), []byte(n), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if r = scanStorage(root, 1, time.Now().Add(time.Second)); !r.partial {
		t.Fatal("entry limit reported exact total")
	}
	if r = scanStorage(root, 1000, time.Now().Add(-time.Second)); !r.partial {
		t.Fatal("deadline reported exact total")
	}
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if r = scanStorage(link, 1000, time.Now().Add(time.Second)); !r.partial {
		t.Fatal("root symlink followed")
	}
}

func TestStorageWarningIncludesManagedBytesAndNeverFails(t *testing.T) {
	r := storageUsage{native: storageBytes{allocated: storageWarnBytes + 1}, free: 2 * storageMinFree, freeKnown: true}
	f := storageFinding(r)
	if f.Status != "warn" || !strings.Contains(f.Detail, "managed") {
		t.Fatalf("managed usage not warned: %+v", f)
	}
	r.native.allocated = 0
	r.free = storageMinFree - 1
	if f = storageFinding(r); f.Status != "warn" {
		t.Fatalf("low space not warned: %+v", f)
	}
	r.free = 2 * storageMinFree
	if f = storageFinding(r); f.Status != "ok" {
		t.Fatalf("healthy scan: %+v", f)
	}
	r.partial = true
	if f = storageFinding(r); f.Status != "warn" || !strings.Contains(f.Detail, "partial") {
		t.Fatalf("partial scan hidden: %+v", f)
	}
}

func TestDoctorStorageFindingDoesNotChangeExitOrCreateState(t *testing.T) {
	dir := doctorEnv(t)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	var out, errs strings.Builder
	if code := runDoctor([]string{"--json"}, &out, &errs); code != 0 {
		t.Fatalf("doctor exit %d: %s", code, errs.String())
	}
	if !strings.Contains(out.String(), `"check": "storage"`) {
		t.Fatalf("storage finding absent: %s", out.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("doctor created state: %v", err)
	}
}
