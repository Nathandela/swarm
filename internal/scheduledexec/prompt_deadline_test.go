package scheduledexec

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/schedule"
)

type undeliveredPromptBackend struct{ *lifecycleBackend }

func (undeliveredPromptBackend) SendPrompt(string, string, string, func() error) error {
	return errors.New("native input is unavailable")
}

// A worker that never received its opening prompt cannot complete, so the
// attempt must end on its own instead of blocking the job forever.
func TestUndeliveredOpeningPromptEndsTheAttempt(t *testing.T) {
	for name, needsInput := range map[string]bool{"empty composer": false, "startup dialog": true} {
		e, store, lb, job, now := lifecycleFixture(t)
		e.backend = undeliveredPromptBackend{lb}
		id, err := e.Run(job.ID, schedule.NewID(now), now)
		if err != nil || len(lb.launches) != 1 {
			t.Fatalf("%s: start: %v", name, err)
		}
		attempt := lb.launches[0].AttemptID
		lb.sessions[attempt] = Session{Running: true, NeedsInput: needsInput}
		if err := e.Tick(now.Add(promptDeadline - time.Second)); err != nil || len(lb.stops) != 0 {
			t.Fatalf("%s: stopped before the deadline: %v %v", name, err, lb.stops)
		}
		if err := e.Tick(now.Add(promptDeadline)); err != nil || len(lb.stops) != 1 {
			t.Fatalf("%s: undelivered prompt kept the worker: %v %v", name, err, lb.stops)
		}
		lb.sessions[attempt] = Session{Stopped: true}
		if err := e.Tick(now.Add(promptDeadline + time.Second)); err != nil {
			t.Fatal(err)
		}
		run, err := store.Occurrence(id)
		if err != nil || run.State != schedule.OccurrenceIncomplete || !strings.Contains(run.Detail, "opening prompt") {
			t.Fatalf("%s: want a final incomplete run naming the prompt: %+v %v", name, run, err)
		}
	}
}
