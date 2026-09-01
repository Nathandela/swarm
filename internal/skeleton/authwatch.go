package skeleton

// THE AUTH WATCHER (ADR-024): the daemon-side component that notices a provider
// re-login and recycles the sessions it stranded.
//
// THE INCIDENT IT ENCODES (2026-09-01). Codex processes load ~/.codex/auth.json
// once at startup and hold its tokens in memory. A logout/login to another
// account rotates the stored credentials; every session started before the
// change then fails each turn with "Your access token could not be refreshed
// because you have since logged out or signed in to another account" until its
// processes are RESTARTED. The manual fix was Ctrl+X (kill) then `r` (resume) on
// every affected row. This component performs exactly that gesture, from exactly
// the same primitives (Kill; Launch carrying OptionResumeFrom; Delete), when it
// detects the account change.
//
// WHY IT LIVES HERE. Like the supervisor it needs the assembly's seams -- the
// roster, the adapter registry (which the daemon deliberately never resolves),
// and coreAPI.Launch, the ONE entry every launch passes through -- and
// internal/skeleton is the only package that holds all of them.
//
// DETECTION IS IDENTITY, NEVER MTIME. Providers rewrite their credentials file
// with fresh tokens on a routine cadence (codex stamps last_refresh on every
// refresh), so file mtime or a whole-file hash would recycle the fleet daily.
// The adapter's AuthProbe derives a digest of the ACCOUNT identity alone; the
// watcher compares it against two things: the identity it saw last (persisted,
// so a change while the daemon was down is still a change) and the identity
// each session was STAMPED with at launch (meta.AuthIdentity).
//
// UNKNOWN HOLDS EVERYTHING. A missing or unparseable credentials file is the
// logged-out window mid-relogin, not a change: the watcher holds -- no baseline
// update, no recycle -- until a parseable identity returns. Sessions with an
// empty stamp (pre-ADR-024 launches, or launches during a logged-out window)
// are judged only when a change is OBSERVED (they predate it by construction);
// absent such an observation they are never touched.
//
// THE RECYCLE IS THE OWNER'S LOCKED GESTURE (2026-09-01 decisions): fully
// automatic; a session mid-turn is DEFERRED until idle (its doomed turn is
// allowed to end by itself; the watcher never interrupts); a session that never
// captured a conversation id is left running and logged (killing it would
// destroy the only thing a manual resume needs too); after a successful resume
// the stale row is DELETED so the board keeps one row per conversation. The
// pending set is persisted BEFORE the first kill, so a crash mid-recycle
// resumes the sweep instead of forgetting it.
//
// OPT-OUT: <stateDir>/auth-watch.json {"disabled": true}. Absent means enabled
// (the feature is the default); a present-but-unreadable file means DISABLED --
// for a component that kills sessions on its own, ambiguous config must fail
// toward inaction.

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
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
	// authWatchInterval is the identity poll cadence. A re-login is a rare,
	// human-paced event; 30s keeps the recovery invisible without meaningfully
	// polling the disk.
	authWatchInterval = 30 * time.Second
	// authWatchSettingsFile / authWatchStateFile live directly under the state
	// dir (the daemon.env precedent): settings are the user's, state is ours.
	authWatchSettingsFile = "auth-watch.json"
	authWatchStateFile    = "auth-watch-state.json"
	// authRecycleExitWait bounds the wait for a killed session's monitor to
	// record the exit (resume validates its source is ended); the poll paces it.
	authRecycleExitWait = 10 * time.Second
	authRecycleExitPoll = 250 * time.Millisecond
	// authRecycleCols/Rows size the resumed PTY: the mobile default every
	// client can render (the spawn precedent); an attach resizes it live.
	authRecycleCols = 80
	authRecycleRows = 24
)

// authHomeDir / authReadFile are package-level indirections (the spawnStateDir
// precedent) so tests can point identity resolution at a fixture home.
var (
	authHomeDir  = os.UserHomeDir
	authReadFile = os.ReadFile
)

// CurrentAuthIdentity resolves the account identity agentType's on-disk
// credentials carry RIGHT NOW, or "" when there is no probe, no home, or no
// readable/parseable credentials file. "" gates conservatively everywhere it
// lands: a launch stamps no identity, the watcher holds.
func CurrentAuthIdentity(agentType string) string {
	ad, ok := registry.New(agentType)
	if !ok {
		return ""
	}
	probe, ok := adapter.AsAuthProbe(ad)
	if !ok {
		return ""
	}
	home, err := authHomeDir()
	if err != nil {
		return ""
	}
	raw, err := authReadFile(filepath.Join(home, probe.AuthCredentialsFile()))
	if err != nil {
		return ""
	}
	id, ok := probe.AuthIdentity(raw)
	if !ok {
		return ""
	}
	return id
}

// AuthProbedAgents enumerates the production providers whose adapters have a
// characterized credentials layout -- the set the watcher watches.
func AuthProbedAgents() []string {
	var out []string
	for _, name := range registry.Names() {
		if !registry.IsProduction(name) {
			continue
		}
		ad, ok := registry.New(name)
		if !ok {
			continue
		}
		if _, has := adapter.AsAuthProbe(ad); has {
			out = append(out, name)
		}
	}
	return out
}

// authWatchState is the watcher's durable memory: the last identity seen per
// agent, and the frozen stale set a detected change is still working through.
type authWatchState struct {
	Identities map[string]string   `json:"identities"`
	Pending    map[string][]string `json:"pending,omitempty"`
}

// authWatcher is the component. Every action goes through an injected seam
// (production: the coreAPI's Kill/Launch/Delete and the core's roster; fakes in
// tests), so the sweep logic is unit-testable with no daemon and no socket.
type authWatcher struct {
	stateDir   string
	endpointID string
	interval   time.Duration
	agents     []string
	identity   func(agentType string) string
	list       func() []persist.Meta
	get        func(local string) (persist.Meta, bool)
	kill       func(local string) error
	launch     func(daemon.LaunchSpec) (persist.Meta, error)
	remove     func(local string) error
	sleep      func(time.Duration)
	exitWait   time.Duration
	exitPoll   time.Duration

	state authWatchState
	// warned suppresses the per-tick repeat of the "cannot auto-resume" line: a
	// session without a captured conversation id stays pending (truthfully -- it
	// still holds stale credentials) but is announced once, not every 30s.
	warned map[string]bool

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// newAuthWatcher loads the persisted state a prior incarnation left and starts
// the poll goroutine. A missing state file is first run: baselines are
// established on the first tick and nothing predating them is judged.
func newAuthWatcher(stateDir, endpointID string, agents []string,
	identity func(agentType string) string,
	list func() []persist.Meta,
	get func(local string) (persist.Meta, bool),
	kill func(local string) error,
	launch func(daemon.LaunchSpec) (persist.Meta, error),
	remove func(local string) error) *authWatcher {
	w := &authWatcher{
		stateDir: stateDir, endpointID: endpointID, interval: authWatchInterval,
		agents: agents, identity: identity,
		list: list, get: get, kill: kill, launch: launch, remove: remove,
		sleep: time.Sleep, exitWait: authRecycleExitWait, exitPoll: authRecycleExitPoll,
		state:  authWatchState{Identities: map[string]string{}, Pending: map[string][]string{}},
		warned: map[string]bool{},
		stop:   make(chan struct{}),
	}
	w.state = loadAuthWatchState(stateDir)
	w.wg.Add(1)
	go w.run()
	return w
}

// loadAuthWatchState reads the state a prior incarnation persisted; a missing
// or unparseable file is first run (empty maps, everything baselines afresh).
func loadAuthWatchState(stateDir string) authWatchState {
	st := authWatchState{Identities: map[string]string{}, Pending: map[string][]string{}}
	raw, err := os.ReadFile(filepath.Join(stateDir, authWatchStateFile))
	if err != nil {
		return st
	}
	var loaded authWatchState
	if json.Unmarshal(raw, &loaded) != nil {
		return st
	}
	if loaded.Identities == nil {
		loaded.Identities = map[string]string{}
	}
	if loaded.Pending == nil {
		loaded.Pending = map[string][]string{}
	}
	return loaded
}

// close stops the poll goroutine and waits for an in-flight tick to finish, so
// no recycle action may start once the assembly is shutting down.
func (w *authWatcher) close() {
	w.stopOnce.Do(func() { close(w.stop) })
	w.wg.Wait()
}

func (w *authWatcher) run() {
	defer w.wg.Done()
	t := time.NewTicker(w.interval)
	defer t.Stop()
	w.tick() // baseline (or resume a pending sweep) promptly at daemon start
	for {
		select {
		case <-w.stop:
			return
		case <-t.C:
			w.tick()
		}
	}
}

// tick is one full pass: settings gate, then per-agent identity check + sweep.
// It runs on the one watcher goroutine; nothing else mutates w.state.
func (w *authWatcher) tick() {
	if w.disabled() {
		return
	}
	for _, agent := range w.agents {
		w.tickAgent(agent)
	}
}

// disabled reads the settings file fresh each tick (it is the user's file; no
// restart should be needed to flip it).
func (w *authWatcher) disabled() bool { return AuthWatchDisabled(w.stateDir) }

// AuthWatchDisabled reports whether the automatic watcher is opted out at
// stateDir -- the ONE reading, shared by the daemon's tick and `swarm relogin`
// (which acts itself exactly when the watcher will not). Missing = enabled (the
// feature is the default); present but unreadable/unparseable = disabled,
// because ambiguous config must fail toward inaction for a component that kills
// sessions on its own.
func AuthWatchDisabled(stateDir string) bool {
	raw, err := os.ReadFile(filepath.Join(stateDir, authWatchSettingsFile))
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("authwatch: settings unreadable, holding: %v", err)
			return true
		}
		return false
	}
	var s struct {
		Disabled bool `json:"disabled"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		log.Printf("authwatch: settings unparseable, holding: %v", err)
		return true
	}
	return s.Disabled
}

// SetAuthWatchDisabled records the opt-out (`swarm relogin --auto off`) or
// removes it (`--auto on`; absence IS the enabled state, so re-enabling leaves
// no file behind).
func SetAuthWatchDisabled(stateDir string, disabled bool) error {
	path := filepath.Join(stateDir, authWatchSettingsFile)
	if !disabled {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return os.WriteFile(path, []byte("{\"disabled\": true}\n"), 0o600)
}

func (w *authWatcher) tickAgent(agent string) {
	id := w.identity(agent)
	if id == "" {
		return // unknown (logged-out window, unreadable file): hold everything
	}
	prev := w.state.Identities[agent]
	if prev == "" {
		w.state.Identities[agent] = id
		w.saveState()
		return // baseline established; nothing predating it is judged
	}
	dirty := false
	if prev != id {
		// The account changed. Freeze the stale set NOW -- every running session
		// of this agent not launched under the new identity, EMPTY STAMPS
		// INCLUDED (a pre-ADR-024 launch predates the change by construction).
		for _, m := range w.list() {
			if m.AgentType == agent && m.Status.Process == status.ProcessRunning && m.AuthIdentity != id {
				dirty = w.addPending(agent, m.ID) || dirty
			}
		}
		w.state.Identities[agent] = id
		dirty = true
	}
	// Stamped mismatches are ground truth independent of observing the change
	// (a re-login while the daemon was down, or before this build first ran,
	// left stamps disagreeing with the current identity): sweep them in too.
	for _, m := range w.list() {
		if m.AgentType == agent && m.Status.Process == status.ProcessRunning && m.AuthIdentity != "" && m.AuthIdentity != id {
			dirty = w.addPending(agent, m.ID) || dirty
		}
	}
	if dirty {
		w.saveState() // BEFORE any kill: a crash mid-recycle resumes the sweep
	}
	w.workPending(agent, id)
}

// addPending records local in agent's pending set (dedup'd); reports whether it
// was new.
func (w *authWatcher) addPending(agent, local string) bool {
	for _, p := range w.state.Pending[agent] {
		if p == local {
			return false
		}
	}
	w.state.Pending[agent] = append(w.state.Pending[agent], local)
	return true
}

// workPending walks agent's pending set: recycle what is idle, defer what is
// mid-turn, drop what is gone, current, or unresumable.
func (w *authWatcher) workPending(agent, id string) {
	pending := w.state.Pending[agent]
	if len(pending) == 0 {
		return
	}
	var keep []string
	for _, local := range pending {
		m, ok := w.get(local)
		switch {
		case !ok || m.Status.Process != status.ProcessRunning:
			// Gone, or ended by other hands -- not ours to resurrect.
			delete(w.warned, local)
		case m.AuthIdentity == id:
			// Already relaunched under the current account.
			delete(w.warned, local)
		case m.Status.Turn != status.TurnIdle:
			// Mid-turn (or unclassified): the owner's locked rule is DEFER. The
			// doomed turn ends by itself; the next tick retries.
			keep = append(keep, local)
		case m.ConversationID == "":
			// No captured conversation id: a kill would destroy the only thing a
			// MANUAL resume needs too. It stays pending (it truthfully still
			// holds stale credentials) but is left running for the user, and the
			// line below is said once, not every tick.
			if !w.warned[local] {
				w.warned[local] = true
				log.Printf("authwatch: %s session %s (%s) holds stale credentials but captured no conversation id; leaving it for a manual restart", agent, local, m.Name)
			}
			keep = append(keep, local)
		default:
			if w.recycle(agent, m) {
				keep = append(keep, local)
			}
		}
	}
	if len(keep) == 0 {
		delete(w.state.Pending, agent)
	} else {
		w.state.Pending[agent] = keep
	}
	w.saveState()
}

// recycle performs the owner's manual gesture -- kill, wait for the exit to be
// recorded, resume-as-new-session, delete the stale row -- and reports whether
// the session should stay pending (true = retry next tick).
func (w *authWatcher) recycle(agent string, m persist.Meta) (retry bool) {
	local := m.ID
	if err := w.kill(local); err != nil {
		log.Printf("authwatch: kill stale %s session %s (%s): %v", agent, local, m.Name, err)
		return true
	}
	deadline := time.Now().Add(w.exitWait)
	for {
		cur, ok := w.get(local)
		if !ok || cur.Status.Process != status.ProcessRunning {
			break
		}
		if time.Now().After(deadline) {
			log.Printf("authwatch: %s session %s did not record its exit in time; retrying next tick", agent, local)
			return true
		}
		w.sleep(w.exitPoll)
	}
	fresh, err := w.launch(daemon.LaunchSpec{
		AgentType: agent,
		Name:      m.Name, // the resumed row keeps its label (the TUI resume precedent)
		Cwd:       m.Cwd,
		Cols:      authRecycleCols,
		Rows:      authRecycleRows,
		// ClientEnv stays nil ON PURPOSE: coreAPI.Launch resolves it to the
		// daemon-saved environment (LaunchPolicyEnv), exactly as a remote launch
		// does -- there is no client terminal behind this launch.
		Options: map[string]string{protocol.OptionResumeFrom: w.endpointID + "/" + local},
	})
	if err != nil {
		log.Printf("authwatch: resume %s session %s (%s): %v -- the ended row remains for a manual resume", agent, local, m.Name, err)
		return false
	}
	// The owner's locked rule: after a successful resume the stale row is
	// deleted -- one row per conversation. The new row's ResumedFrom keeps the
	// lineage.
	if err := w.remove(local); err != nil {
		log.Printf("authwatch: delete recycled %s session %s: %v", agent, local, err)
	}
	log.Printf("authwatch: recycled %s session %s -> %s (%s): credentials changed account", agent, local, fresh.ID, m.Name)
	return false
}

// saveState persists the watcher's memory atomically (tmp + rename), 0600 like
// every other daemon-authored state file.
func (w *authWatcher) saveState() {
	raw, err := json.MarshalIndent(w.state, "", "  ")
	if err != nil {
		log.Printf("authwatch: marshal state: %v", err)
		return
	}
	path := filepath.Join(w.stateDir, authWatchStateFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		log.Printf("authwatch: write state: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("authwatch: commit state: %v", err)
	}
}
