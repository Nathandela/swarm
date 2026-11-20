package scheduledexec

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/schedule"
)

func TestStartupRejectionFallsBackOnlyAfterVerifiedStop(t *testing.T) {
	engine, store, backend, job, now := lifecycleFixture(t)
	runID, err := engine.Run(job.ID, schedule.NewID(now), now)
	if err != nil {
		t.Fatal(err)
	}
	first := backend.launches[0].AttemptID
	backend.sessions[first] = Session{Running: true, StartupRejected: true}
	if err := engine.Tick(now.Add(time.Second)); err != nil || len(backend.launches) != 1 {
		t.Fatalf("startup rejection does not prove stop: launches %d, err %v", len(backend.launches), err)
	}
	backend.sessions[first] = Session{Stopped: true, StartupRejected: true}
	if err := engine.Tick(now.Add(2 * time.Second)); err != nil || len(backend.launches) != 2 {
		t.Fatalf("verified pre-prompt stop did not advance fallback: launches %d, err %v", len(backend.launches), err)
	}
	run, err := store.Occurrence(runID)
	if err != nil || len(run.Attempts) != 2 || run.Attempts[0].State != schedule.AttemptStopped || run.Attempts[1].Agent != schedule.RouteClaude {
		t.Fatalf("startup fallback history: %+v, err %v", run.Attempts, err)
	}
}
