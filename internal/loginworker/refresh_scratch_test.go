package loginworker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRefreshScratchOwnedCleanupPreservesUnknownPaths(t *testing.T) {
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(filepath.Join(root, "logins"), 0700); e != nil {
		t.Fatal(e)
	}
	f, e := openRefreshJob(root, "00112233445566778899aabbccddeeff", true)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	nonce := "00112233445566778899aabbccddeeff"
	if e = prepareRefreshScratch(f, nonce); e != nil {
		t.Fatal(e)
	}
	if e = f.root.Mkdir("home", 0700); e != nil {
		t.Fatal(e)
	}
	outside := filepath.Join(root, "keep")
	if e = os.WriteFile(outside, []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = f.root.Symlink(outside, "home/helper-link"); e != nil {
		t.Fatal(e)
	}
	file, e := f.root.OpenFile("home/oversize", os.O_CREATE|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	e = file.Truncate(maxRefreshScratchBytes + 1)
	_ = file.Close()
	if e != nil {
		t.Fatal(e)
	}
	if e = checkRefreshScratch(f, true); e != ErrInvalid {
		t.Fatalf("helper HOME quota not enforced: %v", e)
	}
	if e = f.write("unrelated.json", map[string]bool{"keep": true}); e != nil {
		t.Fatal(e)
	}
	if e = cleanupRefreshScratch(f); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(outside); e != nil {
		t.Fatal("scratch cleanup followed symlink")
	}
	if _, e = f.root.Lstat("unrelated.json"); e != nil {
		t.Fatal("scratch cleanup erased unowned job bytes")
	}
	if e = prepareRefreshScratch(f, nonce); e != nil {
		t.Fatal(e)
	}
	if _, e = f.root.Lstat("home"); !os.IsNotExist(e) {
		t.Fatal("next helper inherited HOME data")
	}
}
