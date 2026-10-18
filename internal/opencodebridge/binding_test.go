package opencodebridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readerFixture(t *testing.T) (string, Binding) {
	t.Helper()
	dir, err := os.MkdirTemp("/private/tmp", "ocb-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	b := Binding{Version: 1, WorkerID: "worker", SessionInstance: strings.Repeat("a", 32), RootID: "ses_" + strings.Repeat("b", 32), Directory: "/project with space/ü", BackendGeneration: strings.Repeat("c", 32), SocketPath: filepath.Join(dir, "oc.sock"), Token: strings.Repeat("d", 64), Mode: "create"}
	return filepath.Join(dir, "binding.json"), b
}

func writeReaderBinding(t *testing.T, path string, b Binding) []byte {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestReadBindingPrivateCustodyAndStrictJSON(t *testing.T) {
	path, b := readerFixture(t)
	raw := writeReaderBinding(t, path, b)
	got, err := ReadBinding(path)
	if err != nil || got != b {
		t.Fatalf("private Unicode profile: %+v %v", got, err)
	}
	for name, body := range map[string][]byte{
		"duplicate":    append([]byte(`{"version":1,`), raw[1:]...),
		"case alias":   append([]byte(`{"Version":1,`), raw[1:]...),
		"unknown":      append([]byte(`{"unknown":1,`), raw[1:]...),
		"missing bool": []byte(strings.Replace(string(raw), `,"initialPrompt":false`, "", 1)),
		"trailing":     append(append([]byte(nil), raw...), []byte(` {}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadBinding(path); err == nil {
				t.Fatal("ambiguous private binding accepted")
			}
		})
	}
	writeReaderBinding(t, path, b)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBinding(path); err == nil {
		t.Fatal("publicly readable credential accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(path), "linked")
	if err := os.Link(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBinding(path); err == nil {
		t.Fatal("multiply-linked credential accepted")
	}
}

func TestReadBindingRejectsUnqualifiedIdentityAndPaths(t *testing.T) {
	for name, change := range map[string]func(*Binding){
		"unknown root":             func(b *Binding) { b.RootID = "latest" },
		"bad generation":           func(b *Binding) { b.BackendGeneration = "next" },
		"bad instance":             func(b *Binding) { b.SessionInstance = "old" },
		"resume without timestamp": func(b *Binding) { b.Mode = "resume" },
		"create with timestamp":    func(b *Binding) { b.NativeCreatedAtMS = 1 },
		"unsafe timestamp":         func(b *Binding) { b.Mode = "resume"; b.NativeCreatedAtMS = 1 << 54 },
		"relative directory":       func(b *Binding) { b.Directory = "project" },
		"control directory":        func(b *Binding) { b.Directory = "/project\nname" },
		"foreign socket parent":    func(b *Binding) { b.SocketPath = "/private/tmp/other.sock" },
		"short token":              func(b *Binding) { b.Token = "secret" },
	} {
		t.Run(name, func(t *testing.T) {
			path, b := readerFixture(t)
			change(&b)
			writeReaderBinding(t, path, b)
			if _, err := ReadBinding(path); err == nil {
				t.Fatal("invalid private binding accepted")
			}
		})
	}
}
