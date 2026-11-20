package discord

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/voice"
)

func TestDispatcherCallPreflightRedactsFailures(t *testing.T) {
	dir := t.TempDir()
	keyDir := filepath.Join(dir, "speech")
	if err := voice.SaveOpenAIKey(keyDir, strings.NewReader("private-key")); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\necho private-helper-output >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	d := &dispatcherCalls{ctx: context.Background(), t: &teammateConnector{service: &Service{Config: Config{Calls: &DispatcherCallsConfig{HelperPath: helper, SpeechStateDir: keyDir}}}}}
	if got := d.preflight(); got != "voice helper check failed; verify helper installation and Node 22.12+" || strings.Contains(got, "private") {
		t.Fatalf("helper failure = %q", got)
	}
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if got := d.preflight(); got != "" {
		t.Fatalf("healthy preflight = %q", got)
	}
}

func TestDispatcherCallPreflightDistinguishesKeyFailure(t *testing.T) {
	d := &dispatcherCalls{ctx: context.Background(), t: &teammateConnector{service: &Service{Config: Config{Calls: &DispatcherCallsConfig{SpeechStateDir: filepath.Join(t.TempDir(), "missing")}}}}}
	if got := d.preflight(); got != "speech key is unavailable; run swarm voice setup" {
		t.Fatalf("missing key = %q", got)
	}
}

func TestDispatcherStopCallSuspendsAutostartAndCancelsActiveCall(t *testing.T) {
	f := newDispatcherCallFixture(t, false)
	f.begin()
	f.calls.mu.Lock()
	active := f.calls.active
	f.calls.mu.Unlock()
	f.calls.stopCall()
	f.calls.mu.Lock()
	_, permitted := f.calls.permittedLocked()
	f.calls.mu.Unlock()
	status := f.calls.status()
	if active == nil || active.ctx.Err() == nil || permitted || status != "calls suspended until connector restart" {
		t.Fatalf("active=%v permitted=%t status=%q", active != nil, permitted, status)
	}
}
