package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"
)

func TestClaudePrimaryFixtureRealPipesAndNoAutomaticInput(t *testing.T) {
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []*os.File{inR, inW, outR, outW, errR, errW} {
		defer func() { _ = f.Close() }()
	}
	result := make(chan int, 1)
	joined := false
	t.Cleanup(func() {
		_ = inR.Close()
		if !joined {
			select {
			case <-result:
			case <-time.After(time.Second):
				t.Error("fixture goroutine did not join")
			}
		}
	})
	go func() {
		result <- run([]string{"--claude-primary-fixture", "--session-id=8e4c6620-bfdd-43c3-93de-e69a2f97a01f"}, inR, outW, errW)
	}()
	// Deadlines bound bad fixture implementations without changing its 12s cap.
	_ = outR.SetReadDeadline(time.Now().Add(time.Second))
	_ = errR.SetReadDeadline(time.Now().Add(time.Second))
	var init struct {
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		SessionID string `json:"session_id"`
		Stdio     *struct {
			StdinTTY  bool `json:"stdin_tty"`
			StdoutTTY bool `json:"stdout_tty"`
			StderrTTY bool `json:"stderr_tty"`
		} `json:"fixture_stdio"`
	}
	if err := json.NewDecoder(outR).Decode(&init); err != nil {
		t.Fatal(err)
	}
	if init.Type != "system" || init.Subtype != "init" || init.SessionID != "8e4c6620-bfdd-43c3-93de-e69a2f97a01f" || init.Stdio == nil || init.Stdio.StdinTTY || init.Stdio.StdoutTTY || init.Stdio.StderrTTY {
		t.Fatalf("wrong init: %+v", init)
	}
	var telemetry struct {
		Fixture   string `json:"fixture"`
		StdinTTY  bool   `json:"stdin_tty"`
		StdoutTTY bool   `json:"stdout_tty"`
		StderrTTY bool   `json:"stderr_tty"`
	}
	if err := json.NewDecoder(errR).Decode(&telemetry); err != nil {
		t.Fatal(err)
	}
	if telemetry.Fixture != "claude-primary" || telemetry.StdinTTY || telemetry.StdoutTTY || telemetry.StderrTTY {
		t.Fatalf("not stdio: %+v", telemetry)
	}
	select {
	case code := <-result:
		joined = true
		t.Fatalf("did not block for owner input/Stop: %d", code)
	default:
	}
	_ = inW.Close()
	select {
	case code := <-result:
		joined = true
		if code != 0 {
			t.Fatalf("EOF code=%d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("EOF did not release fixture")
	}
}

func TestClaudePrimaryFixtureRejectsExtraArgsAndUserBytes(t *testing.T) {
	for _, args := range [][]string{
		{"--claude-primary-fixture"},
		{"--claude-primary-fixture", "--session-id=bad"},
		{"--claude-primary-fixture", "--session-id=8e4c6620-bfdd-43c3-93de-e69a2f97a01f", "automatic user message"},
	} {
		var out, stderr bytes.Buffer
		if code := run(args, bytes.NewReader(nil), &out, &stderr); code != 2 || out.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("malformed fixture args accepted: %d", code)
		}
	}
	if code := run([]string{"--claude-primary-fixture", "--session-id=8e4c6620-bfdd-43c3-93de-e69a2f97a01f"}, bytes.NewBufferString("user"), io.Discard, io.Discard); code != 3 {
		t.Fatalf("unexpected user input accepted: %d", code)
	}
}
