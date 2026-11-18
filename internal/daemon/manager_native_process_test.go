package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
)

func TestManagerNativeGenerationIsPhysicalAndFailsClosed(t *testing.T) {
	state := t.TempDir()
	id := "manager"
	dir := filepath.Join(state, id)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	pid := os.Getpid()
	start, err := procStartTimeFn(pid)
	if err != nil {
		t.Fatal(err)
	}
	meta := persist.Meta{ID: id, AgentType: "codex", ShimPID: pid, ShimStartTime: start}
	d := &Daemon{cfg: Config{StateDir: state}, sessions: map[string]*session{id: {meta: meta}}}
	info := shim.NativeProcessInfo{SchemaVersion: shim.NativeWriterSchemaVersion, Generation: "0123456789abcdef0123456789abcdef", Provider: "codex", ShimPID: pid, ShimStartTime: start, PID: pid, StartTime: start, BackendPID: pid, BackendStartTime: start}
	save := func(value any, name string) {
		t.Helper()
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save(info, shim.NativeProcessFile)
	save(shim.BackendInfo{PID: pid, StartedAtMS: start}, shim.BackendFile)
	first, err := d.SessionNativeGeneration(id)
	if err != nil || first != info.Generation {
		t.Fatalf("generation: %q %v", first, err)
	}
	second, err := d.SessionNativeGeneration(id)
	if err != nil || second != first {
		t.Fatal("reading/reconnecting changed native process generation")
	}
	info.StartTime++
	save(info, shim.NativeProcessFile)
	if _, err := d.SessionNativeGeneration(id); err == nil {
		t.Fatal("reused/foreign native PID inherited authority")
	}
	info.StartTime = start
	save(info, shim.NativeProcessFile)
	_ = os.Chmod(filepath.Join(dir, shim.NativeProcessFile), 0644)
	if _, err := d.SessionNativeGeneration(id); err == nil {
		t.Fatal("public native process custody was accepted")
	}
}
