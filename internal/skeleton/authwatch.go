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
// DESTRUCTION NEVER OUTRUNS RECOVERY (audit round: Fable H1/H3, codex 4).
// Before any kill the watcher (a) proves the resume is composable -- the agent
// binary resolves on the SESSION's own saved environment, the conversation id
// is captured -- and (b) durably records the kill as its own (state.Killed), so
// the one hazardous asymmetry an unattended session-killer could have (a kill
// that succeeds paired with a resume that is lost to a crash, a timeout, or an
// unresolvable binary) cannot arise: an ended session carrying the killed mark
// is a resume OWED, completed on a later tick or by the next incarnation, and
// never dropped as "ended by other hands". A claim that cannot be persisted
// forbids the kill outright.
//
// THE RECYCLE IS THE OWNER'S LOCKED GESTURE (2026-09-01 decisions): fully
// automatic; a session mid-turn -- or mid-INTERACTION: a permission prompt sits
// on an idle turn -- is DEFERRED until quiet (the watcher never interrupts);
// a session that never captured a conversation id is left running and warned
// once (killing it would destroy the only thing a manual resume needs too); a
// WORKTREE-ISOLATED session is never auto-recycled at all (the resume cannot
// follow the conversation into its checkout, and the auto-delete would `git
// worktree remove --force` uncommitted agent work -- audit C1); after a
// successful resume the stale row is DELETED so the board keeps one row per
// conversation. The resumed session gets the source's OWN saved environment
// and lineage (SpawnedFrom/Supervision), not the daemon's.
//
// THE FIRST TICK AFTER DAEMON START NEVER KILLS. Reconciled sessions are
// seeded from persisted status and the live backends are still catching up, so
// an "idle" read in that window can be a session that went active while the
// daemon was down (codex finding 6). The first pass baselines, freezes, and
// completes OWED resumes only; new kills start from the second tick, by which
// point the engine's poll has reclassified everything.
//
// OPT-OUT: <stateDir>/auth-watch.json {"disabled": true}. Absent means enabled
// (the feature is the default); a present-but-unreadable file means DISABLED --
// for a component that kills sessions on its own, ambiguous config must fail
// toward inaction.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	// A timeout is NOT a drop: the killed mark keeps the resume owed.
	authRecycleExitWait = 10 * time.Second
	authRecycleExitPoll = 250 * time.Millisecond
	// authRecycleCols/Rows size the resumed PTY: the mobile default every
	// client can render (the spawn precedent); an attach resizes it live.
	authRecycleCols = 80
	authRecycleRows = 24
	// authCredentialsMaxBytes bounds the credentials read (the detect model-probe
	// size-cap precedent): a credentials file is small; anything huge is not one.
	authCredentialsMaxBytes = 1 << 20
)

// authHomeDir is a package-level indirection (the spawnStateDir precedent) so
// tests can point identity resolution at a fixture home.
var authHomeDir = os.UserHomeDir

// readCredentials reads a credentials file conservatively: regular files only
// (a FIFO would park the watcher -- and daemon shutdown -- forever) and size-
// capped (codex finding 9).
func readCredentials(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("credentials at %s are not a regular file", path)
	}
	if info.Size() > authCredentialsMaxBytes {
		return nil, fmt.Errorf("credentials at %s are implausibly large (%d bytes)", path, info.Size())
	}
	return os.ReadFile(path)
}

// CurrentAuthIdentity resolves the account identity agentType's on-disk
// credentials carry RIGHT NOW under the daemon's own home, or "" when there is
// no probe, no home, or no readable/parseable credentials file. "" gates
// conservatively everywhere it lands: a launch stamps no identity, the watcher
// holds.
func CurrentAuthIdentity(agentType string) string {
	home, err := authHomeDir()
	if err != nil {
		return ""
	}
	return AuthIdentityForHome(agentType, home)
}

// AuthIdentityForHome is CurrentAuthIdentity against an explicit home -- the
// launch stamp uses the HOME the agent will actually inherit (a session with a
// per-session HOME reads that home's credentials, not the daemon's), while the
// watcher polls the daemon's.
func AuthIdentityForHome(agentType, home string) string {
	ad, ok := registry.New(agentType)
	if !ok {
		return ""
	}
	probe, ok := adapter.AsAuthProbe(ad)
	if !ok {
		return ""
	}
	raw, err := readCredentials(filepath.Join(home, probe.AuthCredentialsFile()))
	if err != nil {
		return ""
	}
	id, ok := probe.AuthIdentity(raw)
	if !ok {
		return ""
	}
	return id
}

// launchAuthIdentity resolves the identity stamp for one launch: the HOME the
// agent will actually inherit (from the resolved launch env) wins over the
// daemon's own, so a session with a per-session HOME is stamped with the
// account its codex will really load (audit M6).
func launchAuthIdentity(agentType string, env []string) string {
	home := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, "HOME=") {
			home = kv[len("HOME="):]
		}
	}
	if home == "" {
		h, err := authHomeDir()
		if err != nil {
			return ""
		}
		home = h
	}
	return AuthIdentityForHome(agentType, home)
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
// agent, the frozen stale set a detected change is still working through, and
// the sessions whose kill was OURS -- for which a resume is owed across
// timeouts, restarts and crashes (audit H1).
type authWatchState struct {
	Identities map[string]string   `json:"identities"`
	Pending    map[string][]string `json:"pending,omitempty"`
	Killed     map[string]bool     `json:"killed,omitempty"`
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
	resolve    func(name string, env []string) (string, error)
	// unsafe covers live authority/effect state absent from persisted Status: owner/remote
	// controls and ContextGuard's provider effect window. withRecycleFence queues the final
	// revalidation+kill behind every already-admitted composer operation and keeps later ones
	// behind it, closing the check-to-kill window on the durable phone send plane.
	unsafe           func(local string) bool
	withRecycleFence func(local string, attempt func() error) error
	pause            func(time.Duration)
	exitWait         time.Duration
	exitPoll         time.Duration

	state authWatchState
	// settled flips after the first full tick: reconciled sessions are seeded
	// from persisted status, so the first pass never STARTS a kill (owed
	// resumes still complete).
	settled bool
	// warned suppresses per-tick repeats of the hold explanations (no
	// conversation id, worktree-isolated, unresolvable binary): each is said
	// once per session, not every 30s.
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
	remove func(local string) error,
	unsafe func(local string) bool,
	withRecycleFence func(local string, attempt func() error) error) *authWatcher {
	w := &authWatcher{
		stateDir: stateDir, endpointID: endpointID, interval: authWatchInterval,
		agents: agents, identity: identity,
		list: list, get: get, kill: kill, launch: launch, remove: remove,
		unsafe: unsafe, withRecycleFence: withRecycleFence,
		resolve:  lookPathIn,
		exitWait: authRecycleExitWait, exitPoll: authRecycleExitPoll,
		warned: map[string]bool{},
		stop:   make(chan struct{}),
	}
	// pause is a stop-aware sleep, so a shutdown never waits behind an exit
	// poll (audit M5); tests replace it with a no-op.
	w.pause = func(d time.Duration) {
		select {
		case <-w.stop:
		case <-time.After(d):
		}
	}
	w.state = loadAuthWatchState(stateDir)
	w.wg.Add(1)
	go w.run()
	return w
}

var errAuthRecycleUnsafe = errors.New("authwatch: session became unsafe to recycle")

func (w *authWatcher) sessionUnsafe(local string) bool {
	return w.unsafe != nil && w.unsafe(local)
}

func (w *authWatcher) fencedRecycleAttempt(local string, attempt func() error) error {
	if w.withRecycleFence == nil {
		return attempt()
	}
	return w.withRecycleFence(local, attempt)
}

// loadAuthWatchState reads the state a prior incarnation persisted; a missing
// or unparseable file is first run (empty maps, everything baselines afresh).
func loadAuthWatchState(stateDir string) authWatchState {
	st := authWatchState{Identities: map[string]string{}, Pending: map[string][]string{}, Killed: map[string]bool{}}
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
	if loaded.Killed == nil {
		loaded.Killed = map[string]bool{}
	}
	return loaded
}

// close stops the watcher and waits out an in-flight tick. The loops inside a
// tick are stop-aware (stopping/pause), so the wait is bounded by one action,
// not one sweep. Called FIRST in Daemon.Close: the watcher launches sessions
// through the full assembly, so nothing of the assembly may be torn down while
// it can still act (codex finding 7).
func (w *authWatcher) close() {
	w.stopOnce.Do(func() { close(w.stop) })
	w.wg.Wait()
}

// stopping reports whether close has begun; sweeps abort between actions.
func (w *authWatcher) stopping() bool {
	select {
	case <-w.stop:
		return true
	default:
		return false
	}
}

func (w *authWatcher) run() {
	defer w.wg.Done()
	t := time.NewTicker(w.interval)
	defer t.Stop()
	w.tick() // baseline + complete owed resumes promptly at daemon start
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
		if w.stopping() {
			return
		}
		w.tickAgent(agent)
	}
	w.settled = true
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
		if err := w.saveState(); err != nil {
			log.Printf("authwatch: persist baseline: %v", err)
		}
		return // baseline established; nothing predating it is judged
	}
	dirty := false
	if prev != id {
		// The account changed. Freeze the stale set NOW -- every running session
		// of this agent not launched under the new identity, EMPTY STAMPS
		// INCLUDED (a pre-ADR-024 launch predates the change by construction).
		for _, m := range w.list() {
			if m.AgentType == agent && m.Status.Process == status.ProcessRunning && m.AuthIdentity != id {
				w.addPending(agent, m.ID)
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
		if err := w.saveState(); err != nil {
			// The freeze could not be made durable; kills below re-persist their
			// own claim and abort if that fails, so continuing is safe -- but say
			// so, because a crash now would re-derive the set from stamps only.
			log.Printf("authwatch: persist pending set: %v", err)
		}
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

// forget clears every per-session record when an entry leaves the pending set.
func (w *authWatcher) forget(local string) {
	delete(w.state.Killed, local)
	for k := range w.warned {
		if k == local || len(k) > len(local) && k[len(k)-len(local):] == local {
			delete(w.warned, k)
		}
	}
}

// once says a hold explanation a single time per (kind, session).
func (w *authWatcher) once(key, format string, args ...any) {
	if w.warned[key] {
		return
	}
	w.warned[key] = true
	log.Printf(format, args...)
}

// workPending walks agent's pending set: complete owed resumes, recycle what is
// quiet, defer what is mid-turn or mid-interaction, hold what cannot be
// recycled safely, drop what is gone or current.
func (w *authWatcher) workPending(agent, id string) {
	pending := w.state.Pending[agent]
	if len(pending) == 0 {
		return
	}
	var keep []string
	for i, local := range pending {
		if w.stopping() {
			keep = append(keep, pending[i:]...) // shutdown: the rest stays owed
			break
		}
		m, ok := w.get(local)
		switch {
		case !ok:
			// Deleted entirely -- nothing left to resume.
			w.forget(local)
		case m.Status.Process != status.ProcessRunning && w.state.Killed[local]:
			// OUR kill: the resume is owed (audit H1). A crash or an exit-wait
			// timeout between the kill and the resume lands here on a later tick
			// -- or in the next incarnation -- and completes the gesture. The
			// daemon-side resume dedup makes a replay after a crash-between-
			// launch-and-delete idempotent.
			if w.resumeEnded(agent, m) {
				keep = append(keep, local)
			} else {
				w.forget(local)
			}
		case m.Status.Process != status.ProcessRunning:
			// Ended by other hands -- not ours to resurrect.
			w.forget(local)
		case m.AuthIdentity == id:
			// Already relaunched under the current account.
			w.forget(local)
		case m.LaunchOptions[protocol.OptionWorktree] == "true":
			// Worktree-isolated (audit C1): an automatic resume cannot follow the
			// conversation into its checkout, and the auto-delete would `git
			// worktree remove --force` uncommitted agent work. Manual only.
			w.once("worktree:"+local, "authwatch: %s session %s (%s) holds stale credentials but is worktree-isolated; recycle it manually", agent, local, m.Name)
			keep = append(keep, local)
		case m.Status.Turn != status.TurnIdle || m.Status.Interaction != status.InteractionNone:
			// Mid-turn, unclassified, or sitting on a permission prompt (an
			// interaction rides an IDLE turn -- audit M3): the owner's locked
			// rule is DEFER. The next tick retries.
			keep = append(keep, local)
		case w.sessionUnsafe(local):
			// Persisted turn/interaction state cannot see controller leases, a recent
			// phone driver, ContextGuard's post-write effect window, or an admitted
			// composer operation. None may be interrupted by an automatic credential
			// recycle; the final boundary rechecks this inside the composer lane too.
			keep = append(keep, local)
		case m.ConversationID == "":
			// No captured conversation id: a kill would destroy the only thing a
			// MANUAL resume needs too. It stays pending (it truthfully still
			// holds stale credentials) but is left running for the user.
			w.once("noconv:"+local, "authwatch: %s session %s (%s) holds stale credentials but captured no conversation id; leaving it for a manual restart", agent, local, m.Name)
			keep = append(keep, local)
		case !w.settled:
			// First tick after daemon start: persisted status may lag reality
			// (codex finding 6). No NEW kill until the engine has had a full
			// interval to reclassify; the entry stays owed.
			keep = append(keep, local)
		default:
			if w.recycle(agent, m) {
				keep = append(keep, local)
			} else {
				w.forget(local)
			}
		}
	}
	if len(keep) == 0 {
		delete(w.state.Pending, agent)
	} else {
		w.state.Pending[agent] = keep
	}
	if err := w.saveState(); err != nil {
		log.Printf("authwatch: persist pending set: %v", err)
	}
}

// recycle performs the owner's manual gesture -- claim, kill, wait for the exit
// to be recorded, resume-as-new-session, delete the stale row -- and reports
// whether the session should stay pending (true = retry next tick).
func (w *authWatcher) recycle(agent string, m persist.Meta) (retry bool) {
	local := m.ID
	// FEASIBILITY BEFORE DESTRUCTION (audit H3): the resume must be provably
	// composable before anything is killed. The binary is resolved against the
	// SESSION's own saved environment -- the same env the resume will launch
	// with -- because the daemon's env may lack the agent's PATH entirely (the
	// original daemon-PATH incident).
	if ad, ok := registry.New(agent); ok {
		if _, err := w.resolve(ad.Binary(), m.Env); err != nil {
			w.once("binary:"+local, "authwatch: %s session %s (%s): agent binary does not resolve on the session's environment (%v); holding the recycle", agent, local, m.Name, err)
			return true
		}
	}
	// THE CLAIM: record the kill as ours -- durably -- before signalling it. A
	// claim that cannot be persisted forbids the kill: otherwise a crash in the
	// next instant would strand a session we could not prove we killed.
	w.state.Killed[local] = true
	if err := w.saveState(); err != nil {
		delete(w.state.Killed, local)
		log.Printf("authwatch: cannot persist the kill claim for %s (%v); not killing", local, err)
		return true
	}
	// Serialize the final revalidation+kill with the session's composer FIFO. Existing sends
	// finish first; later sends queue behind the recycle. Controller and ContextGuard state is
	// re-read INSIDE that fence because neither is represented in persisted Status.
	killErr := w.fencedRecycleAttempt(local, func() error {
		cur, ok := w.get(local)
		if !ok || cur.Status.Process != status.ProcessRunning ||
			cur.Status.Turn != status.TurnIdle || cur.Status.Interaction != status.InteractionNone ||
			w.sessionUnsafe(local) {
			return errAuthRecycleUnsafe
		}
		return w.kill(local)
	})
	if killErr != nil {
		delete(w.state.Killed, local)
		if err := w.saveState(); err != nil {
			log.Printf("authwatch: persist claim release for %s: %v", local, err)
		}
		if !errors.Is(killErr, errAuthRecycleUnsafe) {
			log.Printf("authwatch: kill stale %s session %s (%s): %v", agent, local, m.Name, killErr)
		}
		return true
	}
	deadline := time.Now().Add(w.exitWait)
	for {
		cur, ok := w.get(local)
		if !ok || cur.Status.Process != status.ProcessRunning {
			break
		}
		if w.stopping() {
			return true // shutdown mid-recycle: the killed mark keeps the resume owed
		}
		if time.Now().After(deadline) {
			log.Printf("authwatch: %s session %s has not recorded its exit yet; its resume stays owed and completes on a later tick", agent, local)
			return true
		}
		w.pause(w.exitPoll)
	}
	return w.resumeEnded(agent, m)
}

// resumeEnded is the second half of the gesture, also entered directly for an
// owed resume (a session we killed whose replacement never launched): launch
// the replacement from the source's own environment and lineage, then delete
// the stale row.
func (w *authWatcher) resumeEnded(agent string, m persist.Meta) (retry bool) {
	local := m.ID
	fresh, err := w.launch(daemon.LaunchSpec{
		AgentType: agent,
		Name:      m.Name, // the resumed row keeps its label (the TUI resume precedent)
		Cwd:       m.Cwd,
		Cols:      authRecycleCols,
		Rows:      authRecycleRows,
		// The SESSION's saved environment, not the daemon's (audit H3/M6): its
		// PATH is what resolved this agent originally, and its HOME/API key are
		// what the conversation has been running under. coreAPI.Launch re-filters
		// it through the same allowlist as any client env.
		ClientEnv: m.Env,
		// Lineage rides along (audit M2): a supervised handoff child stays a
		// supervised handoff child under its new id.
		SpawnedFrom: m.SpawnedFrom,
		SpawnIntent: m.SpawnIntent,
		Supervision: m.Supervision,
		Options:     map[string]string{protocol.OptionResumeFrom: w.endpointID + "/" + local},
	})
	if err != nil {
		log.Printf("authwatch: resume %s session %s (%s): %v -- the ended row remains for a manual resume", agent, local, m.Name, err)
		return false
	}
	// The owner's locked rule: after a successful resume the stale row is
	// deleted -- one row per conversation. The new row's ResumedFrom keeps the
	// lineage; a delete failure leaves a visible ended row, which is benign.
	if err := w.remove(local); err != nil {
		log.Printf("authwatch: delete recycled %s session %s: %v", agent, local, err)
	}
	log.Printf("authwatch: recycled %s session %s -> %s (%s): credentials changed account", agent, local, fresh.ID, m.Name)
	return false
}

// saveState persists the watcher's memory atomically (tmp + rename), 0600 like
// every other daemon-authored state file. Callers that are about to DESTROY
// something treat an error as a veto.
func (w *authWatcher) saveState() error {
	raw, err := json.MarshalIndent(w.state, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(w.stateDir, authWatchStateFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
