package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/workermail"
)

// A scheduled worker has no manager to message; it must be told how to finish.
func TestTeammateMessageFromScheduledWorkerPointsToScheduleComplete(t *testing.T) {
	t.Setenv(daemon.ScheduleEnvRun, "run")
	t.Setenv(schema.WorkerBridgeEnv, "")
	t.Setenv(workermail.SocketEnv, "")
	var stdout, stderr bytes.Buffer
	if code := dispatchTeammate([]string{"message", "--kind", "complete", "--text", "done"}, &stdout, &stderr); code == 0 {
		t.Fatal("scheduled worker message reported success")
	}
	if !strings.Contains(stderr.String(), "schedule complete --summary") {
		t.Fatalf("no completion guidance: %q", stderr.String())
	}
}
