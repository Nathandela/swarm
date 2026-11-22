package voice

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestManagerStartupExitUsesOnlyBoundedProcessFacts(t *testing.T) {
	code := 143
	row := protocol.SessionView{
		ManagerExitCode: &code, ManagerExitSignal: 15,
		CreatedAt: time.Unix(5, 0), LastActivity: time.Unix(9, 0),
		Summary: "private terminal text", BackendPlanError: "private executable path", ResumeError: "private error text",
	}
	got := managerStartupExit(row).Error()
	for _, want := range []string{"exit code 143", "signal 15", "1970-01-01T00:00:05Z", "1970-01-01T00:00:09Z", "backend plan unavailable"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "private") {
		t.Fatalf("leaked private diagnostic: %q", got)
	}
	if msg := managerStartupExit(protocol.SessionView{}).Error(); strings.Contains(msg, "exit code") || strings.Contains(msg, "0001-") {
		t.Fatalf("invented exit metadata: %q", msg)
	}
}

func TestDiscussionStartupMessageHidesPrivateDetails(t *testing.T) {
	for _, tc := range []struct {
		cause error
		want  string
	}{
		{errManagerStillStarting, "setup or sign-in"},
		{&bootstrapExit{}, "closed"},
		{errControllerSupervisorOffline, "offline"},
		{errCompetingManagers, "Two dispatchers"},
		{protocol.ErrManagerLaunchPrerequisite, "setup"},
		{ErrDiscussionActivationUnavailable, "unavailable"},
	} {
		got := DiscussionStartupMessage(errors.Join(tc.cause, errors.New("private terminal output")))
		if !strings.Contains(got, tc.want) || strings.Contains(got, "private") {
			t.Fatalf("unexpected public startup message: %q", got)
		}
	}
}
