package skeleton

import (
	"errors"
	"fmt"
	"net"
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
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/wire"
)

const (
	// eventPoll is how often the roster is sampled for status changes (well within
	// the L1 <=1 s bound). It mirrors protocol.FromDaemon's cadence.
	eventPoll = 200 * time.Millisecond
	// shimAttachTimeout bounds waiting for a shim's snapshot on attach.
	shimAttachTimeout = 10 * time.Second
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
//     conversation, and the new session's ResumedFrom links back to the source. The
//     reserved "fake" agent (no adapter) relaunches fresh, still linked.
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
	if src := spec.Options[protocol.OptionResumeFrom]; src != "" {
		local, srcMeta, err := validateResumeSource(src, spec.AgentType, endpointID, getSource)
		if err != nil {
			return daemon.LaunchSpec{}, err
		}
		spec.ResumedFrom = local
		if ad, ok := registry.New(spec.AgentType); ok {
			argv, rerr := ad.Resume(adapter.ResumeSpec{
				Cwd:            spec.Cwd,
				ConversationID: srcMeta.ConversationID,
				Options:        spec.Options,
			})
			if rerr != nil {
				return daemon.LaunchSpec{}, fmt.Errorf("resume: compose argv: %w", rerr)
			}
			if len(argv) > 0 { // the resume argv carries the source's conversation id
				resolved, lerr := resolveArgv0(argv, spec.ClientEnv, lookPath)
				if lerr != nil {
					return daemon.LaunchSpec{}, fmt.Errorf("resume: %w", lerr)
				}
				spec.Argv = resolved
			}
		}
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

// Attach opens a real SessionStream over the daemon->shim connection.
func (a *coreAPI) Attach(id string) (protocol.SessionStream, error) {
	conn, err := a.core.DialSession(id)
	if err != nil {
		return nil, err
	}
	return newShimStream(conn)
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

// ---------------------------------------------------------------------------
// shimStream — a protocol.SessionStream backed by a live daemon->shim connection.
// It mirrors protocol.FromDaemon's shim stream (that type is unexported), so the
// assembly can serve attach without depending on FromDaemon's bundled poller.
// ---------------------------------------------------------------------------

type shimStream struct {
	conn   net.Conn
	snap   []byte
	frames chan []byte

	writeMu   sync.Mutex
	closeOnce sync.Once
	done      chan struct{}
}

// newShimStream sends the attach request over an already-helloed shim connection
// and reads the one snapshot frame the shim emits first (S10), then starts
// streaming live output frames.
func newShimStream(conn net.Conn) (*shimStream, error) {
	body, err := shimwire.Encode(shimwire.Control{Type: shimwire.TypeAttach})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := wire.WriteFrame(conn, wire.TControl, body); err != nil {
		conn.Close()
		return nil, err
	}

	_ = conn.SetReadDeadline(time.Now().Add(shimAttachTimeout))
	snap, err := readSnapshot(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Time{})

	st := &shimStream{
		conn:   conn,
		snap:   snap,
		frames: make(chan []byte, 256),
		done:   make(chan struct{}),
	}
	go st.readLoop()
	return st, nil
}

// readSnapshot reads frames until the shim's single TSnapshot arrives.
func readSnapshot(conn net.Conn) ([]byte, error) {
	for {
		typ, payload, err := wire.ReadFrame(conn)
		if err != nil {
			return nil, err
		}
		if typ == wire.TSnapshot {
			return payload, nil
		}
		if typ == wire.TDataOut {
			return nil, errors.New("skeleton: shim sent a live frame before the snapshot")
		}
	}
}

func (st *shimStream) readLoop() {
	defer close(st.frames)
	for {
		typ, payload, err := wire.ReadFrame(st.conn)
		if err != nil {
			return
		}
		switch typ {
		case wire.TDataOut:
			select {
			case st.frames <- payload:
			case <-st.done:
				return
			}
		case wire.TControl:
			c, derr := shimwire.Decode(payload)
			if derr == nil && c.Type == shimwire.TypeExitReport {
				return // session ended
			}
		}
	}
}

func (st *shimStream) Snapshot() []byte      { return st.snap }
func (st *shimStream) Frames() <-chan []byte { return st.frames }

func (st *shimStream) Input(p []byte) error {
	st.writeMu.Lock()
	defer st.writeMu.Unlock()
	return wire.WriteFrame(st.conn, wire.TDataIn, p)
}

func (st *shimStream) Resize(cols, rows int) error {
	body, err := shimwire.Encode(shimwire.Control{Type: shimwire.TypeResize, Cols: cols, Rows: rows})
	if err != nil {
		return err
	}
	st.writeMu.Lock()
	defer st.writeMu.Unlock()
	return wire.WriteFrame(st.conn, wire.TControl, body)
}

func (st *shimStream) Close() error {
	st.closeOnce.Do(func() {
		close(st.done)
		st.conn.Close()
	})
	return nil
}
