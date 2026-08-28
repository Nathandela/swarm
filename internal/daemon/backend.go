package daemon

// THE SESSION BACKEND, from the DAEMON's side (Wave R7, Mirror M4.1; ADR-013 §R7.2b/c and
// §R7.6): its launch config, its recovery across a daemon restart, and the orphan reaper that
// closes the SIGKILL residual.
//
// THE DAEMON CARRIES THE PLAN, IT NEVER DERIVES IT. BackendSpec arrives on LaunchSpec exactly
// as Argv and CaptureEvents do: the assembly resolves it from the session's adapter (through
// adapter.ResolveBackend, which performs conformance obligations 9a and 9c) and this package
// resolves no adapter at all.
//
// WHY LIVENESS IS A FACT AND NOT A PROBE. The agent's documented last-resort residual -- an
// uncatchable SIGKILL of the shim -- is bounded in practice by the PTY: the master closes and
// the agent takes SIGHUP/EIO. The app-server has NO PTY, no controlling terminal and writes to
// neither stream ever, so a SIGKILL of its shim leaves a process authenticated to a real
// ChatGPT account alive indefinitely, still serving <session-dir>/codex.sock -- and still
// looking HEALTHY to any rule whose readiness test is "the dial succeeded". A restarted daemon
// that dialled would succeed, adopt an unrelated process on a reused path, and report a
// session live whose agent died hours ago. So liveness is (pid alive AND start-time matches),
// read off the shim's own backend.json, and the reaper NEVER DIALS.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"github.com/Nathandela/swarm/internal/shim"
)

// backendSocketFile is the per-session backend endpoint's name, a sibling of hook.sock inside
// the session dir. It is deterministic for the same reason shimSocketPath is: spawn, the
// containment check and any later reader must agree without bookkeeping.
const backendSocketFile = "codex.sock"

// BackendSpec is one session's RESOLVED backend plan, carried from the assembly. Program is
// already an absolute path: the adapter NAMES a program and adapter.ResolveBackend LookPaths
// it, so a restarted daemon never re-resolves a bare name through a PATH that may now point at
// a different version.
type BackendSpec struct {
	Program   string
	Args      []string
	AgentArgs []string
	Env       []string
}

// BackendChannel names one session's backend endpoint, recovered from the 0600
// shim-launch.json. It is the twin of HookChannel and exists for the same reason: shims
// outlive daemons (ADR-001), so a daemon that restarted under a still-running shim must find
// exactly what it minted at launch.
type BackendChannel struct {
	SocketPath string
	AgentArgs  []string
}

// backendSocketPath is the per-session backend UDS path.
func backendSocketPath(stateDir, id string) string {
	return filepath.Join(stateDir, id, backendSocketFile)
}

// SessionBackend recovers id's backend channel from its persisted launch config. It reports
// false for a session launched WITHOUT one (no backend_socket_path key at all) rather than
// fabricating one -- the same "unset means disabled" convention SessionHookChannel follows,
// and what makes every pre-R7 session, and every non-Codex session forever, read as having
// none.
func (d *Daemon) SessionBackend(id string) (BackendChannel, bool) {
	data, err := os.ReadFile(filepath.Join(d.sessionDir(id), shimLaunchConfigFile))
	if err != nil {
		return BackendChannel{}, false
	}
	var lc shimSpawnConfig
	if json.Unmarshal(data, &lc) != nil || lc.BackendSocketPath == "" {
		return BackendChannel{}, false
	}
	return BackendChannel{SocketPath: lc.BackendSocketPath, AgentArgs: lc.BackendAgentArgs}, true
}

// backendAliveAt reports whether the backend recorded in sessionDir's backend.json is THAT
// process, still running.
//
// IT IS DELIBERATELY NOT A DIAL. A socket outlives the process that bound it, and an
// unrelated process on a reused path answers a dial perfectly. The check is the SAME
// (pid, start-time) pair reconcile already matches shims by (launch.go / reconcile.go), which
// is why it costs no new mechanism: a recycled pid whose start time disagrees is not our
// backend, and on a busy machine a recycled pid is not exotic.
func (d *Daemon) backendAliveAt(sessionDir string) bool {
	info, ok := shim.ReadBackendInfo(sessionDir)
	if !ok || info.PID <= 0 {
		return false
	}
	if !pidAlive(info.PID) {
		return false
	}
	st, err := procStartTimeFn(info.PID)
	return err == nil && st == info.StartedAtMS
}

// reapOrphanBackend kills the backend of a session whose shim is gone, and removes the record.
//
// An app-server whose shim is gone is BY CONSTRUCTION an orphan: nothing else in the system
// will ever reap it and no agent is attached to it. It acts on the RECORDED pid and the
// RECORDED start time and NOTHING ELSE -- a reaper that dialled would treat a squatter on the
// path as its own backend and spare it, which is the very confusion this whole mechanism
// exists to resolve.
//
// The stale file is removed after the kill so the NEXT reconcile does not try to reap a pid
// that is now somebody else's.
func (d *Daemon) reapOrphanBackend(id string) {
	// One reap at a time (r7-check round 4, finding 1): the sequence below is
	// read-validate-kill-remove over shared on-disk state, and its callers (markLost,
	// handleShimExit, reconcile) can race for the same id. See Daemon.reapMu.
	d.reapMu.Lock()
	defer d.reapMu.Unlock()
	dir := d.sessionDir(id)
	info, ok := shim.ReadBackendInfo(dir)
	if !ok {
		return
	}
	pgid := info.PGID
	if pgid <= 0 {
		pgid = info.PID
	}
	switch {
	case d.backendAliveAt(dir):
		// One shot, the whole group: R1 leg 1 recorded that one `codex app-server` is TWO
		// pids (a node launcher plus the vendored rust binary), so the group is what reaps
		// both.
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		d.logf("reaped orphaned session backend for %s (pid %d, pgid %d)", id, info.PID, pgid)
	case pidAlive(info.PID):
		// The recorded leader pid is RUNNING but it is not our backend: its start time
		// disagrees (a recycled pid) or cannot be read at all. Signalling -pgid here would
		// hit a stranger -- and in the recycled case there is provably nothing of ours left
		// to reap, because POSIX only recycles a pid once its old process GROUP has no
		// members remaining. LOUD, because the unreadable-start-time case lands here too and
		// is the one residual this arm cannot tell apart; the record is still removed below
		// (an unverifiable record can never become verifiable, so keeping it would make
		// every future reconcile retry this same dead end).
		d.logf("backend record for %s names live pid %d whose identity does not match; NOT "+
			"signalling group %d (recycled pid, or unreadable start time) and dropping the record",
			id, info.PID, pgid)
	default:
		// DEAD LEADER. The app-server being TWO pids cuts both ways: the leader dying does
		// not mean the backend is gone -- the vendored child can survive in the group. If any
		// member remains, kill(-pgid, 0) succeeds, and whoever it reaches IS the recorded
		// backend's: a process-group id cannot be reused while the group still has members
		// (POSIX process-group lifetime), so with the leader gone this can never be pid
		// reuse. The identity guard is the arm above: a leader that is alive-but-mismatched
		// never reaches this kill.
		switch gerr := syscall.Kill(-pgid, 0); {
		case gerr == nil:
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			d.logf("reaped surviving members of session backend group for %s (dead leader %d, "+
				"pgid %d)", id, info.PID, pgid)
		case !errors.Is(gerr, syscall.ESRCH):
			// The group cannot be validated (EPERM: members exist that are not ours to
			// signal -- so not our backend's). Refuse to signal what cannot be proven ours,
			// say so LOUDLY, and still drop the record below: it can never become provable,
			// and keeping it would pin every future reconcile on this same unprovable kill.
			d.logf("CANNOT VALIDATE backend group %d for %s (%v); not signalling it and "+
				"dropping the record", pgid, id, gerr)
		}
		// ESRCH: the group is empty; there is nothing left to reap.
	}
	if err := os.Remove(filepath.Join(dir, shim.BackendFile)); err != nil && !os.IsNotExist(err) {
		d.logf("remove stale backend record for %s: %v", id, err)
	}
}

// SendBackendAttach releases a shim that is waiting for the daemon's go-ahead before it spawns
// its agent. The assembly calls it once it is a connected client of the session's backend.
func (d *Daemon) SendBackendAttach(id string, agentArgs []string) error {
	return sendBackendAttach(shimSocketPath(d.cfg.StateDir, id), agentArgs)
}

// BackendPlanner is the seam through which the ASSEMBLY supplies a session's backend plan
// (Config.BackendPlanner). The core calls it at spawn, when the session id -- and therefore
// the session dir and the backend socket path -- exist for the first time; the assembly
// resolves the plan from the session's adapter and performs the contract's own checks
// (adapter.ResolveBackend: the program resolves through a prober, and no argument names an
// absolute path outside the session dir).
//
// It exists because the two facts live in different packages and neither may import the
// other: only the core knows the socket path, and only the assembly may touch an adapter.
// A nil planner, or a nil spec, is a session with no backend -- the ordinary case.
//
// agentEnv is the launch's RESOLVED agent environment (PolicyEnv already applied), so the
// planner resolves the backend program against the same PATH the agent itself runs with --
// not the daemon's own. A daemon started outside a login shell carries a PATH that may not
// see the agent binary at all, while the launch's client env does; resolving against the
// daemon's env in that case planned no backend for a session whose agent ran fine. A nil
// agentEnv (a caller predating the launch path, or a test) lets the assembly fall back to
// daemon policy.
type BackendPlanner func(agentType, sessionDir, socketPath string, agentEnv []string) (*BackendSpec, error)
