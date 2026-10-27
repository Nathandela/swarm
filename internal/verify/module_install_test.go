package verify_test

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"debug/buildinfo"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

type installSourceFile struct{ root, name string }

func (f installSourceFile) Path() string { return f.name }
func (f installSourceFile) Lstat() (os.FileInfo, error) {
	return os.Lstat(filepath.Join(f.root, f.name))
}
func (f installSourceFile) Open() (io.ReadCloser, error) {
	return os.Open(filepath.Join(f.root, f.name))
}

// A local proxy exercises the published-module install path without publishing.
func TestVersionedInstallFromModuleZipOutsideCheckout(t *testing.T) {
	root := repoRoot(t)
	tracked := exec.Command("git", "ls-files", "-c", "-o", "--exclude-standard", "-z")
	tracked.Dir = root
	raw, err := tracked.Output()
	if err != nil {
		t.Fatal(err)
	}
	var files []modzip.File
	for _, name := range strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00") {
		files = append(files, installSourceFile{root, name})
	}
	nonce := make([]byte, 8)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	patch := (binary.BigEndian.Uint64(nonce) & ((1 << 62) - 1)) + 1
	m := module.Version{Path: "github.com/Nathandela/swarm", Version: "v0.0." + strconv.FormatUint(patch, 10)}
	escaped, err := module.EscapePath(m.Path)
	if err != nil {
		t.Fatal(err)
	}
	proxy := t.TempDir()
	versions := filepath.Join(proxy, escaped, "@v")
	if err = os.MkdirAll(versions, 0700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(versions, m.Version+".zip")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	err = modzip.Create(f, m, files)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("module ZIP: %v; close: %v", err, closeErr)
	}
	zipped, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	present := map[string]bool{}
	for _, entry := range zipped.File {
		present[strings.TrimPrefix(entry.Name, m.Path+"@"+m.Version+"/")] = true
	}
	_ = zipped.Close()
	for _, name := range []string{"internal/thirdparty/bubbletea/tea.go", "internal/thirdparty/bubbletea/LICENSE", "internal/testutil/teatest/teatest.go", "internal/testutil/teatest/LICENSE", "THIRD_PARTY_NOTICES"} {
		if !present[name] {
			t.Fatalf("downloaded module ZIP omits %s", name)
		}
	}
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := json.Marshal(struct {
		Version string
		Time    time.Time
	}{m.Version, time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	for suffix, data := range map[string][]byte{".mod": mod, ".info": info} {
		if err = os.WriteFile(filepath.Join(versions, m.Version+suffix), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cache, err := exec.CommandContext(ctx, "go", "env", "GOMODCACHE").Output()
	if err != nil || strings.TrimSpace(string(cache)) == "" {
		t.Fatalf("resolve shared module cache: %v %q", err, cache)
	}
	prior, err := filepath.Glob(filepath.Join(strings.TrimSpace(string(cache)), "cache", "download", escaped, "@v", m.Version+".*"))
	if err != nil || len(prior) != 0 {
		t.Fatalf("synthetic module version is not fresh: %v %v", prior, err)
	}
	bin := t.TempDir()
	cmd := exec.CommandContext(ctx, "go", "install", m.Path+"/cmd/swarm@"+m.Version)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=", "GOBIN="+bin,
		"GONOSUMDB="+m.Path, "GOPROXY="+(&url.URL{Scheme: "file", Path: proxy}).String()+",https://proxy.golang.org,direct")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("outside-checkout versioned install: %v\n%s", err, output)
	}
	name := "swarm"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable := filepath.Join(bin, name)
	build, err := buildinfo.ReadFile(executable)
	if err != nil || build.Main.Path != m.Path || build.Main.Version != m.Version || build.Main.Replace != nil {
		t.Fatalf("installed binary is not the downloaded module: %+v, %v", build, err)
	}
	if output, err := exec.CommandContext(ctx, executable, "version").CombinedOutput(); err != nil || !strings.HasPrefix(string(output), "swarm ") {
		t.Fatalf("installed binary: %v %q", err, output)
	}
	t.Logf("installed %s@%s from module ZIP outside checkout", m.Path, m.Version)
}
