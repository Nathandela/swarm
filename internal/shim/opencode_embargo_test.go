package shim

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeEmbargoBlocksInputUntilGenerationRelease(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{SessionDir: dir, Argv: []string{"/native/opencode", "--model", "opencode/model"}, OpenCode: &OpenCodeConfig{
		RootID: "ses_" + strings.Repeat("a", 32), Generation: strings.Repeat("b", 32), BindingFile: filepath.Join(dir, OpenCodeBindingFile), EmbargoToken: strings.Repeat("c", 64)}}
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
	if n, err := p.WriteInput([]byte("owner")); n != 0 || !errors.Is(err, errOpenCodeStarting) {
		t.Fatal("owner input escaped")
	}
	if n, err := p.WriteControl([]byte("control")); n != 0 || !errors.Is(err, errOpenCodeStarting) {
		t.Fatal("control input escaped")
	}
	if err := p.submitMessage([]byte("message"), 0); !errors.Is(err, errOpenCodeStarting) {
		t.Fatal("submit escaped")
	}
	if n, err := p.Write([]byte("terminal-query-reply")); err != nil || n == 0 {
		t.Fatal("terminal negotiation was blocked")
	}
	if e.release(strings.Repeat("d", 32), cfg.OpenCode.EmbargoToken) == nil {
		t.Fatal("wrong generation released input")
	}
	if e.release(cfg.OpenCode.Generation, strings.Repeat("e", 64)) == nil {
		t.Fatal("wrong authority released input")
	}
	if err := e.release(cfg.OpenCode.Generation, cfg.OpenCode.EmbargoToken); err != nil {
		t.Fatal(err)
	}
	again, err := openInputEmbargo(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer again.close()
	if again.held.Load() {
		t.Fatal("durable release not recovered")
	}
	if _, err := os.Stat(filepath.Join(dir, AccountInputEmbargoFile)); !os.IsNotExist(err) {
		t.Fatal("OpenCode reused account embargo state")
	}
	if err := p.submitMessage([]byte("after"), 0); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCodeRuntimeRejectsUngatedNativeStartup(t *testing.T) {
	dir := t.TempDir()
	base := Config{SessionDir: dir, Argv: []string{"/native/opencode"}, OpenCode: &OpenCodeConfig{RootID: "ses_" + strings.Repeat("a", 32), Generation: strings.Repeat("b", 32), BindingFile: filepath.Join(dir, OpenCodeBindingFile), EmbargoToken: strings.Repeat("c", 64)}}
	for _, args := range [][]string{{"--prompt", "hello"}, {"--prompt=hello"}, {"--session", "ses_" + strings.Repeat("a", 32)}, {"--continue"}, {"--fork"}, {"--port", "0"}} {
		cfg := base
		cfg.Argv = append([]string{"/native/opencode"}, args...)
		if ValidateOpenCodeConfig(cfg) == nil {
			t.Fatalf("native startup bypass accepted: %q", args)
		}
	}
	if err := ValidateOpenCodeConfig(base); err != nil {
		t.Fatal(err)
	}
}
