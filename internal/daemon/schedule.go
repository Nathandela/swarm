package daemon

import (
	"errors"
	"fmt"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/worktree"
)

const (
	ScheduleTag        = "automation"
	ScheduleEnvAttempt = "SWARM_SCHEDULE_ATTEMPT"
	ScheduleEnvRun     = "SWARM_SCHEDULE_RUN"
	ScheduleEnvToken   = "SWARM_SCHEDULE_PROOF"
	ScheduleEnvSocket  = "SWARM_SCHEDULE_SOCKET"
	ScheduleEnvBinary  = "SWARM_SCHEDULE_BIN"
)

var ErrScheduledWorkspaceBusy = errors.New("scheduled workspace is owned by a running or unconfirmed Swarm session")
var ErrScheduledWorkspaceInvalid = errors.New("invalid scheduled workspace")

type ScheduledLaunch struct {
	AttemptID        string
	RunID            string
	Token            string
	CompletionSocket string
}

func (d *Daemon) scheduledEnvironment(launch *ScheduledLaunch, id string) []string {
	return []string{
		ScheduleEnvAttempt + "=" + launch.AttemptID,
		ScheduleEnvRun + "=" + launch.RunID,
		ScheduleEnvToken + "=" + launch.Token,
		ScheduleEnvSocket + "=" + launch.CompletionSocket,
		ScheduleEnvBinary + "=" + d.cfg.ShimBinary,
		"SWARM_SESSION_ID=" + id,
	}
}

// ScheduledExit requires the shim's durable receipt, written after containment
// and backend join. A lost shim alone cannot establish that its worker stopped.
func (d *Daemon) ScheduledExit(id, attempt string) bool {
	m, ok := d.Get(id)
	if !ok || m.ScheduleAttempt != attempt || m.ShimPID <= 0 || m.HermesNative || d.shimIdentityMatches(m) {
		return false
	}
	_, hasExit := readExitSideFile(d.sessionDir(id))
	return hasExit
}

func (d *Daemon) MarkScheduledExhaustion(id string, exhausted bool) error {
	return d.updateScheduledMeta(id, "", func(m *persist.Meta) bool {
		if m.ScheduleExhausted == exhausted {
			return false
		}
		m.ScheduleExhausted = exhausted
		return true
	})
}

// ProjectScheduledCompletion applies the board marker once. A later owner tag
// edit is permanent even if a recovery tick replays the completion projection.
func (d *Daemon) ProjectScheduledCompletion(id, attempt string) error {
	return d.updateScheduledMeta(id, attempt, func(m *persist.Meta) bool {
		if m.ScheduleProjected {
			return false
		}
		m.ScheduleProjected = true
		m.ScheduledCompleted = true
		m.Tag = ScheduleTag
		return true
	})
}

func (d *Daemon) updateScheduledMeta(id, attempt string, change func(*persist.Meta) bool) error {
	d.writeMu.Lock()
	d.mu.Lock()
	s, ok := d.sessions[id]
	var m persist.Meta
	if ok {
		m = s.meta
	}
	d.mu.Unlock()
	if !ok || m.ScheduleAttempt == "" || (attempt != "" && m.ScheduleAttempt != attempt) {
		d.writeMu.Unlock()
		return fmt.Errorf("scheduled automation: session %q has a different attempt", id)
	}
	if !change(&m) {
		d.writeMu.Unlock()
		return nil
	}
	m.SchemaVersion = persist.SchemaVersion
	m.Env = d.store.MetaEnv(m)
	written, err := d.saveMetaLocked(&m)
	d.writeMu.Unlock()
	if err != nil {
		return err
	}
	if !written {
		return errors.New("scheduled automation: session disappeared during metadata update")
	}
	if d.cfg.onMetaSave != nil {
		d.cfg.onMetaSave(m)
	}
	return nil
}

// ScheduledWorkspaceAvailable is an advisory check; launch reserves and checks
// the same workspace under the registry mutex before spawning.
func (d *Daemon) ScheduledWorkspaceAvailable(cwd string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.scheduledWorkspaceConflictLocked(cwd, true)
}

func (d *Daemon) scheduledWorkspaceConflictLocked(cwd string, scheduled bool) bool {
	for _, existing := range d.sessions {
		m := existing.meta
		// Leave established ordinary-vs-ordinary launch behavior intact.
		if !scheduled && m.ScheduleAttempt == "" {
			continue
		}
		if m.Status.Process == status.ProcessRunning && worktree.PathsOverlap(cwd, m.ProviderCwd()) {
			return true
		}
		if m.ScheduleAttempt != "" && m.Status.Process == status.ProcessLost && worktree.PathsOverlap(cwd, m.ProviderCwd()) {
			if _, confirmed := readExitSideFile(d.sessionDir(m.ID)); !confirmed {
				return true
			}
		}
	}
	return false
}
