package loginworker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// This native test executable transports synthetic loopback overrides after the
// production scrub. Production binaries contain neither this dispatch nor sidecar.
type refreshFixtureTransport struct {
	Target, SHA256, Version string
	Prefix, Environment     []string
}

func init() {
	executable, err := os.Executable()
	if err != nil || filepath.Base(executable) != "codex-refresh-fixture" {
		return
	}
	path := executable + ".json"
	info, err := os.Lstat(path)
	if err != nil || !privateInfo(info, false) || info.Size() > 16<<10 {
		os.Exit(91)
	}
	raw, err := os.ReadFile(path)
	var transport refreshFixtureTransport
	if err != nil || json.Unmarshal(raw, &transport) != nil || !filepath.IsAbs(transport.Target) {
		os.Exit(92)
	}
	file, err := os.Open(transport.Target)
	if err != nil {
		os.Exit(93)
	}
	hash := sha256.New()
	count, err := io.Copy(hash, io.LimitReader(file, 1<<30+1))
	_ = file.Close()
	if err != nil || count > 1<<30 || hex.EncodeToString(hash.Sum(nil)) != transport.SHA256 {
		os.Exit(94)
	}
	if len(os.Args) == 2 && os.Args[1] == "--version" && transport.Version != "" {
		fmt.Println("codex-cli " + transport.Version)
		os.Exit(0)
	}
	args := append([]string{transport.Target}, transport.Prefix...)
	if transport.Version == "" {
		args = append(args, os.Args[1:]...)
	}
	if err := syscall.Exec(transport.Target, args, append(os.Environ(), transport.Environment...)); err != nil {
		os.Exit(95)
	}
}

func newRefreshFixtureTransport(t *testing.T, dir string, transport refreshFixtureTransport) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	copyFile := func(path string) {
		t.Helper()
		input, err := os.Open(executable)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = input.Close() }()
		output, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
		if err != nil {
			t.Fatal(err)
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		if copyErr != nil || closeErr != nil {
			t.Fatalf("copy native test transport: %v %v", copyErr, closeErr)
		}
	}
	if transport.SHA256 == "" {
		file, err := os.Open(transport.Target)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		_ = file.Close()
		if copyErr != nil {
			t.Fatal(copyErr)
		}
		transport.SHA256 = hex.EncodeToString(hash.Sum(nil))
	}
	path := filepath.Join(dir, "codex-refresh-fixture")
	copyFile(path)
	raw, err := json.Marshal(transport)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".json", raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
