package scheduledexec

import (
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Nathandela/swarm/internal/schedule"
	"github.com/Nathandela/swarm/internal/worktree"
)

var ErrAdmission = errors.New("scheduled launch refused before starting")
var ErrWorkspaceBusy = errors.New("scheduled workspace is owned by a running or unconfirmed Swarm session")
var errStateChanged = errors.New("scheduled occurrence changed")

const startupRejectedDetail = "Worker rejected startup before the opening prompt. Worker stop verified; check the provider login and installation."

type Launch struct {
	RunID, AttemptID, OperationID, Token      string
	Agent, Authority, Name, Workspace, Prompt string
}

type Session struct {
	Running, NeedsInput, Exhausted, Stopped, StartupRejected bool
}

type Backend interface {
	Launch(Launch) (string, error)
	Reconcile(operationID string) (string, bool)
	Session(sessionID, attemptID string) (Session, bool)
	Stop(sessionID, attemptID string) error
	ProjectCompletion(sessionID, attemptID string) error
	HandoffEvidence(sessionID, attemptID string) string
}

type promptBackend interface {
	SendPrompt(sessionID, attemptID, prompt string, begin func() error) error
}

type closingMessageBackend interface {
	ClosingMessage(sessionID, attemptID, summary string) bool
}

type Engine struct {
	store    *schedule.Store
	stateDir string
	backend  Backend
	dispatch sync.Mutex
}

func New(store *schedule.Store, stateDir string, backend Backend) *Engine {
	return &Engine{store: store, stateDir: stateDir, backend: backend}
}

func (e *Engine) Tick(now time.Time) error {
	e.dispatch.Lock()
	defer e.dispatch.Unlock()
	if _, err := e.store.ClaimDue(now); err != nil {
		return err
	}
	runs, err := e.store.ActiveOccurrences()
	if err != nil {
		return err
	}
	slices.SortFunc(runs, func(a, b schedule.Occurrence) int {
		return cmp.Or(a.DueAt.Compare(b.DueAt), cmp.Compare(a.ID, b.ID))
	})
	var failures []error
	for _, run := range runs {
		if err := e.advance(run, now); err != nil && !errors.Is(err, errStateChanged) {
			failures = append(failures, fmt.Errorf("run %s: %w", run.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (e *Engine) Run(jobID, operationID string, now time.Time) (string, error) {
	e.dispatch.Lock()
	defer e.dispatch.Unlock()
	run, err := e.store.ClaimManualWithID(jobID, operationID, now)
	if err != nil {
		return "", err
	}
	// A Run now retry can recover an occurrence that never began dispatch.
	// Once an attempt exists, only the scheduler advances its lifecycle.
	if len(run.Attempts) != 0 || run.State != schedule.OccurrenceQueued {
		return run.ID, nil
	}
	err = e.dispatchRun(run, now)
	if errors.Is(err, errStateChanged) {
		err = nil
	}
	return run.ID, err
}

func (e *Engine) advance(run schedule.Occurrence, now time.Time) error {
	switch run.State {
	case schedule.OccurrenceReady:
		return e.project(run)
	case schedule.OccurrenceAttention:
		return e.recoverUnknown(run, now)
	case schedule.OccurrenceWorking, schedule.OccurrenceNeedsInput:
		return e.observe(run, now)
	case schedule.OccurrenceQueued, schedule.OccurrenceBlocked:
		grace := run.Spec.Grace
		if !run.Manual && usedRoutes(run) == 0 && ((grace == 0 && now.Sub(run.DueAt) >= time.Minute) ||
			(grace > 0 && now.Sub(run.DueAt) > grace)) {
			return e.mark(run.ID, schedule.OccurrenceMissed, "Trigger expired before safe dispatch.")
		}
		return e.dispatchRun(run, now)
	default:
		return nil
	}
}

func (e *Engine) observe(run schedule.Occurrence, now time.Time) error {
	if len(run.Attempts) == 0 {
		return e.mark(run.ID, schedule.OccurrenceAttention, "Worker state is missing its execution attempt.")
	}
	a := run.Attempts[len(run.Attempts)-1]
	if a.State == schedule.AttemptDispatching {
		if session, ok := e.backend.Reconcile(a.OperationID); ok {
			return e.update(run, a, schedule.OccurrenceWorking, schedule.AttemptRunning, session, "", now)
		}
		return e.update(run, a, schedule.OccurrenceAttention, schedule.AttemptUnknown, "", "Launch outcome unknown. Inspect the daemon session before re-running.", now)
	}
	session, exists := e.backend.Session(a.SessionID, a.ID)
	if !exists {
		return e.update(run, a, schedule.OccurrenceAttention, schedule.AttemptUnknown, "", "Worker session is unavailable. Inspect the saved workspace and session history.", now)
	}
	if a.State == schedule.AttemptStopping {
		return e.stop(run, a, session, now)
	}
	if run.Completion != nil {
		if verifier, ok := e.backend.(closingMessageBackend); ok && verifier.ClosingMessage(a.SessionID, a.ID, run.Completion.Summary) {
			return e.finalize(run.ID, a.ID, now)
		}
		if session.Stopped {
			return e.update(run, a, schedule.OccurrenceIncomplete, schedule.AttemptStopped, "", "Completion was received, but the worker exited before its closing chat message could be verified.", now)
		}
		if run.State != schedule.OccurrenceNeedsInput && now.Sub(a.UpdatedAt) >= 5*time.Minute {
			return e.update(run, a, schedule.OccurrenceNeedsInput, a.State, "", "Open the worker chat and ask it to include the saved completion summary in its closing message.", now)
		}
		return nil
	}
	switch {
	case session.Exhausted:
		// Commit stop intent before signalling, and fence the completion verb.
		if err := e.update(run, a, schedule.OccurrenceWorking, schedule.AttemptStopping, "", "Provider usage limit reached. Verifying that the worker stopped.", now); err != nil {
			return err
		}
		a.State, a.UpdatedAt = schedule.AttemptStopping, now
		return e.stop(run, a, session, now)
	case session.Stopped && session.StartupRejected && !a.PromptStarted:
		return e.advanceRoute(run, a, startupRejectedDetail, now)
	case session.Stopped:
		return e.update(run, a, schedule.OccurrenceIncomplete, schedule.AttemptStopped, "", "Worker exited without a completion receipt; the workspace was kept.", now)
	case !session.Running:
		return e.update(run, a, schedule.OccurrenceAttention, schedule.AttemptUnknown, "", "Worker disappeared without a verified stop or completion receipt.", now)
	case session.NeedsInput:
		return e.update(run, a, schedule.OccurrenceNeedsInput, a.State, "", "Open the worker chat to continue.", now)
	case !a.PromptDelivered && supportsPrivatePrompt(e.backend):
		return e.deliverPrompt(run, a, now)
	case run.State != schedule.OccurrenceWorking:
		return e.update(run, a, schedule.OccurrenceWorking, a.State, "", "", now)
	default:
		return nil
	}
}

func supportsPrivatePrompt(backend Backend) bool {
	_, supported := backend.(promptBackend)
	return supported
}

func (e *Engine) deliverPrompt(run schedule.Occurrence, a schedule.Attempt, now time.Time) error {
	if a.PromptStarted {
		return e.update(run, a, schedule.OccurrenceAttention, schedule.AttemptUnknown, "", "Opening prompt delivery outcome unknown. Inspect the worker chat before sending anything again.", now)
	}
	private := e.backend.(promptBackend)
	started := false
	update := func(delivered bool) error {
		_, err := e.store.UpdateOccurrence(run.ID, func(current *schedule.Occurrence) error {
			last := &current.Attempts[len(current.Attempts)-1]
			if last.ID != a.ID || last.State != schedule.AttemptRunning {
				return errStateChanged
			}
			last.PromptStarted, last.PromptDelivered = true, delivered
			if delivered && current.Completion == nil {
				current.State, current.Detail = schedule.OccurrenceWorking, ""
			}
			return nil
		})
		return err
	}
	err := private.SendPrompt(a.SessionID, a.ID, e.handoffPrompt(run), func() error {
		err := update(false)
		started = err == nil
		return err
	})
	if started {
		if err != nil {
			return e.update(run, a, schedule.OccurrenceAttention, schedule.AttemptUnknown, "", "Opening prompt delivery outcome unknown. Inspect the worker chat before sending anything again.", now)
		}
		return update(true)
	}
	// Safe to retry because the durable write boundary was never reached.
	if now.Sub(a.StartedAt) >= 2*time.Minute {
		return e.update(run, a, schedule.OccurrenceNeedsInput, a.State, "", "The private composer is not available. Open the worker chat and check its login and workspace trust.", now)
	}
	return nil
}

// Late evidence can release a frozen workspace. A definitively admitted
// session can be monitored again, but an expired stop never redrives launch.
func (e *Engine) recoverUnknown(run schedule.Occurrence, now time.Time) error {
	if len(run.Attempts) == 0 {
		return nil
	}
	a := run.Attempts[len(run.Attempts)-1]
	if a.State != schedule.AttemptUnknown {
		return nil
	}
	sessionID := a.SessionID
	if sessionID == "" {
		var found bool
		sessionID, found = e.backend.Reconcile(a.OperationID)
		if !found {
			return nil
		}
	}
	session, found := e.backend.Session(sessionID, a.ID)
	if !found {
		return nil
	}
	if run.Completion != nil {
		if verifier, ok := e.backend.(closingMessageBackend); ok && verifier.ClosingMessage(sessionID, a.ID, run.Completion.Summary) {
			return e.finalize(run.ID, a.ID, now)
		}
	}
	if session.Stopped {
		return e.update(run, a, schedule.OccurrenceIncomplete, schedule.AttemptStopped, sessionID, "Worker stop was verified after an uncertain outcome. The workspace and history were kept.", now)
	}
	if session.Running && a.SessionID == "" {
		return e.update(run, a, schedule.OccurrenceWorking, schedule.AttemptRunning, sessionID, "", now)
	}
	return nil
}

func (e *Engine) stop(run schedule.Occurrence, a schedule.Attempt, session Session, now time.Time) error {
	if session.Stopped {
		return e.advanceRoute(run, a, "Provider usage limit reached; worker stop verified.", now)
	}
	if now.Sub(a.UpdatedAt) > 2*time.Minute {
		return e.update(run, a, schedule.OccurrenceAttention, schedule.AttemptUnknown, "", "Could not verify worker stop. Inspect the previous session before continuing.", now)
	}
	// Stop is idempotent for the same attempt. It never removes the workspace.
	if !session.Running {
		return nil
	}
	// The shim may close its socket while contained processes are joining.
	// Keep the recorded stop intent and wait for independent exit evidence.
	_ = e.backend.Stop(a.SessionID, a.ID)
	return nil
}

func (e *Engine) advanceRoute(run schedule.Occurrence, a schedule.Attempt, detail string, now time.Time) error {
	if err := e.update(run, a, schedule.OccurrenceQueued, schedule.AttemptStopped, "", detail, now); err != nil {
		return err
	}
	latest, err := e.store.Occurrence(run.ID)
	if err != nil {
		return err
	}
	return e.dispatchRun(latest, now)
}

// A reservation lost to a competing workspace launch did not use a provider.
func usedRoutes(run schedule.Occurrence) int {
	count := 0
	for _, attempt := range run.Attempts {
		if attempt.State != schedule.AttemptDeferred {
			count++
		}
	}
	return count
}

func (e *Engine) dispatchRun(run schedule.Occurrence, now time.Time) error {
	var err error
	if run.Workspace == "" {
		run, err = e.reserveWorkspace(run)
		if err != nil || run.Workspace == "" {
			return err
		}
	}
	if run.State == schedule.OccurrenceBlocked {
		available, err := e.workspaceAvailable(run)
		if err != nil || !available {
			return err
		}
	}
	if err := ensureWorkspace(e.stateDir, run); err != nil {
		return e.mark(run.ID, schedule.OccurrenceAttention, err.Error())
	}

	for route := usedRoutes(run); route < len(run.Spec.Agents); route++ {
		agent := run.Spec.Agents[route]
		i := len(run.Attempts)
		a, token := newAttempt(now, run.Workspace, agent)
		run, err = e.store.UpdateOccurrence(run.ID, func(current *schedule.Occurrence) error {
			if len(current.Attempts) != i || (current.State != schedule.OccurrenceQueued && current.State != schedule.OccurrenceBlocked) {
				return errStateChanged
			}
			current.State = schedule.OccurrenceWorking
			current.Detail = ""
			current.Attempts = append(current.Attempts, a)
			return nil
		})
		if err != nil {
			return err
		}
		prompt := e.handoffPrompt(run)
		session, launchErr := e.backend.Launch(Launch{
			RunID: run.ID, AttemptID: a.ID, OperationID: a.OperationID, Token: token,
			Agent: agent, Authority: run.Spec.Authority, Name: run.Spec.Name,
			Workspace: run.Workspace, Prompt: prompt,
		})
		detail := launchDetail(launchErr, token, prompt, run.Spec.Prompt)
		switch {
		case launchErr == nil && session != "":
			return e.update(run, a, schedule.OccurrenceWorking, schedule.AttemptRunning, session, "", now)
		case errors.Is(launchErr, ErrWorkspaceBusy):
			return e.update(run, a, schedule.OccurrenceBlocked, schedule.AttemptDeferred, "", detail, now)
		case errors.Is(launchErr, ErrAdmission):
			if err := e.update(run, a, schedule.OccurrenceQueued, schedule.AttemptRejected, "", detail, now); err != nil {
				return err
			}
			run.Attempts[i].State = schedule.AttemptRejected
			run.Attempts[i].Detail = detail
		default:
			if session, ok := e.backend.Reconcile(a.OperationID); ok {
				return e.update(run, a, schedule.OccurrenceWorking, schedule.AttemptRunning, session, "", now)
			}
			return e.update(run, a, schedule.OccurrenceAttention, schedule.AttemptUnknown, "", "Launch outcome unknown. Inspect the daemon session before re-running. "+detail, now)
		}
	}
	return e.mark(run.ID, schedule.OccurrenceExhausted, "All configured agents were refused or reached a verified usage limit.")
}

func (e *Engine) reserveWorkspace(run schedule.Occurrence) (schedule.Occurrence, error) {
	isolate := run.Spec.Workspace == schedule.WorkspaceWorktree
	source, workspace, err := describeWorkspace(e.stateDir, run, isolate)
	if err != nil {
		return run, e.mark(run.ID, schedule.OccurrenceAttention, err.Error())
	}
	run.SourceDirectory, run.Workspace = source, workspace
	available, err := e.workspaceAvailable(run)
	if err != nil {
		return schedule.Occurrence{}, err
	}
	if !available && !isolate && run.Spec.AllowOverlap {
		_, isolated, isolationErr := describeWorkspace(e.stateDir, run, true)
		if isolationErr == nil {
			run.Workspace = isolated
			available, err = e.workspaceAvailable(run)
		}
	}
	if err != nil {
		return schedule.Occurrence{}, err
	}
	if !available {
		return schedule.Occurrence{}, e.mark(run.ID, schedule.OccurrenceBlocked, fmt.Sprintf("Workspace %s overlaps another Swarm session or occurrence. Choose a dedicated checkout for isolation, or wait until it is free.", run.SourceDirectory))
	}
	return e.store.UpdateOccurrence(run.ID, func(current *schedule.Occurrence) error {
		current.SourceDirectory, current.Workspace = run.SourceDirectory, run.Workspace
		return nil
	})
}

func (e *Engine) workspaceAvailable(run schedule.Occurrence) (bool, error) {
	if check, ok := e.backend.(interface{ WorkspaceAvailable(string) bool }); ok && !check.WorkspaceAvailable(run.Workspace) {
		return false, nil
	}
	others, err := e.store.ActiveOccurrences()
	if err != nil {
		return false, err
	}
	for _, other := range others {
		if other.ID == run.ID || !ownsWorkspace(other) {
			continue
		}
		sameJob := other.JobID == run.JobID && other.State != schedule.OccurrenceReady
		if worktree.PathsOverlap(run.Workspace, other.Workspace) || (sameJob && (!run.Spec.AllowOverlap || !other.Spec.AllowOverlap)) {
			return false, nil
		}
	}
	return true, nil
}

func ownsWorkspace(run schedule.Occurrence) bool {
	if run.Workspace == "" {
		return false
	}
	switch run.State {
	case schedule.OccurrenceQueued, schedule.OccurrenceWorking, schedule.OccurrenceNeedsInput:
		return true
	case schedule.OccurrenceAttention:
		return len(run.Attempts) > 0 && run.Attempts[len(run.Attempts)-1].State == schedule.AttemptUnknown
	case schedule.OccurrenceReady:
		// The native session remains attachable and may still be active after
		// reporting completion. Keep ownership until a verified stop.
		return run.ReleasedAt.IsZero()
	default:
		return false
	}
}

func newAttempt(now time.Time, workspace, agent string) (schedule.Attempt, string) {
	var entropy [32]byte
	rand.Read(entropy[:])
	token := hex.EncodeToString(entropy[:])
	return schedule.Attempt{
		ID: schedule.NewID(now), Agent: agent, Workspace: workspace, OperationID: "schedule:" + schedule.NewID(now),
		CompletionTokenHash: tokenHash(token), DispatchStarted: true, State: schedule.AttemptDispatching,
		StartedAt: now, UpdatedAt: now,
	}, token
}

func launchDetail(err error, secrets ...string) string {
	if err == nil {
		return ""
	}
	detail := err.Error()
	for _, secret := range secrets {
		if secret != "" {
			detail = strings.ReplaceAll(detail, secret, "[redacted]")
		}
	}
	detail = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, detail)
	runes := []rune(detail)
	if len(runes) > 400 {
		detail = string(runes[:400]) + "…"
	}
	return detail
}

func tokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func (e *Engine) handoffPrompt(run schedule.Occurrence) string {
	for i := len(run.Attempts) - 2; i >= 0; i-- {
		previous := run.Attempts[i]
		if previous.State == schedule.AttemptStopped {
			if previous.Detail == startupRejectedDetail {
				continue
			}
			return Prompt(run.Spec.Prompt) + "\n\nA previous " + previous.Agent + " worker reached a provider usage limit and was verified stopped. " +
				"It used this same workspace. Inspect the changes and verify any prior external actions before continuing. Preserve existing work. Previous Swarm session: " + previous.SessionID + "." + e.backend.HandoffEvidence(previous.SessionID, previous.ID)
		}
	}
	return Prompt(run.Spec.Prompt)
}

func (e *Engine) mark(id string, state schedule.OccurrenceState, detail string) error {
	_, err := e.store.UpdateOccurrence(id, func(run *schedule.Occurrence) error {
		run.State, run.Detail = state, detail
		return nil
	})
	return err
}

func (e *Engine) update(run schedule.Occurrence, a schedule.Attempt, state schedule.OccurrenceState, attemptState schedule.AttemptState, session, detail string, now time.Time) error {
	_, err := e.store.UpdateOccurrence(run.ID, func(current *schedule.Occurrence) error {
		last := &current.Attempts[len(current.Attempts)-1]
		if last.ID != a.ID || last.State != a.State || (run.Completion == nil && current.Completion != nil) {
			return errStateChanged
		}
		last.State, last.Detail, last.UpdatedAt = attemptState, detail, now
		if session != "" {
			last.SessionID = session
		}
		current.State, current.Detail = state, detail
		return nil
	})
	return err
}

// Complete verifies an exact attempt token and bound native session inside the
// state transaction. The worker can retry the identical receipt after a crash.
func (e *Engine) Complete(runID, attemptID, sessionID, token string, report schedule.Completion, now time.Time) error {
	report.Summary = strings.TrimSpace(report.Summary)
	if report.Summary == "" || len(report.Summary) > 4096 || len(report.Links) > 16 || len(token) != 64 {
		return errors.New("invalid completion summary, links, or attempt credential")
	}
	for _, link := range report.Links {
		if strings.TrimSpace(link) == "" || len(link) > 1024 {
			return errors.New("invalid completion artifact link")
		}
	}
	run, err := e.store.UpdateOccurrence(runID, func(run *schedule.Occurrence) error {
		if len(run.Attempts) == 0 {
			return errors.New("no active automation attempt")
		}
		a := &run.Attempts[len(run.Attempts)-1]
		if a.ID != attemptID || subtle.ConstantTimeCompare([]byte(a.CompletionTokenHash), []byte(tokenHash(token))) != 1 {
			return errors.New("completion credential did not match the active automation attempt")
		}
		if a.SessionID != "" && a.SessionID != sessionID {
			return errors.New("completion came from a different worker session")
		}
		if run.Completion != nil {
			if run.Completion.Summary == report.Summary && slices.Equal(run.Completion.Links, report.Links) {
				return nil
			}
			return errors.New("a different completion receipt is already recorded")
		}
		if a.State != schedule.AttemptRunning && a.State != schedule.AttemptDispatching {
			return errors.New("this automation attempt is no longer eligible to complete")
		}
		if supportsPrivatePrompt(e.backend) && !a.PromptStarted {
			return errors.New("the opening prompt has not been dispatched")
		}
		session, bound := e.backend.Session(sessionID, attemptID)
		if !bound || (!session.Running && a.SessionID == "") {
			return errors.New("completion came from an unbound worker session")
		}
		a.SessionID, a.State, a.UpdatedAt = sessionID, schedule.AttemptRunning, now
		run.State, run.Completion = schedule.OccurrenceWorking, &report
		run.Detail = "Completion received. Waiting for the closing chat message."
		return nil
	})
	if err != nil {
		return err
	}
	if run.State == schedule.OccurrenceReady {
		return nil
	}
	if verifier, ok := e.backend.(closingMessageBackend); ok && !verifier.ClosingMessage(sessionID, attemptID, report.Summary) {
		return nil
	}
	return e.finalize(runID, attemptID, now)
}

func (e *Engine) finalize(runID, attemptID string, now time.Time) error {
	run, err := e.store.UpdateOccurrence(runID, func(run *schedule.Occurrence) error {
		a := &run.Attempts[len(run.Attempts)-1]
		if a.ID != attemptID || run.Completion == nil {
			return errStateChanged
		}
		if run.State == schedule.OccurrenceReady {
			return nil
		}
		a.State, a.UpdatedAt = schedule.AttemptComplete, now
		run.State, run.CompletedAt, run.Detail = schedule.OccurrenceReady, now, ""
		return nil
	})
	if err != nil {
		return err
	}
	_ = e.project(run)
	return nil
}

func (e *Engine) project(run schedule.Occurrence) error {
	if len(run.Attempts) == 0 {
		return fmt.Errorf("completion has no worker attempt")
	}
	a := run.Attempts[len(run.Attempts)-1]
	session, exists := e.backend.Session(a.SessionID, a.ID)
	projected := run.ProjectedAt.IsZero() && exists
	released := run.ReleasedAt.IsZero() && exists && session.Stopped
	if !projected && !released {
		return nil
	}
	if projected {
		if err := e.backend.ProjectCompletion(a.SessionID, a.ID); err != nil {
			return err
		}
	}
	_, err := e.store.UpdateOccurrence(run.ID, func(current *schedule.Occurrence) error {
		if projected && current.ProjectedAt.IsZero() {
			current.ProjectedAt = time.Now().UTC()
		}
		if released && current.ReleasedAt.IsZero() {
			current.ReleasedAt = time.Now().UTC()
		}
		return nil
	})
	return err
}
