package loginworker

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAstraFailedStartDoesNotPoisonJobInventory(t *testing.T) {
	state, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	c := Config{SchemaVersion: 1, StateRoot: state, JobID: strings.Repeat("a", 32), Generation: 1, Provider: "codex", Label: "test", Method: MethodDeviceCode, NativePath: "/definitely/missing/codex", NativeVersion: "0.163.0", NativeFingerprint: "missing", Deadline: time.Now().Add(time.Minute)}
	if _, e = Start(context.Background(), "/definitely/missing/swarm", c); e == nil {
		t.Fatal("expected failed start")
	}
	if _, e = Jobs(state); e != nil {
		t.Fatalf("failed Start leaves missing-config directory that blocks every future login action: %v", e)
	}
}
