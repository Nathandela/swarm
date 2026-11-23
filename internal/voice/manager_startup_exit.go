package voice

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// The status can reach local operator diagnostics. Never include terminal output or provider errors.
func managerStartupExit(row protocol.SessionView) error {
	facts := []string{"manager process exited before readiness could be verified"}
	if row.ManagerExitCode != nil {
		facts = append(facts, fmt.Sprintf("exit code %d", *row.ManagerExitCode))
	}
	if row.ManagerExitSignal > 0 && row.ManagerExitSignal < 128 {
		facts = append(facts, fmt.Sprintf("signal %d", row.ManagerExitSignal))
	}
	if !row.CreatedAt.IsZero() {
		facts = append(facts, "started "+row.CreatedAt.UTC().Format(time.RFC3339Nano))
	}
	if !row.LastActivity.IsZero() {
		facts = append(facts, "last activity "+row.LastActivity.UTC().Format(time.RFC3339Nano))
	}
	if row.BackendPlanError != "" {
		facts = append(facts, "backend plan unavailable")
	}
	return errors.New(strings.Join(facts, ", "))
}

// DiscussionStartupMessage translates only locally recognized conditions. Raw CLI
// errors, paths, and provider response bodies must stay out of Discord.
func DiscussionStartupMessage(err error) string {
	var exited *bootstrapExit
	var failed *failedResume
	switch {
	case errors.As(err, &exited), errors.As(err, &failed):
		return "The dispatcher closed before it could answer. Your message is saved."
	case errors.Is(err, errControllerSupervisorOffline):
		return "Swarm is offline on this machine. Your message is saved."
	case errors.Is(err, errCompetingManagers):
		return "Swarm found another dispatcher for this chat. Check the dispatchers in Swarm. Your message is saved."
	case errors.Is(err, errMissingDispatcherExit):
		return "Your message is saved. Swarm needs help confirming the previous dispatcher stopped. Check its status on this machine."
	case errors.Is(err, ErrControllerUnsupported), errors.Is(err, protocol.ErrManagerLaunchPrerequisite):
		return "Swarm couldn’t start the configured CLI. Check the dispatcher setup on this machine. Your message is saved."
	case errors.Is(err, ErrControllerNativeUnavailable):
		return "The dispatcher opened but isn’t ready. Check its window in Swarm for a setup or sign-in prompt. Your message is saved."
	default:
		return "The dispatcher is unavailable. Your message is saved."
	}
}
