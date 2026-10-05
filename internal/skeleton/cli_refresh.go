package skeleton

// Automatic provider-CLI refresh is one reason handled by authWatcher's single
// lifecycle coordinator. It deliberately has no goroutine of its own: auth
// rotation and CLI replacement must share one durable kill claim and one set of
// composer/owner fences or they can both replace the same session.

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

const (
	cliRefreshSettingsFile = "cli-refresh.json"
	cliRefreshPending      = "pending"
	cliRefreshWaiting      = "waiting"
	cliRefreshBlocked      = "blocked"
	cliRefreshComplete     = "complete"
	// A tick may execute a bounded number of env-specific version probes. A large
	// roster therefore cannot turn one 30-second watcher pass into an unbounded
	// train of subprocesses; later sessions are picked up on later passes.
	maxCLIRefreshProbesPerTick = 8
	refreshRetryBase           = 30 * time.Second
	refreshRetryMax            = 10 * time.Minute
)

type recycleRetry struct {
	Attempts    int       `json:"attempts"`
	NextAttempt time.Time `json:"next_attempt,omitempty"`
}

type cliRefreshCandidate struct {
	Identity     persist.CLIIdentity `json:"identity"`
	Observations int                 `json:"observations"`
}

type cliRefreshRecord struct {
	AgentType      string              `json:"agent_type"`
	Target         persist.CLIIdentity `json:"target"`
	ReplacementID  string              `json:"replacement_id,omitempty"`
	State          string              `json:"state"`
	LastError      string              `json:"last_error,omitempty"`
	HealthDeadline time.Time           `json:"health_deadline,omitempty"`
	UpdatedAt      time.Time           `json:"updated_at"`
}

// CLIRefreshRecord is the stable, non-secret status view exposed to the CLI.
type CLIRefreshRecord struct {
	SourceID      string `json:"source_id"`
	AgentType     string `json:"agent_type"`
	State         string `json:"state"`
	TargetVersion string `json:"target_version"`
	ReplacementID string `json:"replacement_id,omitempty"`
	LastError     string `json:"last_error,omitempty"`
}

// CLIRefreshReport describes the coordinator's durable view. Frozen means the
// shared state document could not be decoded, so neither auth nor CLI refresh
// will take a destructive action until the document is repaired.
type CLIRefreshReport struct {
	Disabled bool               `json:"disabled"`
	Frozen   bool               `json:"frozen"`
	Error    string             `json:"error,omitempty"`
	Records  []CLIRefreshRecord `json:"records"`
}

func emptyAuthWatchState() authWatchState {
	return authWatchState{
		Identities: map[string]string{}, Pending: map[string][]string{},
		Killed: map[string]bool{}, Candidates: map[string]string{},
		Retries: map[string]recycleRetry{}, CLI: map[string]cliRefreshRecord{},
		CLICandidates: map[string]cliRefreshCandidate{},
	}
}

func (w *authWatcher) ensureStateMaps() {
	if w.state.Identities == nil {
		w.state.Identities = map[string]string{}
	}
	if w.state.Pending == nil {
		w.state.Pending = map[string][]string{}
	}
	if w.state.Killed == nil {
		w.state.Killed = map[string]bool{}
	}
	if w.state.Candidates == nil {
		w.state.Candidates = map[string]string{}
	}
	if w.state.Retries == nil {
		w.state.Retries = map[string]recycleRetry{}
	}
	if w.state.CLI == nil {
		w.state.CLI = map[string]cliRefreshRecord{}
	}
	if w.state.CLICandidates == nil {
		w.state.CLICandidates = map[string]cliRefreshCandidate{}
	}
}

func validateAuthWatchState(st authWatchState) error {
	if err := validateAccountRecoveryState(st); err != nil {
		return err
	}
	for agent, sources := range st.Pending {
		if agent == "" {
			return fmt.Errorf("authwatch: pending set has an empty provider")
		}
		for _, source := range sources {
			if !persist.ValidID(source) {
				return fmt.Errorf("authwatch: pending set has an invalid source")
			}
		}
	}
	for source, claimed := range st.Killed {
		if !persist.ValidID(source) || !claimed {
			return fmt.Errorf("authwatch: invalid kill claim")
		}
	}
	for source, replacement := range st.Candidates {
		if !persist.ValidID(source) || !persist.ValidID(replacement) {
			return fmt.Errorf("authwatch: invalid replacement binding")
		}
	}
	for source, retry := range st.Retries {
		if !persist.ValidID(source) || retry.Attempts < 0 {
			return fmt.Errorf("authwatch: invalid retry record")
		}
	}
	for source, candidate := range st.CLICandidates {
		if !persist.ValidID(source) || !validCLIIdentity(&candidate.Identity) || candidate.Observations < 1 || candidate.Observations > 2 {
			return fmt.Errorf("authwatch: invalid CLI candidate for %q", source)
		}
	}
	for source, rec := range st.CLI {
		if !persist.ValidID(source) || !refreshProvider(rec.AgentType) || !validCLIIdentity(&rec.Target) {
			return fmt.Errorf("authwatch: invalid CLI refresh record for %q", source)
		}
		switch rec.State {
		case cliRefreshPending, cliRefreshWaiting, cliRefreshBlocked, cliRefreshComplete:
		default:
			return fmt.Errorf("authwatch: invalid CLI refresh state for %q", source)
		}
		if rec.ReplacementID != "" && !persist.ValidID(rec.ReplacementID) {
			return fmt.Errorf("authwatch: invalid CLI replacement for %q", source)
		}
		if rec.State == cliRefreshComplete && rec.ReplacementID == "" {
			return fmt.Errorf("authwatch: completed CLI refresh %q has no replacement", source)
		}
	}
	return nil
}

func cliRefreshSetting(stateDir string) (bool, error) {
	raw, err := os.ReadFile(filepath.Join(stateDir, cliRefreshSettingsFile))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return true, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return true, err
	}
	if len(fields) != 1 {
		return true, fmt.Errorf("cli-refresh: settings must contain exactly disabled")
	}
	value, ok := fields["disabled"]
	if !ok {
		return true, fmt.Errorf("cli-refresh: settings are missing disabled")
	}
	var disabled bool
	if err := json.Unmarshal(value, &disabled); err != nil || string(value) == "null" {
		if err == nil {
			err = fmt.Errorf("disabled must be a boolean")
		}
		return true, err
	}
	return disabled, nil
}

// CLIRefreshDisabled is the one settings read used by the watcher and CLI.
// Missing means enabled; ambiguous settings fail closed.
func CLIRefreshDisabled(stateDir string) bool {
	disabled, err := cliRefreshSetting(stateDir)
	if err != nil {
		log.Printf("cli-refresh: settings unreadable, holding: %v", err)
		return true
	}
	return disabled
}

// SetCLIRefreshDisabled records the independent automatic-refresh opt-out.
func SetCLIRefreshDisabled(stateDir string, disabled bool) error {
	path := filepath.Join(stateDir, cliRefreshSettingsFile)
	if !disabled {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("{\"disabled\": true}\n"), 0o600)
}

// CLIRefreshStatus reads only durable coordinator state; it never probes or
// starts a provider CLI.
func CLIRefreshStatus(stateDir string) (CLIRefreshReport, error) {
	disabled, settingsErr := cliRefreshSetting(stateDir)
	st, stateErr := loadAuthWatchStateChecked(stateDir)
	report := CLIRefreshReport{Disabled: disabled, Frozen: stateErr != nil}
	if settingsErr != nil {
		report.Disabled = true
		report.Error = settingsErr.Error()
	}
	if stateErr != nil {
		if report.Error != "" {
			report.Error += "; "
		}
		report.Error += stateErr.Error()
	}
	for source, rec := range st.CLI {
		report.Records = append(report.Records, CLIRefreshRecord{
			SourceID: source, AgentType: rec.AgentType, State: rec.State,
			TargetVersion: rec.Target.Version, ReplacementID: rec.ReplacementID,
			LastError: rec.LastError,
		})
	}
	sort.Slice(report.Records, func(i, j int) bool { return report.Records[i].SourceID < report.Records[j].SourceID })
	if settingsErr != nil {
		return report, settingsErr
	}
	return report, stateErr
}

func readCLIObservation(stateDir, local string) *persist.CLIIdentity {
	return shim.ReadCLIObservation(filepath.Join(stateDir, local))
}

func validCLIIdentity(id *persist.CLIIdentity) bool {
	return id != nil && id.Path != "" && id.Version != "" && id.Fingerprint != ""
}

func refreshProvider(agent string) bool { return agent == "codex" || agent == "claude" }

func (w *authWatcher) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := refreshRetryBase
	for i := 1; i < attempt && d < refreshRetryMax; i++ {
		d *= 2
		if d >= refreshRetryMax {
			return refreshRetryMax
		}
	}
	return d
}

func (w *authWatcher) retryReady(source string) bool {
	r, ok := w.state.Retries[source]
	return !ok || r.NextAttempt.IsZero() || !w.clock().Before(r.NextAttempt)
}

func (w *authWatcher) deferRetry(source string) {
	r := w.state.Retries[source]
	r.Attempts++
	r.NextAttempt = w.clock().Add(retryDelay(r.Attempts))
	w.state.Retries[source] = r
}

func (w *authWatcher) tickCLIRefresh(allowNewKills bool) {
	if w.cliProbe == nil {
		w.cliProbe = probeCLIIdentity
	}
	if w.cliObserve == nil {
		w.cliObserve = func(local string) *persist.CLIIdentity { return readCLIObservation(w.stateDir, local) }
	}

	// Existing records go first. Disabled means no new terminal edge; an already
	// killed source still completes its owed replacement and health check.
	w.workCLIRefresh(allowNewKills)
	if !allowNewKills || w.stopping() {
		return
	}
	w.discoverCLIRefreshes()
}

func (w *authWatcher) discoverCLIRefreshes() {
	roster := w.list()
	sort.Slice(roster, func(i, j int) bool { return roster[i].ID < roster[j].ID })
	if len(roster) == 0 {
		return
	}
	start := 0
	if w.cliCursor != "" {
		start = sort.Search(len(roster), func(i int) bool { return roster[i].ID > w.cliCursor })
		if start == len(roster) {
			start = 0
		}
	}
	probes := 0
	dirty := false
	cache := map[string]struct {
		id  *persist.CLIIdentity
		err error
	}{}
	for step := 0; step < len(roster); step++ {
		m := roster[(start+step)%len(roster)]
		if w.stopping() {
			break
		}
		previousCursor := w.cliCursor
		w.cliCursor = m.ID
		if m.RosterHidden || m.Status.Process != status.ProcessRunning || !refreshProvider(m.AgentType) {
			continue
		}
		if _, exists := w.state.CLI[m.ID]; exists {
			continue
		}
		if m.LaunchOptions[protocol.OptionWorktree] == "true" {
			w.once("cli-worktree:"+m.ID, "cli-refresh: %s session %s (%s) is worktree-isolated; refresh it manually", m.AgentType, m.ID, m.Name)
			continue
		}
		if !validCLIIdentity(m.CLIIdentity) {
			w.once("cli-unknown:"+m.ID, "cli-refresh: %s session %s (%s) has no complete launch CLI identity; leaving it untouched", m.AgentType, m.ID, m.Name)
			continue
		}
		if observed := w.cliObserve(m.ID); !validCLIIdentity(observed) || *observed != *m.CLIIdentity {
			w.once("cli-unobserved:"+m.ID, "cli-refresh: %s session %s (%s) has no matching post-spawn CLI observation; leaving it untouched", m.AgentType, m.ID, m.Name)
			continue
		}
		if m.ConversationID == "" {
			w.once("cli-noconv:"+m.ID, "cli-refresh: %s session %s (%s) has no captured conversation id; leaving it untouched", m.AgentType, m.ID, m.Name)
			continue
		}
		key := m.AgentType + "\x00" + m.Cwd + "\x00" + strings.Join(m.Env, "\x00")
		result, ok := cache[key]
		if !ok {
			if probes >= maxCLIRefreshProbesPerTick {
				// Revisit this row first next tick; it did not consume its probe.
				w.cliCursor = previousCursor
				break
			}
			result.id, result.err = w.cliProbe(m.AgentType, "", m.Env, m.Cwd)
			cache[key] = result
			probes++
		}
		if result.err != nil || !validCLIIdentity(result.id) || !cliIdentityNewer(result.id, m.CLIIdentity) {
			if _, ok := w.state.CLICandidates[m.ID]; ok {
				delete(w.state.CLICandidates, m.ID)
				dirty = true
			}
			continue
		}
		if m.AccountBinding != nil && m.AgentType == accounts.ProviderClaude && w.accountRotation != nil && w.accountRotation.d != nil && w.accountRotation.d.accounts != nil {
			manager := w.accountRotation.d.accounts
			manager.nativeMu.Lock()
			manager.nativeAmbient = result.id
			manager.nativeMu.Unlock()
		}
		if m.AccountBinding != nil && !accountconfig.SupportedNativeVersion(m.AgentType, result.id.Version) {
			if _, exists := w.state.CLICandidates[m.ID]; exists {
				delete(w.state.CLICandidates, m.ID)
				dirty = true
			}
			w.once("cli-managed-version:"+m.ID, "cli-refresh: managed %s session %s has an unsupported target; leaving it untouched", m.AgentType, m.ID)
			continue
		}
		if m.AccountBinding != nil && m.AgentType == accounts.ProviderClaude {
			identity, err := w.managedRefreshIdentity(m, result.id)
			if err != nil {
				continue
			}
			result.id = identity
		}
		candidate := w.state.CLICandidates[m.ID]
		if candidate.Identity != *result.id {
			w.state.CLICandidates[m.ID] = cliRefreshCandidate{Identity: *result.id, Observations: 1}
			dirty = true
			continue
		}
		if candidate.Observations < 2 {
			candidate.Observations++
			w.state.CLICandidates[m.ID] = candidate
			dirty = true
		}
		if candidate.Observations >= 2 {
			w.state.CLI[m.ID] = cliRefreshRecord{
				AgentType: m.AgentType, Target: candidate.Identity,
				State: cliRefreshPending, UpdatedAt: w.clock(),
			}
			delete(w.state.CLICandidates, m.ID)
			dirty = true
		}
	}
	if dirty {
		if err := w.saveState(); err != nil {
			// A refresh record is not authority until durable. Freeze instead of
			// letting the next tick consume memory that a crash would erase.
			w.stateErr = err
			log.Printf("cli-refresh: persist discovery state; freezing lifecycle automation: %v", err)
		}
	}
}

func (w *authWatcher) workCLIRefresh(allowNewKills bool) {
	keys := make([]string, 0, len(w.state.CLI))
	for source := range w.state.CLI {
		keys = append(keys, source)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return
	}
	start := 0
	if w.cliWorkCursor != "" {
		start = sort.SearchStrings(keys, w.cliWorkCursor)
		for start < len(keys) && keys[start] <= w.cliWorkCursor {
			start++
		}
		if start == len(keys) {
			start = 0
		}
	}
	probeAttempts := 0
	destructiveAttempted := false
	for step := 0; step < len(keys); step++ {
		source := keys[(start+step)%len(keys)]
		rec := w.state.CLI[source]
		if rec.State == cliRefreshComplete || rec.State == cliRefreshBlocked {
			continue
		}
		m, ok := w.get(source)
		if !ok || m.RosterHidden {
			w.blockCLIRefresh(source, "source session was deleted or archived before refresh completed")
			continue
		}
		if replacement, found, ambiguous := w.findReplacement(source, rec.AgentType); ambiguous != "" {
			w.blockCLIRefresh(source, ambiguous)
			continue
		} else if found {
			if w.checkCLIReplacement(source, m, replacement) {
				continue
			}
			// A running replacement with no observation is still starting. A
			// terminal/mismatched replacement was made blocked by the check.
			continue
		}
		if m.Status.Process != status.ProcessRunning {
			if !w.state.Killed[source] {
				w.blockCLIRefresh(source, "source session ended before automatic refresh claimed it")
			} else if w.retryReady(source) && probeAttempts < maxCLIRefreshProbesPerTick && !destructiveAttempted {
				probeAttempts++
				w.cliWorkCursor = source
				destructiveAttempted = true
				_ = w.resumeClaimed(rec.AgentType, m)
			}
			continue
		}
		if !allowNewKills || !w.settled || !w.retryReady(source) {
			continue
		}
		if probeAttempts >= maxCLIRefreshProbesPerTick {
			break
		}
		probeAttempts++
		w.cliWorkCursor = source
		if !w.prepareCLIRefreshTarget(m) {
			continue
		}
		if m.Status.Turn != status.TurnIdle || m.Status.Interaction != status.InteractionNone || w.sessionUnsafe(source) {
			continue
		}
		if destructiveAttempted {
			continue
		}
		destructiveAttempted = true
		if w.recycle(rec.AgentType, m) {
			continue
		}
	}
}

// prepareCLIRefreshTarget stabilizes a changed candidate before entering the
// input fence. An installer may move from v2 to v3 while a busy source waits;
// pinning v2 forever would make the record impossible to satisfy.
func (w *authWatcher) prepareCLIRefreshTarget(source persist.Meta) bool {
	rec, ok := w.state.CLI[source.ID]
	if !ok {
		return true
	}
	observed, err := w.refreshTargetIdentity(source, rec.Target)
	if err != nil || !validCLIIdentity(observed) {
		if err != nil {
			rec.LastError = err.Error()
		} else {
			rec.LastError = "candidate CLI identity is incomplete"
		}
		rec.UpdatedAt = w.clock()
		w.state.CLI[source.ID] = rec
		return false
	}
	if err := w.validateManagedRefresh(source, observed); err != nil {
		rec.LastError = errAccountSuccessorUnavailable.Error()
		rec.UpdatedAt = w.clock()
		w.state.CLI[source.ID] = rec
		if err := w.saveState(); err != nil {
			w.stateErr = err
		}
		return false
	}
	if *observed == rec.Target {
		delete(w.state.CLICandidates, source.ID)
		return true
	}
	if !validCLIIdentity(source.CLIIdentity) || !cliIdentityNewer(observed, source.CLIIdentity) {
		delete(w.state.CLICandidates, source.ID)
		rec.LastError = "current CLI is no longer a newer stable candidate"
		rec.UpdatedAt = w.clock()
		w.state.CLI[source.ID] = rec
		_ = w.saveState()
		return false
	}
	candidate := w.state.CLICandidates[source.ID]
	if candidate.Identity != *observed {
		w.state.CLICandidates[source.ID] = cliRefreshCandidate{Identity: *observed, Observations: 1}
		rec.LastError = "newer CLI candidate changed; waiting for a second observation"
		rec.UpdatedAt = w.clock()
		w.state.CLI[source.ID] = rec
		_ = w.saveState()
		return false
	}
	candidate.Observations++
	if candidate.Observations < 2 {
		w.state.CLICandidates[source.ID] = candidate
		_ = w.saveState()
		return false
	}
	rec.Target = candidate.Identity
	rec.LastError = ""
	rec.UpdatedAt = w.clock()
	w.state.CLI[source.ID] = rec
	delete(w.state.CLICandidates, source.ID)
	if err := w.saveState(); err != nil {
		w.stateErr = err
		return false
	}
	return true
}

// validateCLIRefreshTarget is called inside the composer/direct-input fence,
// immediately before the durable claim and kill.
func (w *authWatcher) validateCLIRefreshTarget(m persist.Meta) error {
	rec, ok := w.state.CLI[m.ID]
	if !ok {
		return nil
	}
	observed, err := w.refreshTargetIdentity(m, rec.Target)
	if err != nil || !validCLIIdentity(observed) || *observed != rec.Target {
		if err == nil {
			err = fmt.Errorf("candidate identity changed")
		}
		return fmt.Errorf("cli-refresh: target is no longer stable: %w", err)
	}
	return w.validateManagedRefresh(m, observed)
}

func (w *authWatcher) resumeCLIEnded(agent string, source persist.Meta) bool {
	current, exists := w.get(source.ID)
	if !exists || current.RosterHidden {
		w.blockCLIRefresh(source.ID, "source session was deleted or archived before its owed replacement launched")
		return false
	}
	source = current
	rec, ok := w.state.CLI[source.ID]
	if !ok {
		return w.resumeEnded(agent, source)
	}
	if replacement, found, ambiguous := w.findReplacement(source.ID, agent); ambiguous != "" {
		w.blockCLIRefresh(source.ID, ambiguous)
		return false
	} else if found {
		if w.checkCLIReplacement(source.ID, source, replacement) {
			return rec.State != cliRefreshComplete
		}
		return w.state.CLI[source.ID].State != cliRefreshBlocked
	}
	if !w.prepareCLIRefreshTarget(source) || !w.retryReady(source.ID) {
		return true
	}
	rec = w.state.CLI[source.ID]

	launch := daemon.LaunchSpec{
		AgentType: agent, Name: source.Name, Tag: source.Tag, Cwd: source.Cwd,
		Cols: authRecycleCols, Rows: authRecycleRows, ClientEnv: source.Env,
		SpawnedFrom: source.SpawnedFrom, SpawnIntent: source.SpawnIntent,
		Supervision:         source.Supervision,
		Options:             map[string]string{protocol.OptionResumeFrom: w.endpointID + "/" + source.ID},
		ExpectedCLIIdentity: &rec.Target,
	}
	if source.AccountBinding != nil {
		var err error
		launch, err = w.accountRotation.preflightSuccessor(source, *source.AccountBinding, w.accountRotation.effectiveModel(source), "", &rec.Target)
		if err != nil {
			rec.LastError = errAccountSuccessorUnavailable.Error()
			w.state.CLI[source.ID] = rec
			w.deferRetry(source.ID)
			_ = w.saveState()
			return true
		}
	}
	fresh, err := w.launch(launch)
	if fresh.ID != "" {
		rec.ReplacementID = fresh.ID
		rec.State = cliRefreshWaiting
		if rec.HealthDeadline.IsZero() {
			rec.HealthDeadline = w.clock().Add(5 * time.Minute)
		}
		rec.UpdatedAt = w.clock()
		w.state.CLI[source.ID] = rec
		w.state.Candidates[source.ID] = fresh.ID
		if saveErr := w.saveState(); saveErr != nil {
			log.Printf("cli-refresh: checkpoint replacement %s for %s: %v", fresh.ID, source.ID, saveErr)
			w.deferRetry(source.ID)
			return true
		}
	}
	if err != nil {
		rec = w.state.CLI[source.ID]
		rec.LastError = err.Error()
		rec.UpdatedAt = w.clock()
		w.state.CLI[source.ID] = rec
		w.deferRetry(source.ID)
		_ = w.saveState()
		log.Printf("cli-refresh: resume %s session %s (%s): %v; obligation retained", agent, source.ID, source.Name, err)
		return true
	}
	if fresh.ID == "" {
		w.deferRetry(source.ID)
		return true
	}
	// Require a later watcher observation. A process can still fail provider
	// initialization immediately after the shim writes its spawn observation.
	return true
}

func (w *authWatcher) findReplacement(source, agent string) (persist.Meta, bool, string) {
	if expected := w.state.Candidates[source]; expected != "" {
		m, ok := w.get(expected)
		if !ok {
			return persist.Meta{}, false, fmt.Sprintf("replacement %s disappeared; refusing to create another", expected)
		}
		if m.ResumedFrom != source || m.AgentType != agent {
			return persist.Meta{}, false, fmt.Sprintf("replacement %s does not belong to source %s", expected, source)
		}
		return m, true, ""
	}
	var children []persist.Meta
	for _, m := range w.list() {
		if m.ResumedFrom == source && m.AgentType == agent {
			children = append(children, m)
		}
	}
	if len(children) > 1 {
		return persist.Meta{}, false, fmt.Sprintf("source has %d replacement rows; refusing to choose or create another", len(children))
	}
	if len(children) == 0 {
		return persist.Meta{}, false, ""
	}
	w.state.Candidates[source] = children[0].ID
	if rec, ok := w.state.CLI[source]; ok {
		rec.ReplacementID = children[0].ID
		rec.State = cliRefreshWaiting
		if rec.HealthDeadline.IsZero() {
			rec.HealthDeadline = w.clock().Add(5 * time.Minute)
		}
		rec.UpdatedAt = w.clock()
		w.state.CLI[source] = rec
	}
	if err := w.saveState(); err != nil {
		// The child itself is durable evidence. Keep observing it; a restart can
		// rediscover the same lineage without ever creating another child.
		log.Printf("cli-refresh: checkpoint existing replacement %s for %s: %v", children[0].ID, source, err)
	}
	return children[0], true, ""
}

// checkCLIReplacement returns true once the record reached a terminal
// complete/blocked state. Missing observation on a live row is a startup wait.
func (w *authWatcher) checkCLIReplacement(sourceID string, source, replacement persist.Meta) bool {
	rec := w.state.CLI[sourceID]
	if replacement.Status.Process != status.ProcessRunning {
		w.blockCLIRefresh(sourceID, fmt.Sprintf("replacement %s exited before validation; refusing to create another", replacement.ID))
		return true
	}
	if replacement.BackendPlanError != "" {
		w.blockCLIRefresh(sourceID, fmt.Sprintf("replacement %s launched without its required backend: %s", replacement.ID, replacement.BackendPlanError))
		return true
	}
	if !validCLIIdentity(replacement.CLIIdentity) || *replacement.CLIIdentity != rec.Target {
		w.blockCLIRefresh(sourceID, fmt.Sprintf("replacement %s metadata did not retain the expected CLI identity", replacement.ID))
		return true
	}
	if rec.AgentType == "codex" && (w.ready == nil || !w.ready(replacement)) {
		if rec.HealthDeadline.IsZero() {
			rec.HealthDeadline = w.clock().Add(5 * time.Minute)
			rec.UpdatedAt = w.clock()
			w.state.CLI[sourceID] = rec
			_ = w.saveState()
		} else if !w.clock().Before(rec.HealthDeadline) {
			w.blockCLIRefresh(sourceID, fmt.Sprintf("replacement %s did not establish conversation transport before the startup deadline", replacement.ID))
			return true
		}
		return false
	}
	if replacement.ConversationID == "" || replacement.ConversationID != source.ConversationID {
		w.blockCLIRefresh(sourceID, fmt.Sprintf("replacement %s did not preserve the source conversation identity", replacement.ID))
		return true
	}
	observed := w.cliObserve(replacement.ID)
	if observed == nil {
		if rec.HealthDeadline.IsZero() {
			rec.HealthDeadline = w.clock().Add(5 * time.Minute)
			rec.UpdatedAt = w.clock()
			w.state.CLI[sourceID] = rec
			_ = w.saveState()
		} else if !w.clock().Before(rec.HealthDeadline) {
			w.blockCLIRefresh(sourceID, fmt.Sprintf("replacement %s produced no stable post-spawn CLI observation before the startup deadline", replacement.ID))
			return true
		}
		return false
	}
	if !validCLIIdentity(observed) || *observed != rec.Target {
		w.blockCLIRefresh(sourceID, fmt.Sprintf("replacement %s observed a different CLI installation; refusing to create another", replacement.ID))
		return true
	}
	prior := rec
	wasKilled := w.state.Killed[sourceID]
	rec.State = cliRefreshComplete
	rec.LastError = ""
	rec.ReplacementID = replacement.ID
	rec.UpdatedAt = w.clock()
	w.state.CLI[sourceID] = rec
	w.state.Candidates[sourceID] = replacement.ID
	delete(w.state.Retries, sourceID)
	delete(w.state.Killed, sourceID)
	committed, err := w.persistState()
	if err != nil && !committed {
		w.state.CLI[sourceID] = prior
		if wasKilled {
			w.state.Killed[sourceID] = true
		}
		log.Printf("cli-refresh: persist completion for %s: %v", sourceID, err)
		return false
	}
	if w.clearRecycle != nil {
		w.clearRecycle(sourceID)
	}
	if err != nil {
		log.Printf("cli-refresh: completion for %s committed but directory sync was unconfirmed: %v", sourceID, err)
	}
	log.Printf("cli-refresh: replaced %s session %s -> %s at CLI %s; source row retained and conversation metadata preserved", rec.AgentType, sourceID, replacement.ID, rec.Target.Version)
	return true
}

func (w *authWatcher) blockCLIRefresh(source, reason string) {
	rec, ok := w.state.CLI[source]
	if !ok {
		return
	}
	prior := rec
	retry, hadRetry := w.state.Retries[source]
	wasKilled := w.state.Killed[source]
	rec.State = cliRefreshBlocked
	rec.LastError = reason
	rec.UpdatedAt = w.clock()
	w.state.CLI[source] = rec
	delete(w.state.Retries, source)
	delete(w.state.Killed, source)
	committed, err := w.persistState()
	if err != nil && !committed {
		w.state.CLI[source] = prior
		if hadRetry {
			w.state.Retries[source] = retry
		}
		if wasKilled {
			w.state.Killed[source] = true
		}
		log.Printf("cli-refresh: persist blocked state for %s: %v", source, err)
		return
	}
	if w.clearRecycle != nil {
		w.clearRecycle(source)
	}
	if err != nil {
		log.Printf("cli-refresh: blocked state for %s committed but directory sync was unconfirmed: %v", source, err)
	}
}
