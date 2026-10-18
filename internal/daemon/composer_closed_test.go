package daemon

import (
	"github.com/Nathandela/swarm/internal/idempotency"
	"path/filepath"
	"testing"
)

func TestComposerCustodyCannotWriteAfterCoreReleasesSingleton(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	if _, _, err := d.ClaimComposerOperation("native", "semantic_send", "worker", "instance", "hash"); err != nil {
		t.Fatal(err)
	}
	if err := d.BindSemanticRequest("native", []byte(`{"message_id":"msg_owned"}`)); err != nil {
		t.Fatal(err)
	}
	if err := d.BeginComposerOperation("native"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.ClaimComposerOperation("queued", "semantic_send", "worker", "instance", "hash"); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.ObserveSemanticReceipt("native", []byte(`{"status":"admitted"}`)); err == nil {
		t.Fatal("late native receipt wrote through retired daemon")
	}
	if err := d.CommitComposerOperation("native", []byte(`{"ok":true}`), true); err == nil {
		t.Fatal("late delivery commit wrote through retired daemon")
	}
	if err := d.BeginComposerOperation("queued"); err == nil {
		t.Fatal("retired daemon crossed execution boundary")
	}
	if _, _, err := d.ClaimComposerOperation("new", "semantic_send", "worker", "instance", "hash"); err == nil {
		t.Fatal("retired daemon reserved new operation")
	}
	reopened, err := idempotency.Open(filepath.Join(cfg.StateDir, "idempotency"))
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := reopened.Get("native")
	if rec.Phase != idempotency.PhaseExecuting || len(rec.Evidence) != 0 {
		t.Fatal("late actor changed durable custody")
	}
}
