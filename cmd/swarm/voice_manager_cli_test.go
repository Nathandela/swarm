package main

import (
	"bytes"
	"encoding/json"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/voice"
	"os"
	"path/filepath"
	"testing"
)

func TestManagerSetupOwnsAStableConversationWithoutSpeech(t *testing.T) {
	root := t.TempDir()
	t.Setenv(daemon.EnvStateDir, root)
	t.Setenv(daemon.EnvSocket, filepath.Join(root, "daemon.sock"))
	work := t.TempDir()
	args := []string{"voice", "setup", "--manager", "codex", "--dir", work}
	var out, errout bytes.Buffer
	if code := dispatch(args, &out, &errout); code != 0 {
		t.Fatalf("configure returned %d: %s", code, &errout)
	}
	dir := filepath.Join(root, "voice")
	want, err := voice.LoadConfig(dir)
	if err != nil || want.Provider != "codex" || want.WorkDir != work || want.ConversationID == "" {
		t.Fatalf("invalid settings %+v: %v", want, err)
	}
	if code := dispatch(args, &out, &errout); code != 0 {
		t.Fatalf("repeat setup: %s", &errout)
	}
	got, _ := voice.LoadConfig(dir)
	if got != want {
		t.Fatal("repeat changed manager binding")
	}
	args[3] = "claude"
	if code := dispatch(args, &out, &errout); code == 0 {
		t.Fatal("implicit replacement accepted")
	}
	got, _ = voice.LoadConfig(dir)
	if got != want {
		t.Fatal("refusal changed manager binding")
	}
	if _, err := os.Stat(filepath.Join(dir, "openai.key")); !os.IsNotExist(err) {
		t.Fatal("text setup touched speech credential")
	}
}

func TestVoiceReceiptRequiresAnOperation(t *testing.T) {
	var o, e bytes.Buffer
	if code := dispatch([]string{"voice", "receipt"}, &o, &e); code != misuseExit {
		t.Fatalf("exit=%d err=%s", code, &e)
	}
	if json.Valid(o.Bytes()) {
		t.Fatal("reported a receipt without an operation")
	}
}
