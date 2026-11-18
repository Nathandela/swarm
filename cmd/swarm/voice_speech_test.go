package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/voice"
)

func TestVoiceSetupAndInvalidCommands(t *testing.T) {
	state := t.TempDir()
	t.Setenv("SWARM_DAEMON_STATE", state)
	input, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	if _, err := input.WriteString("fixture-key\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	if code := dispatchSpeech([]string{"setup"}, input, &out, &errout); code != 0 {
		t.Fatalf("%d: %s", code, &errout)
	}
	key, err := voice.LoadOpenAIKey(filepath.Join(state, "voice"))
	if err != nil || key != "fixture-key" {
		t.Fatal("missing saved key", err)
	}
	for _, args := range [][]string{{"live", "fixture-key"}, {"setup", "--api-key", "fixture-key"}, {"bogus"}} {
		if code := dispatchVoice(args, &out, &errout); code == 0 {
			t.Fatalf("accepted %v", args)
		}
	}
	if strings.Contains(out.String()+errout.String(), "fixture-key") {
		t.Fatal("credential exposed")
	}
}

func TestTerminalTranscriptPreservesLinesAndRemovesControls(t *testing.T) {
	if got := terminalTranscript("one\r\x1b[1m\n\técoute\u009b"); got != "one  [1m\n\técoute " {
		t.Fatal("unsafe terminal transcript", got)
	}
}
