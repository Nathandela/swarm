package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

// persistScheduledLaunch uses the ordinary metadata writer and syncs the
// namespace before the caller starts or releases the shim. Store.Save already
// syncs the file contents before renaming it.
func (d *Daemon) persistScheduledLaunch(m *persist.Meta) error {
	m.SchemaVersion = persist.SchemaVersion
	d.writeMu.Lock()
	written, err := d.saveMetaLocked(m)
	if err == nil {
		if written {
			err = syncLaunchDirectories(d.sessionDir(m.ID), d.cfg.StateDir)
		} else {
			err = errors.New("scheduled launch reservation disappeared")
		}
	}
	d.writeMu.Unlock()
	if err == nil && d.cfg.onMetaSave != nil {
		d.cfg.onMetaSave(*m)
	}
	return err
}

func syncLaunchDirectories(dirs ...string) error {
	for _, path := range dirs {
		dir, err := os.Open(path)
		if err != nil {
			return err
		}
		err = dir.Sync()
		_ = dir.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *Daemon) admitScheduledShim(m *persist.Meta, pipe *os.File) error {
	if m.ShimStartTime <= 0 {
		return errors.New("invalid shim start time")
	}
	if err := d.persistScheduledLaunch(m); err != nil {
		return fmt.Errorf("persist shim identity: %w", err)
	}
	if m.WorkerDiscussion != "" {
		if err := d.idem.Begin(strings.TrimPrefix(m.WorkerAttempt, "vm-child-")); err != nil {
			return fmt.Errorf("persist worker execution gate: %w", err)
		}
	}
	if _, err := pipe.Write([]byte{shim.LaunchIdentityRelease}); err != nil {
		return fmt.Errorf("release shim identity gate: %w", err)
	}
	return nil
}

// abortScheduledShim revokes admission by closing the only writer. Even if
// the shim is stopped and cannot observe EOF yet, it cannot start any worker.
// Retain the reservation and its owner-operation binding for reconciliation.
func (d *Daemon) abortScheduledShim(m persist.Meta, cmd *exec.Cmd, s *session, pipe *os.File, err error) (persist.Meta, error) {
	_ = pipe.Close()
	d.wg.Add(1)
	go d.superviseLaunched(m.ID, cmd, s.stop)
	reserved, _ := d.Get(m.ID)
	return reserved, fmt.Errorf("daemon: scheduled launch %s: %w", m.ID, err)
}

// scheduledLaunchApplied keeps a persisted identity alone from becoming a
// positive owner receipt while the shim is still gated or starting up.
func (d *Daemon) scheduledLaunchApplied(m persist.Meta) bool {
	if m.ScheduleAttempt == "" && m.WorkerDiscussion == "" || m.Status.Process == status.ProcessExited {
		return true
	}
	return d.shimIdentityMatches(m) && confirmShimServing(shimSocketPath(d.cfg.StateDir, m.ID))
}
