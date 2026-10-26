package shim

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/logins"
)

func TestInputEmbargoBlocksEveryUserInputPlaneUntilDurableRelease(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{SessionDir: dir, LoginRuntime: &logins.Runtime{}, InputEmbargo: "incident", InputEmbargoToken: strings.Repeat("a", 32)}
	e, err := openInputEmbargo(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.close()
	f, err := os.Create(filepath.Join(dir, "input"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	p := ptyWriter{f: f, embargo: e}
	if n, err := p.WriteInput([]byte("typed")); n != 0 || !errors.Is(err, errLoginSwitching) {
		t.Fatal("raw input escaped embargo")
	}
	if n, err := p.WritePaste([]byte("pasted")); n != 0 || !errors.Is(err, errLoginSwitching) {
		t.Fatal("pasted input escaped embargo")
	}
	if n, err := p.WriteControl([]byte("control")); n != 0 || !errors.Is(err, errLoginSwitching) {
		t.Fatal("control input escaped embargo")
	}
	if err := p.submitMessage([]byte("message"), 0); !errors.Is(err, errLoginSwitching) {
		t.Fatal("submitted input escaped embargo")
	}
	if info, _ := f.Stat(); info.Size() != 0 {
		t.Fatal("refused input wrote bytes")
	}
	if err := e.release("other", cfg.InputEmbargoToken); err == nil {
		t.Fatal("wrong incident released embargo")
	}
	if err := e.release(cfg.InputEmbargo, strings.Repeat("b", 32)); err == nil {
		t.Fatal("wrong authority released embargo")
	}
	reopened, err := openInputEmbargo(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.held.Load() {
		t.Fatal("restart lost embargo")
	}
	reopened.close()
	if err := e.release(cfg.InputEmbargo, cfg.InputEmbargoToken); err != nil {
		t.Fatal(err)
	}
	if err := e.release(cfg.InputEmbargo, cfg.InputEmbargoToken); err != nil {
		t.Fatal("release not idempotent")
	}
	data, err := os.ReadFile(filepath.Join(dir, InputEmbargoFile))
	if err != nil {
		t.Fatal(err)
	}
	var record inputEmbargoRecord
	if json.Unmarshal(data, &record) != nil || record.Held {
		t.Fatal("release not persisted")
	}
	if strings.Contains(string(data), cfg.InputEmbargoToken) {
		t.Fatal("authority persisted in public state")
	}
	if err := p.submitMessage([]byte("new request"), 0); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(f.Name())
	if string(data) != "new request\r" {
		t.Fatal("release replayed or damaged input")
	}
}

func TestInputEmbargoRejectsUnsafePersistedRecord(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "record")
	if err := os.WriteFile(outside, []byte(`{"schema_version":1,"incident_id":"incident","held":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, InputEmbargoFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := openInputEmbargo(Config{SessionDir: dir, LoginRuntime: &logins.Runtime{}, InputEmbargo: "incident", InputEmbargoToken: strings.Repeat("a", 32)}); err == nil {
		t.Fatal("symlink released input")
	}
}
