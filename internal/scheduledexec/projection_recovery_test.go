package scheduledexec

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/schedule"
)

func TestProjectionWaitsForSessionRegistryRecovery(t *testing.T) {
	e, store, backend, job, now := lifecycleFixture(t)
	id, err := e.Run(job.ID, schedule.NewID(now), now)
	if err != nil {
		t.Fatal(err)
	}
	attempt := backend.launches[0]
	run, err := store.UpdateOccurrence(id, func(run *schedule.Occurrence) error {
		run.State = schedule.OccurrenceReady
		run.Attempts[0].State = schedule.AttemptComplete
		run.Completion = &schedule.Completion{Summary: "Finished"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	delete(backend.sessions, attempt.AttemptID)
	if err := e.project(run); err != nil {
		t.Fatal(err)
	}
	run, err = store.Occurrence(id)
	if err != nil || !run.ProjectedAt.IsZero() {
		t.Fatalf("missing session acknowledged projection: %v (%v)", run.ProjectedAt, err)
	}
	backend.sessions[attempt.AttemptID] = Session{Stopped: true}
	if err := e.Tick(now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(backend.projected) != 1 || backend.projected[0] != attempt.AttemptID {
		t.Fatalf("projections after recovery: %v", backend.projected)
	}
}

func TestWorkspaceLaunchRaceRetriesSameAgent(t *testing.T) {
	e, store, backend, job, now := lifecycleFixture(t)
	job.Spec.AllowOverlap = true
	job, err := store.EditJob(job.ID, job.Spec, now)
	if err != nil {
		t.Fatal(err)
	}
	backend.launchErr = ErrWorkspaceBusy
	id, err := e.Run(job.ID, schedule.NewID(now), now)
	if err != nil {
		t.Fatal(err)
	}
	backend.launchErr = nil
	if err := e.Tick(now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	run, err := store.Occurrence(id)
	if err != nil || run.State != schedule.OccurrenceWorking || len(run.Attempts) != 2 ||
		run.Attempts[0].State != schedule.AttemptDeferred || run.Attempts[0].Agent != run.Attempts[1].Agent {
		t.Fatalf("workspace release did not admit original route: %+v, %v", run, err)
	}
}
