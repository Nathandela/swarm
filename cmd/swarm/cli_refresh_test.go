package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/skeleton"
)

func TestCLIRefreshReportDoesNotCreateState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	var out, stderr bytes.Buffer
	if code := runCLIRefresh(nil, dir, &out, &stderr); code != 0 {
		t.Fatalf("code %d: %s", code, &stderr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("read-only report created state: %v", err)
	}
	if !strings.Contains(out.String(), "Legacy") {
		t.Fatalf("missing unknown-session explanation: %s", &out)
	}
}

func TestCLIRefreshPolicyIsIndependentOfAuth(t *testing.T) {
	dir := t.TempDir()
	for _, mode := range []string{"off", "on"} {
		var out, stderr bytes.Buffer
		if code := runCLIRefresh([]string{"--auto", mode, "--json"}, dir, &out, &stderr); code != 0 {
			t.Fatalf("%s code %d: %s", mode, code, &stderr)
		}
		if skeleton.CLIRefreshDisabled(dir) != (mode == "off") {
			t.Fatalf("policy did not change to %s", mode)
		}
		if skeleton.AuthWatchDisabled(dir) {
			t.Fatal("CLI policy changed auth policy")
		}
	}
}

func TestCLIRefreshRejectsInvalidArgumentsBeforeWriting(t *testing.T) {
	for _, args := range [][]string{{"--auto", "maybe"}, {"--auto", "off", "extra"}, {"--force"}} {
		dir := filepath.Join(t.TempDir(), "absent")
		var out, stderr bytes.Buffer
		if code := runCLIRefresh(args, dir, &out, &stderr); code != 2 {
			t.Fatalf("%v: code %d", args, code)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("%v modified policy", args)
		}
	}
}
