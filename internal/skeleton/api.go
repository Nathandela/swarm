package skeleton

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

const (
	// eventPoll is how often the roster is sampled for status changes (well within
	// the L1 <=1 s bound). It mirrors protocol.FromDaemon's cadence.
	eventPoll = 200 * time.Millisecond
	// eventsBuffer sizes the roster event channel the Server fans out from.
	eventsBuffer = 64
)

// coreAPI adapts the core *daemon.Daemon to the protocol.DaemonAPI the Server
// wraps. It is a leak-free, self-contained equivalent of protocol.FromDaemon: the
// same list/kill/delete/attach forwarding and roster-poll event source, plus the
// walking-skeleton's reserved-agent "fake" argv resolution on Launch. It is owned
// here (not FromDaemon) so its poller is stopped deterministically on Close — the
// daemon owns the socket, so the Server never runs FromDaemon's own stop path.
type coreAPI struct {
	core         *daemon.Daemon
	fakeAgentBin string
	endpointID   string // this daemon's stable federation id (resume source validation)

	events   chan persist.Meta
	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func newCoreAPI(core *daemon.Daemon, fakeAgentBin, endpointID string) *coreAPI {
	a := &coreAPI{
		core:         core,
		fakeAgentBin: fakeAgentBin,
		endpointID:   endpointID,
		events:       make(chan persist.Meta, eventsBuffer),
		stop:         make(chan struct{}),
	}
	a.wg.Add(1)
	go a.watch()
	return a
}

func (a *coreAPI) List() []persist.Meta        { return a.core.List() }
func (a *coreAPI) Kill(id string) error        { return a.core.Kill(id) }
func (a *coreAPI) Delete(id string) error      { return a.core.Delete(id) }
func (a *coreAPI) Events() <-chan persist.Meta { return a.events }

// Launch resolves a client launch/resume request into a concrete daemon spec
// (real agent argv composed through the registry adapter, resume validated and
// composed from the source's conversation id) and forwards it to the core.
func (a *coreAPI) Launch(spec daemon.LaunchSpec) (persist.Meta, error) {
	resolved, err := composeLaunchSpec(spec, a.endpointID, a.fakeAgentBin, a.core.Get, lookPathIn)
	if err != nil {
		return persist.Meta{}, err
	}
	return a.core.Launch(resolved)
}

// composeLaunchSpec resolves a launch/resume request's concrete argv (Epic 11
// seam: adapters into launch). It is a pure function of its inputs — getSource
// abstracts the roster lookup — so resume validation and adapter argv composition
// are unit-testable without a live daemon.
//
//   - Resume-as-new-session (R-2): a launch carrying the reserved OptionResumeFrom
//     option resumes a prior session. The source is VALIDATED (belongs to this
//     endpoint, exists, is ended/lost, agent type matches); an invalid source is
//     rejected with a clear error. A resolvable adapter composes the resume argv
//     from the source's conversation id, so the new process CONTINUES the
//     conversation, and the new session's ResumedFrom links back to the source. A
//     resume is rejected (never silently downgraded to a fresh launch) when the
//     agent has no resuming adapter — e.g. the reserved "fake" agent — or no
//     conversation id was captured, so ResumedFrom is stamped only on a real resume.
//   - Fresh launch: a registry-resolvable agent's argv is composed via
//     adapter.Command (the real argv, including any inline hook injection); the
//     reserved dev/test "fake" agent resolves to the swarm-fake-agent binary. The
//     core rejects an unresolved (empty-argv) launch.
//
// An adapter's argv[0] is the bare binary name (e.g. "claude"); the shim execs it
// verbatim, so it is RESOLVED to an absolute path against the agent's own PATH via
// lookPath (a stub in tests, the real PATH search in production). A missing binary
// is a clear launch error.
func composeLaunchSpec(spec daemon.LaunchSpec, endpointID, fakeAgentBin string, getSource func(local string) (persist.Meta, bool), lookPath func(name string, env []string) (string, error)) (daemon.LaunchSpec, error) {
	// GG-6 scope: refuse a registered-but-non-production adapter at the launch
	// boundary so a crafted launch RPC cannot spawn it in a real install. The only
	// such adapter is the fixture-only "reference" (kept registered for the E9.5
	// characterization harness and the launch-picker probe). The gate is lifted ONLY
	// in dev/test mode — signalled, as for the reserved "fake" agent, by fakeAgentBin
	// being configured (SWARM_FAKE_AGENT_BIN, unset in a real install) — under which
	// the reference adapter is the non-billable e2e vehicle for the conversation-
	// capture/resume flows (C1/R2). Riding on that already-dev/test-only signal adds
	// no new production launch surface. ("fake" is not registered here, so it is
	// unaffected; an unknown agent falls through to its existing empty-argv rejection.)
	if fakeAgentBin == "" {
		if _, registered := registry.New(spec.AgentType); registered && !registry.IsProduction(spec.AgentType) {
			return daemon.LaunchSpec{}, fmt.Errorf("launch: agent %q is not a production provider and cannot be launched", spec.AgentType)
		}
	}

	if src := spec.Options[protocol.OptionResumeFrom]; src != "" {
		local, srcMeta, err := validateResumeSource(src, spec.AgentType, endpointID, getSource)
		if err != nil {
			return daemon.LaunchSpec{}, err
		}
		ad, ok := registry.New(spec.AgentType)
		if !ok {
			return daemon.LaunchSpec{}, fmt.Errorf("resume: agent %q has no adapter that can resume", spec.AgentType)
		}
		argv, rerr := ad.Resume(adapter.ResumeSpec{
			Cwd:            spec.Cwd,
			ConversationID: srcMeta.ConversationID,
			Options:        spec.Options,
		})
		if rerr != nil {
			return daemon.LaunchSpec{}, fmt.Errorf("resume: compose argv: %w", rerr)
		}
		// An empty resume argv means the adapter had no conversation id to replay
		// (never captured). REFUSE rather than fall through to a fresh launch falsely
		// stamped ResumedFrom (B1): a resume must resume, or fail with a clear reason.
		if len(argv) == 0 {
			return daemon.LaunchSpec{}, fmt.Errorf("resume: cannot resume %q: no captured conversation id", local)
		}
		resolved, lerr := resolveArgv0(argv, spec.ClientEnv, lookPath)
		if lerr != nil {
			return daemon.LaunchSpec{}, fmt.Errorf("resume: %w", lerr)
		}
		spec.Argv = resolved     // the resume argv carries the source's conversation id
		spec.ResumedFrom = local // stamp ONLY now that a real resume argv is composed
	}

	if len(spec.Argv) == 0 {
		switch {
		case spec.AgentType == "fake":
			if fakeAgentBin != "" {
				spec.Argv = []string{fakeAgentBin, spec.Options["script"]}
			}
		default:
			if ad, ok := registry.New(spec.AgentType); ok {
				argv, err := ad.Command(adapter.LaunchSpec{
					Cwd:           spec.Cwd,
					Options:       spec.Options,
					InitialPrompt: spec.InitialPrompt,
				})
				if err != nil {
					return daemon.LaunchSpec{}, fmt.Errorf("launch: compose %s argv: %w", spec.AgentType, err)
				}
				resolved, lerr := resolveArgv0(argv, spec.ClientEnv, lookPath)
				if lerr != nil {
					return daemon.LaunchSpec{}, fmt.Errorf("launch: resolve %s binary: %w", spec.AgentType, lerr)
				}
				spec.Argv = resolved
			}
		}
	}
	return spec, nil
}

// resolveArgv0 rewrites argv[0] (the bare agent binary name) to an absolute path
// via lookPath, leaving the rest of argv untouched. It copies argv so the caller's
// slice is not mutated.
func resolveArgv0(argv, env []string, lookPath func(name string, env []string) (string, error)) ([]string, error) {
	if len(argv) == 0 {
		return argv, nil
	}
	resolved, err := lookPath(argv[0], env)
	if err != nil {
		return nil, err
	}
	out := append([]string(nil), argv...)
	out[0] = resolved
	return out, nil
}

// lookPathIn resolves a bare program name to an absolute path by searching the PATH
// carried in env — the AGENT's own PATH, not the daemon's — so the resolved binary
// is what the agent would itself run. A name that already contains a path separator
// is returned as-is if it is an executable file. It mirrors exec.LookPath but binds
// to a supplied PATH rather than the daemon process environment.
func lookPathIn(name string, env []string) (string, error) {
	if strings.ContainsRune(name, os.PathSeparator) {
		if isExecutableFile(name) {
			return name, nil
		}
		return "", fmt.Errorf("agent binary %q is not an executable file", name)
	}
	var pathEnv string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			pathEnv = v
		}
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			continue
		}
		cand := filepath.Join(dir, name)
		if isExecutableFile(cand) {
			return cand, nil
		}
	}
	return "", fmt.Errorf("agent binary %q not found on the agent PATH", name)
}

// isExecutableFile reports whether path is a regular, executable file.
func isExecutableFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

// validateResumeSource resolves and validates a resume source id: it must be a
// namespaced id of THIS endpoint, name a session that exists, that has ENDED (not
// running), and whose agent type matches the requested one. It returns the source
// local id and meta, or a clear error naming the reason the resume was rejected.
func validateResumeSource(src, agentType, endpointID string, getSource func(local string) (persist.Meta, bool)) (string, persist.Meta, error) {
	ep, local, ok := protocol.ParseID(src)
	if !ok {
		return "", persist.Meta{}, fmt.Errorf("resume: source id %q is not a valid namespaced session id", src)
	}
	if endpointID != "" && ep != endpointID {
		return "", persist.Meta{}, fmt.Errorf("resume: source %q belongs to another daemon endpoint", src)
	}
	m, ok := getSource(local)
	if !ok {
		return "", persist.Meta{}, fmt.Errorf("resume: source session %q not found", local)
	}
	if m.Status.Process == status.ProcessRunning {
		return "", persist.Meta{}, fmt.Errorf("resume: source session %q is still running; resume an ended or lost session", local)
	}
	if m.AgentType != agentType {
		return "", persist.Meta{}, fmt.Errorf("resume: source agent %q does not match requested agent %q", m.AgentType, agentType)
	}
	return local, m, nil
}

// Attach opens a real SessionStream over the daemon->shim connection, via the
// single shared shim-wire implementation in internal/protocol (protocol.FromDaemon
// uses the same code internally; see protocol.NewShimStream).
func (a *coreAPI) Attach(id string) (protocol.SessionStream, error) {
	conn, caps, err := a.core.DialSession(id)
	if err != nil {
		return nil, err
	}
	return protocol.NewShimStream(conn, caps)
}

// emitStatus routes an engine-derived status change through both halves of Epic
// 10's status wiring (the Epic 11 carry-forward, now wired):
//
//   - PERSIST (G6): SetStatus writes the change back through the daemon's sole meta
//     writer, so it is durable and a reconnecting client's List reflects it.
//   - FAN OUT (Epic 6): the updated meta is pushed to the roster event channel the
//     protocol Server fans out, so Subscribe delivers it immediately (L1) rather
//     than waiting for the next roster poll.
//
// SetStatus is the choke point that also guards the process dimension (the daemon
// stays its sole authority), so an unknown/ended session persists nothing and is
// dropped here.
func (a *coreAPI) emitStatus(id string, s status.Status) {
	if err := a.core.SetStatus(id, s); err != nil {
		return // unknown/ended session: nothing to persist or fan out
	}
	m, ok := a.core.Get(id)
	if !ok {
		return
	}
	select {
	case a.events <- m:
	case <-a.stop:
	}
}

// close stops the roster poller and waits for it to exit, so the assembly leaves
// no goroutine behind.
func (a *coreAPI) close() {
	a.stopOnce.Do(func() { close(a.stop) })
	a.wg.Wait()
}

// watch samples the roster and emits a meta whenever a session's status changes
// (the core exposes no push source, so changes are observed by polling). It
// mirrors protocol.FromDaemon's watcher: dedup by status, retry a momentarily-full
// queue on the next poll (never drop a change), and prune vanished sessions so the
// seen map stays bounded.
func (a *coreAPI) watch() {
	defer a.wg.Done()
	seen := map[string]status.Status{}
	t := time.NewTicker(eventPoll)
	defer t.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-t.C:
			present := map[string]struct{}{}
			for _, m := range a.core.List() {
				present[m.ID] = struct{}{}
				if prev, ok := seen[m.ID]; ok && prev == m.Status {
					continue
				}
				select {
				case a.events <- m:
					seen[m.ID] = m.Status // mark seen ONLY once the change is queued
				case <-a.stop:
					return
				default:
					// Queue momentarily full: leave seen unadvanced so this change is
					// retried on the next poll rather than lost.
				}
			}
			for id := range seen {
				if _, ok := present[id]; !ok {
					delete(seen, id)
				}
			}
		}
	}
}
