package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/upgrade"
)

func TestReleaseManifestRequiresExplicitTagAndEmitsIt(t *testing.T) {
	config, err := os.ReadFile(filepath.Join("..", "..", ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "go run ./scripts/releasemanifest --tag '{{ .Tag }}' --out compat.json") {
		t.Fatal("GoReleaser before hook must pass its release tag explicitly")
	}
	without := filepath.Join(t.TempDir(), "missing.json")
	cmd := exec.Command("go", "run", ".", "--out", without)
	cmd.Env = append(withoutTagEnv(), "GORELEASER_CURRENT_TAG=")
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "release tag is required") {
		t.Fatalf("missing tag accepted: err=%v output=%q", err, out)
	}
	with := filepath.Join(t.TempDir(), "compat.json")
	cmd = exec.Command("go", "run", ".", "--tag", "v0.14.0", "--out", with)
	cmd.Env = append(withoutTagEnv(), "GORELEASER_CURRENT_TAG=v0.0.1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("explicit release tag refused: %v output=%q", err, out)
	}
	data, err := os.ReadFile(with)
	if err != nil {
		t.Fatal(err)
	}
	var card upgrade.CompatManifest
	if err := json.Unmarshal(data, &card); err != nil || card.Version != "v0.14.0" || card.Shimwire == 0 || card.Protocol == 0 || card.Schema == 0 {
		t.Fatalf("compat card = %+v, decode=%v", card, err)
	}
}

func withoutTagEnv() []string {
	var out []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GORELEASER_CURRENT_TAG=") {
			out = append(out, entry)
		}
	}
	return out
}
