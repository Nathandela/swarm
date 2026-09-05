package main

import (
	"bytes"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/remote/relaycfg"
)

func TestRemoteInitRequiresAndPersistsRelayNamespace(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(daemon.EnvStateDir, dir)
	var stdout, stderr bytes.Buffer
	if exit := runRemoteInit([]string{"--relay-url", "wss://s.example.workers.dev"}, &stdout, &stderr); exit == 0 {
		t.Fatal("remote init accepted --relay-url without --relay-namespace")
	}
	if _, found, err := relaycfg.Load(dir); err != nil || found {
		t.Fatalf("rejected init changed relay config: found=%v err=%v", found, err)
	}

	stdout.Reset()
	stderr.Reset()
	if exit := runRemoteInit([]string{
		"--relay-url", "wss://s.example.workers.dev", "--relay-namespace", "owner",
	}, &stdout, &stderr); exit != 0 {
		t.Fatalf("remote init exit=%d stderr=%s", exit, stderr.String())
	}
	cfg, found, err := relaycfg.Load(dir)
	if err != nil || !found {
		t.Fatalf("Load: found=%v err=%v", found, err)
	}
	if cfg.OperatorNamespace != "owner" {
		t.Fatalf("OperatorNamespace = %q, want owner", cfg.OperatorNamespace)
	}
}
