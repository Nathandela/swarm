package main

// Main-level RED test for the swarm-relay binary (R-REL.9). The binary parses
// argv, reads one config file, and boots the relay. This exercises the binary's
// own wiring (argv -> run) and its clean error handling; the full config-boot
// round-trip is covered in-package by relay.TestRelay_BootsFromConfigLocalhost.

import (
	"context"
	"path/filepath"
	"testing"
)

// TestMain_RunRejectsMissingConfig asserts a missing config path is a clean
// error, not a panic — the binary fails closed when it cannot read its config.
func TestMain_RunRejectsMissingConfig(t *testing.T) {
	err := run(context.Background(), []string{"--config", filepath.Join(t.TempDir(), "nope.conf")})
	if err == nil {
		t.Fatalf("run with a missing config file returned nil, want an error")
	}
}

// TestMain_RunRequiresConfigFlag asserts the binary refuses to boot without a
// config file rather than silently starting on unspecified defaults.
func TestMain_RunRequiresConfigFlag(t *testing.T) {
	if err := run(context.Background(), nil); err == nil {
		t.Fatalf("run without --config returned nil, want a usage error")
	}
}
