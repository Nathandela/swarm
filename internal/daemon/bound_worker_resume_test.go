package daemon

import (
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func TestBoundWorkerResumeRechecksSourceUnderReservationLock(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	source := persist.Meta{ID: "source", AgentType: "codex", ConversationID: "conversation",
		ShimPID: 12_345_678, ShimStartTime: 10, Status: status.Status{Process: status.ProcessExited}}
	if err := d.saveMeta(source); err != nil {
		t.Fatal(err)
	}
	spec := LaunchSpec{AgentType: source.AgentType, ConversationID: source.ConversationID,
		ResumedFrom: source.ID, Cwd: t.TempDir(), OwnerLaunchHash: "owner-bound", OperationID: "operation",
		ResumeSource: &ResumeSourceFence{Instance: "expected", ShimPID: source.ShimPID, ShimStartTime: source.ShimStartTime}}
	source.ShimStartTime++
	if err := d.saveMeta(source); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Launch(spec); !errors.Is(err, ErrResumeSourceChanged) {
		t.Fatalf("replaced source reached launch reservation: %v", err)
	}
	if _, known := d.OwnerLaunchOutcome(spec.OperationID); known || len(d.List()) != 1 {
		t.Fatal("stale worker consumed an owner operation or reserved a new worker")
	}
}
