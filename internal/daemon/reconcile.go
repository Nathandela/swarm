package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

// reconcile rebuilds the registry from the meta scan and resolves every session
// (S3/S11/L2). It runs synchronously in Open, before the daemon serves, so
// List/Get reflect the reconnected world immediately.
func (d *Daemon) reconcile() {
	metas, err := d.store.Scan()
	if err != nil {
		return
	}
	for _, m := range metas {
		if m.Status.Process != status.ProcessRunning {
			d.putMem(m) // already terminal: register as-is, no monitor, no write
			continue
		}
		d.reconcileRunning(m)
	}
}

// reconcileRunning resolves one meta that was last persisted as running.
func (d *Daemon) reconcileRunning(m persist.Meta) {
	dir := d.sessionDir(m.ID)

	// 1. An exit side-file is authoritative: the shim finished and reported, so the
	//    session is EXITED (not lost — lost is reserved for a shim that vanished
	//    with no exit report). True even if the PID lingers or was reused.
	if ei, ok := readExitSideFile(dir); ok {
		_ = d.saveMeta(mergeExit(m, ei))
		return
	}

	// 2. Verify identity: the recorded PID is alive AND its start time matches, and
	//    the shim answers the G2 hello. Reconnect WITHOUT any meta write, so a live
	//    shim is never transiently persisted as lost (E5.3, reconnect-before-lost).
	if m.ShimPID > 0 && pidAlive(m.ShimPID) {
		if st, err := processStartTime(m.ShimPID); err == nil && st == m.ShimStartTime {
			if confirmShimServing(shimSocketPath(d.cfg.StateDir, m.ID)) {
				d.registerRunning(m)
				return
			}
		}
	}

	// 3. Reaped, PID-reused, or not serving → LOST. Zero signals are sent: the
	//    daemon reclassifies only its own record, never touching the PID (S3).
	m.Status.Process = status.ProcessLost
	_ = d.saveMeta(m)
}

// registerRunning adopts a live, reconnected shim: it records the running meta in
// memory (no disk write) and starts a liveness monitor that finalizes the session
// when the shim later exits or vanishes.
func (d *Daemon) registerRunning(m persist.Meta) {
	s := d.putMem(m)
	d.wg.Add(1)
	go d.pollMonitor(m.ID, m.ShimPID, m.ShimStartTime, s.stop)
}

// pollMonitor watches a reconnected shim by (PID, start-time) until it exits or
// the daemon stops. On exit it finalizes the session from its side-files. It
// never signals the process; identity mismatch (PID reuse) is treated as a
// vanished shim, not a live one.
func (d *Daemon) pollMonitor(id string, pid int, start int64, stop chan struct{}) {
	defer d.wg.Done()
	t := time.NewTicker(monitorPoll)
	defer t.Stop()
	for {
		select {
		case <-d.stopCh:
			return
		case <-stop:
			return
		case <-t.C:
			if pidAlive(pid) {
				if st, err := processStartTime(pid); err == nil && st == start {
					continue // still our shim
				}
			}
			d.handleShimExit(id)
			return
		}
	}
}

// handleShimExit finalizes a session whose shim has exited: EXITED with the code
// from exit.json when the shim reported, else LOST. It re-checks membership and
// running-ness under the lock, so a Delete that already removed the session, or a
// double-fire, is a no-op.
func (d *Daemon) handleShimExit(id string) {
	d.mu.Lock()
	s, ok := d.sessions[id]
	if !ok || s.meta.Status.Process != status.ProcessRunning {
		d.mu.Unlock()
		return
	}
	m := s.meta
	d.mu.Unlock()

	if ei, ok := readExitSideFile(d.sessionDir(id)); ok {
		m = mergeExit(m, ei)
	} else {
		m.Status.Process = status.ProcessLost
	}
	_ = d.saveMeta(m)
}

// putMem inserts or updates a session in the registry without writing to disk,
// returning the session handle. Used for reconnect and terminal adoption where no
// meta write is warranted.
func (d *Daemon) putMem(m persist.Meta) *session {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.sessions[m.ID]
	if !ok {
		s = &session{stop: make(chan struct{})}
		d.sessions[m.ID] = s
	}
	s.meta = m
	return s
}

// mergeExit folds a shim's exit report into a meta: the process dimension becomes
// exited and the exit code is recorded (G6 side-file merge).
func mergeExit(m persist.Meta, ei shim.ExitInfo) persist.Meta {
	m.Status.Process = status.ProcessExited
	code := ei.ExitCode
	m.ExitCode = &code
	if !ei.FinishedAt.IsZero() {
		m.LastActivity = ei.FinishedAt
	}
	return m
}

// readExitSideFile reads and decodes a session's exit.json, if present.
func readExitSideFile(dir string) (shim.ExitInfo, bool) {
	data, err := os.ReadFile(filepath.Join(dir, shim.ExitFile))
	if err != nil {
		return shim.ExitInfo{}, false
	}
	var ei shim.ExitInfo
	if json.Unmarshal(data, &ei) != nil {
		return shim.ExitInfo{}, false
	}
	return ei, true
}
