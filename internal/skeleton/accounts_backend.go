package skeleton

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/enrollment"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

const accountJobsSchema = 1

// The daemon is the only writer of enrollment intent. Native workers publish
// separate progress, and cannot cancel, admit, or overwrite this authority.
type accountJob struct {
	ID                string                     `json:"id"`
	Generation        uint64                     `json:"generation"`
	Provider          string                     `json:"provider"`
	Method            string                     `json:"method"`
	State             string                     `json:"state"`
	Candidate         accounts.Candidate         `json:"candidate"`
	TargetAccountID   string                     `json:"target_account_id,omitempty"`
	AdmittedAccountID string                     `json:"admitted_account_id,omitempty"`
	Worker            enrollment.ProcessIdentity `json:"worker"`
	Deadline          time.Time                  `json:"deadline"`
	ErrorCode         string                     `json:"error_code,omitempty"`
	Email             string                     `json:"email,omitempty"`
	Plan              string                     `json:"plan,omitempty"`
	CustodyConsumed   bool                       `json:"custody_consumed,omitempty"`
}

type accountJobs struct {
	SchemaVersion int                   `json:"schema_version"`
	Revision      uint64                `json:"revision"`
	Jobs          map[string]accountJob `json:"jobs"`
}

type accountManager struct {
	jobsRoot              *os.Root
	jobsAnchor            os.FileInfo
	mu                    sync.Mutex
	stateRoot, executable string
	store                 *accounts.Store
	jobs                  accountJobs
	unavailable           error
	native                map[string]*persist.CLIIdentity
	list                  func() []persist.Meta
	refresh               func(string) error
	quota                 *accountQuotaFetcher
	retry                 func(string) error
	move                  func(string, accounts.Binding) error
}

func openAccountManager(stateRoot, executable string, list func() []persist.Meta) *accountManager {
	m := &accountManager{stateRoot: stateRoot, executable: executable, list: list, native: make(map[string]*persist.CLIIdentity), jobs: accountJobs{SchemaVersion: accountJobsSchema, Jobs: make(map[string]accountJob)}}
	var err error
	m.store, err = accounts.Open(stateRoot)
	if err != nil {
		m.unavailable = protocol.ErrAccountsUnavailable
		return m
	}
	m.jobsRoot, err = os.OpenRoot(filepath.Join(stateRoot, "accounts"))
	if err != nil {
		m.unavailable = protocol.ErrAccountsUnavailable
		return m
	}
	m.jobsAnchor, err = os.Lstat(filepath.Join(stateRoot, "accounts"))
	if err != nil {
		m.unavailable = protocol.ErrAccountsUnavailable
		return m
	}
	data, err := m.readJobs()
	if err == nil {
		if len(data) > 1<<20 || rejectDuplicateJSONKeys(data) != nil {
			m.unavailable = protocol.ErrAccountsUnavailable
			return m
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if dec.Decode(&m.jobs) != nil || m.jobs.SchemaVersion != accountJobsSchema || len(m.jobs.Jobs) > 256 || m.jobs.Jobs == nil {
			m.unavailable = protocol.ErrAccountsUnavailable
			return m
		}
		for id, job := range m.jobs.Jobs {
			if id != job.ID || job.ID != job.Candidate.ID || job.Generation == 0 || (job.Provider != "codex" && job.Provider != "claude") || (job.CustodyConsumed && job.State != "admitted" && job.State != "cancelled") {
				m.unavailable = protocol.ErrAccountsUnavailable
				return m
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		m.unavailable = protocol.ErrAccountsUnavailable
		return m
	}
	// Probe version only; neither command authenticates or invokes a model.
	for _, provider := range []string{"codex", "claude"} {
		identity, err := probeCLIIdentity(provider, "", daemon.PolicyEnv(nil), stateRoot)
		if err == nil {
			m.native[provider] = identity
		}
	}
	return m
}

func (m *accountManager) close() {
	m.mu.Lock()
	m.unavailable = protocol.ErrAccountsUnavailable
	q := m.quota
	if q != nil {
		q.cancel()
	}
	m.mu.Unlock()
	if q != nil {
		q.wg.Wait()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.store != nil {
		_ = m.store.Close()
	}
	if m.jobsRoot != nil {
		_ = m.jobsRoot.Close()
	}
}

func (m *accountManager) readJobs() ([]byte, error) {
	before, err := m.jobsRoot.Lstat("enrollment.json")
	if err != nil {
		return nil, err
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok || !before.Mode().IsRegular() || before.Mode().Perm()&0o077 != 0 || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 || before.Size() > 1<<20 {
		return nil, protocol.ErrAccountsUnavailable
	}
	f, err := m.jobsRoot.OpenFile("enrollment.json", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(before, actual) {
		return nil, protocol.ErrAccountsUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return nil, err
	}
	// A new singleton reconciles a predecessor's uncertain rename before it may
	// admit or cancel a candidate using this authority.
	if err = f.Sync(); err != nil {
		return nil, err
	}
	dir, err := m.jobsRoot.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	if err = dir.Sync(); err != nil {
		return nil, err
	}
	return data, nil
}

func (m *accountManager) writeJobs(data []byte) error {
	current, err := os.Lstat(filepath.Join(m.stateRoot, "accounts"))
	if err != nil || !os.SameFile(current, m.jobsAnchor) {
		return protocol.ErrAccountsUnavailable
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := ".enrollment-" + hex.EncodeToString(nonce[:])
	f, err := m.jobsRoot.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = m.jobsRoot.Remove(temp) }()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = m.jobsRoot.Rename(temp, "enrollment.json"); err != nil {
		return err
	}
	directory, err := m.jobsRoot.Open(".")
	if err != nil {
		return errContextGuardSettingsPostRename
	}
	defer func() { _ = directory.Close() }()
	if err = directory.Sync(); err != nil {
		return errContextGuardSettingsPostRename
	}
	return nil
}

func (m *accountManager) saveJob(job accountJob) error {
	next := accountJobs{SchemaVersion: accountJobsSchema, Revision: m.jobs.Revision + 1, Jobs: make(map[string]accountJob, len(m.jobs.Jobs)+1)}
	for id, old := range m.jobs.Jobs {
		next.Jobs[id] = old
	}
	next.Jobs[job.ID] = job
	data, err := json.Marshal(next)
	if err != nil {
		return protocol.ErrAccountsUnavailable
	}
	if err = m.writeJobs(data); err != nil {
		if errors.Is(err, errContextGuardSettingsPostRename) {
			m.unavailable = protocol.ErrAccountsUnavailable
		}
		return protocol.ErrAccountsUnavailable
	}
	m.jobs = next
	return nil
}

// Keep a bounded recent enrollment history. Only cancelled jobs with completed
// erasure and admitted jobs with completed writer proof can be forgotten;
// uncertain or failed workers retain their cancellation and admission fences.
func (m *accountManager) pruneTerminalJobs() error {
	if len(m.jobs.Jobs) <= 128 {
		return nil
	}
	var terminal []accountJob
	for _, job := range m.jobs.Jobs {
		if (job.State == "admitted" || job.State == "cancelled") && m.terminalEnrollmentAbsent(job) {
			terminal = append(terminal, job)
		}
	}
	sort.Slice(terminal, func(i, j int) bool {
		if !terminal[i].Deadline.Equal(terminal[j].Deadline) {
			return terminal[i].Deadline.Before(terminal[j].Deadline)
		}
		return terminal[i].ID < terminal[j].ID
	})
	count := min(len(terminal), len(m.jobs.Jobs)-128)
	if count == 0 {
		return nil
	}
	next := accountJobs{SchemaVersion: accountJobsSchema, Revision: m.jobs.Revision + 1, Jobs: make(map[string]accountJob, len(m.jobs.Jobs))}
	for id, job := range m.jobs.Jobs {
		next.Jobs[id] = job
	}
	for _, job := range terminal[:count] {
		delete(next.Jobs, job.ID)
	}
	data, err := json.Marshal(next)
	if err != nil {
		return protocol.ErrAccountsUnavailable
	}
	if err = m.writeJobs(data); err != nil {
		if errors.Is(err, errContextGuardSettingsPostRename) {
			m.unavailable = protocol.ErrAccountsUnavailable
		}
		return protocol.ErrAccountsUnavailable
	}
	m.jobs = next
	return nil
}

func activeAccountJob(state string) bool {
	return state == "cancelling" || state == "admitting" || state == "pending" || state == "authenticating" || state == "verifying" || state == "ready"
}

// Cancellation intent stays active until every credential writer is proved
// stopped. A daemon crash before Start's PID save must not lose the fence or
// admit a second provider login while the first worker is still alive.
func (m *accountManager) stopCancelledJob(job accountJob) (accountJob, error) {
	neverStarted := job.Worker.PID == 0 && enrollment.NeverStarted(m.stateRoot, job.ID)
	if job.Method != "import-native" && job.Method != "token-manual" && !neverStarted {
		if err := enrollment.FenceCancel(m.stateRoot, job.ID, job.Generation); err != nil {
			return job, nil
		}
		if job.Worker.PID == 0 {
			p, err := enrollment.ReadProgress(m.stateRoot, job.ID)
			if err != nil || p.JobID != job.ID || p.Generation != job.Generation || p.Worker.PID <= 0 || p.Worker.StartTime <= 0 {
				return job, nil
			}
			job.Worker = p.Worker
			if err := m.saveJob(job); err != nil {
				return job, err
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		p, err := enrollment.Cancel(ctx, m.ref(job))
		if err != nil || !p.WritersStopped || !p.NativeWritersStopped || p.Worker.Alive() || p.Runner.Alive() {
			return job, nil
		}
		for _, child := range p.Children {
			if child.Alive() {
				return job, nil
			}
		}
	}
	// Only candidates that never committed can reach this path; their stable
	// profile is safe to erase once the cancellation proof is durable.
	if err := m.store.DiscardCandidate(job.Candidate, true); err != nil {
		return job, protocol.ErrAccountsUnavailable
	}
	job.State = "cancelled"
	if err := m.saveJob(job); err != nil {
		return job, err
	}
	return job, nil
}

func (m *accountManager) ref(job accountJob) enrollment.Ref {
	return enrollment.Ref{StateRoot: m.stateRoot, JobID: job.ID, Generation: job.Generation, Worker: job.Worker}
}

func (m *accountManager) syncJob(job accountJob) (accountJob, *enrollment.LiveStatus, error) {
	if job.State == "cancelling" {
		job, err := m.stopCancelledJob(job)
		return job, nil, err
	}
	if job.State == "admitting" {
		registry, err := m.store.Snapshot()
		if err != nil {
			return job, nil, protocol.ErrAccountsUnavailable
		}
		for _, account := range registry.Accounts {
			for _, generation := range account.Generations {
				if generation.ProfileGeneration == job.Candidate.ProfileGeneration && generation.Identity == job.Candidate.Identity && (job.TargetAccountID == "" || job.TargetAccountID == account.ID) {
					job.State = "admitted"
					job.AdmittedAccountID = account.ID
					if err = m.saveJob(job); err != nil {
						return job, nil, err
					}
					return job, nil, nil
				}
			}
		}
		job.State = "ready"
		if err := m.saveJob(job); err != nil {
			return job, nil, err
		}
	}
	if !activeAccountJob(job.State) || job.Method == "import-native" || job.Method == "token-manual" {
		return job, nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	live, err := enrollment.Status(ctx, m.ref(job))
	if err != nil {
		progress, readErr := enrollment.ReadProgress(m.stateRoot, job.ID)
		if readErr != nil || progress.Generation != job.Generation {
			return job, nil, nil
		}
		live.Progress = progress
		live.Email, live.Plan = progress.Email, progress.Plan
	}
	p := live.Progress
	if p.Generation != job.Generation || p.JobID != job.ID {
		return job, nil, protocol.ErrAccountsUnavailable
	}
	if p.Worker.PID <= 0 || p.Worker.StartTime <= 0 || (job.Worker.PID != 0 && job.Worker != p.Worker) {
		return job, nil, protocol.ErrAccountsUnavailable
	}
	if job.Worker.PID == 0 {
		job.Worker = p.Worker
	}
	switch p.Phase {
	case enrollment.PhaseReady:
		if !p.WritersStopped || !p.NativeWritersStopped || p.Worker.Alive() || p.Runner.Alive() {
			return job, nil, protocol.ErrAccountsUnavailable
		}
		for _, child := range p.Children {
			if child.Alive() {
				return job, nil, protocol.ErrAccountsUnavailable
			}
		}
		candidate, verifyErr := m.store.VerifyCandidate(job.Candidate)
		if verifyErr != nil {
			job.State = "failed"
			job.ErrorCode = "identity-verification-failed"
		} else {
			job.Candidate = candidate
			job.State = "ready"
			job.Email, job.Plan = p.Email, p.Plan
		}
	case enrollment.PhaseCanceled:
		job.State = "cancelling"
	case enrollment.PhaseFailed:
		job.State = "verifying"
		if p.WritersStopped && p.NativeWritersStopped && !p.Worker.Alive() && !p.Runner.Alive() {
			stopped := true
			for _, child := range p.Children {
				stopped = stopped && !child.Alive()
			}
			if stopped {
				job.State = "failed"
			}
		}
		job.ErrorCode = p.ErrorCode
	default:
		if time.Now().After(job.Deadline) && p.WritersStopped {
			job.State = "failed"
			job.ErrorCode = "deadline"
		} else {
			job.State = "authenticating"
		}
	}
	old := m.jobs.Jobs[job.ID]
	if job.State != old.State || job.Worker != old.Worker || job.Candidate.Identity != old.Candidate.Identity || job.ErrorCode != old.ErrorCode || job.Email != old.Email || job.Plan != old.Plan {
		if err = m.saveJob(job); err != nil {
			return old, nil, err
		}
	}
	return job, &live, nil
}

func (m *accountManager) jobView(job accountJob, live *enrollment.LiveStatus) protocol.AccountEnrollmentView {
	view := protocol.AccountEnrollmentView{ID: job.ID, Provider: job.Provider, Method: job.Method, State: job.State, TargetAccountID: job.TargetAccountID, Deadline: &job.Deadline, ErrorCode: job.ErrorCode, Email: job.Email, Plan: job.Plan}
	if live != nil && activeAccountJob(job.State) {
		if live.Device != nil {
			view.VerificationURL = live.Device.VerificationURL
			view.UserCode = live.Device.UserCode
		}
		view.LoginSocket = live.LoginSocket
		view.Email = live.Email
		view.Plan = live.Plan
	}
	if job.State == "ready" && job.Candidate.Verification == accounts.VerificationUnverified {
		view.Message = "Credential identity and effective settings are unverified; this account cannot launch discussions. Use native Claude sign-in."
	}
	if job.State == "failed" {
		view.Message = "Sign-in could not be verified. Start a new sign-in attempt."
	}
	return view
}

func (m *accountManager) methods() map[string][]protocol.AccountMethodView {
	out := map[string][]protocol.AccountMethodView{}
	for _, provider := range []string{"codex", "claude"} {
		native := m.native[provider]
		supported := runtime.GOOS == "linux" && native != nil && ((provider == "codex" && native.Version == "0.160.0") || (provider == "claude" && native.Version == "2.1.288"))
		reason := ""
		if !supported {
			reason = "Installed CLI version has not been verified for isolated enrollment."
		} else {
			path, err := filepath.EvalSymlinks(native.Path)
			if err != nil || enrollment.ValidateNativePath(path) != nil {
				supported = false
				reason = "Installed CLI cannot start safely. Check its ownership and executable permissions."
			}
		}
		if provider == "codex" {
			out[provider] = []protocol.AccountMethodView{{ID: "device-code", Label: "Sign in with device code", Available: supported, Reason: reason}, {ID: "browser", Label: "Browser sign-in", Available: false, Reason: "Use device-code sign-in on this VM."}, {ID: "import-native", Label: "Import native profile", Available: false, Reason: "Cached Codex identity is not tied to the imported credential. Use a fresh device-code sign-in."}}
		} else {
			out[provider] = []protocol.AccountMethodView{{ID: "native-login", Label: "Sign in with Claude Code", Available: supported, Reason: reason}, {ID: "import-native", Label: "Import native profile", Available: false, Reason: "Cached Claude identity is not tied to the imported credential. Use a fresh native sign-in."}, {ID: "token-manual", Label: "Import one-year token", Available: false, Reason: "Token-only identity and effective settings are not verified. Use native Claude sign-in."}}
		}
	}
	return out
}

func (m *accountManager) snapshot() (protocol.AccountsReply, error) {
	r, err := m.store.Snapshot()
	if err != nil {
		return protocol.AccountsReply{}, protocol.ErrAccountsUnavailable
	}
	if m.quota != nil {
		m.quota.scheduleLocked(r, time.Now())
	}
	out := protocol.AccountsReply{Revision: r.Revision, Enabled: r.Enabled, Methods: m.methods(), Accounts: []protocol.AccountView{}, Jobs: []protocol.AccountEnrollmentView{}}
	counts := map[string]int{}
	if m.list != nil {
		for _, session := range m.list() {
			if session.Status.Process == status.ProcessRunning && session.AccountBinding != nil {
				counts[session.AccountBinding.AccountID]++
			}
		}
	}
	ids := make([]string, 0, len(r.Accounts))
	for id := range r.Accounts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		account := r.Accounts[id]
		generation := account.Generations[account.CurrentGeneration]
		state := account.Lifecycle
		if account.Auth != accounts.AuthValid {
			state = "needs-login"
		} else if generation.Verification == accounts.VerificationUnverified {
			state = "unverified"
		}
		view := protocol.AccountView{ID: id, Provider: account.Provider, Label: account.Label, CredentialKind: generation.Kind, State: state, CredentialGeneration: account.CurrentGeneration, Assigned: counts[id], Retiring: account.Lifecycle == accounts.LifecycleRetiring, RefreshSupported: account.Provider == "codex" && m.refresh != nil}
		view.CredentialsErased = len(account.Generations) > 0
		for _, retained := range account.Generations {
			view.CredentialsErased = view.CredentialsErased && retained.CredentialErased
		}

		binding := accounts.Binding{SchemaVersion: accounts.SchemaVersion, Provider: account.Provider, AccountID: account.ID, CredentialGeneration: account.CurrentGeneration, Identity: generation.Identity, ConfigurationGeneration: 1}
		view.Email, view.Plan, _ = m.store.DisplayIdentity(binding)
		scopes := make([]string, 0, len(account.Quota.Scopes))
		for scope := range account.Quota.Scopes {
			scopes = append(scopes, scope)
		}
		sort.Strings(scopes)
		for _, name := range scopes {
			scope := account.Quota.Scopes[name]
			if scope.UsedPercent != nil || scope.ResetAt != nil {
				view.Quota = append(view.Quota, protocol.AccountQuotaView{Label: name, UsedPercent: scope.UsedPercent, ResetAt: scope.ResetAt, ObservedAt: quotaUsageObservedAt(scope)})
			}
			if scope.Denied && account.Auth == accounts.AuthValid && account.Lifecycle == accounts.LifecycleEnabled {
				view.State = "cooling-down"
				if scope.NextTrialAt != nil && (view.NextRetryAt == nil || scope.NextTrialAt.After(*view.NextRetryAt)) {
					view.NextRetryAt = scope.NextTrialAt
				}
				if scope.ResetAt != nil && (view.NextRetryAt == nil || scope.ResetAt.After(*view.NextRetryAt)) {
					view.NextRetryAt = scope.ResetAt
				}
			}
		}
		view.RetrySupported = account.Provider == "claude" && view.State == "cooling-down" && m.retry != nil && (view.NextRetryAt == nil || !view.NextRetryAt.After(time.Now()))
		if m.quota != nil {
			m.quota.projectLocked(account, &view)
		}
		out.Accounts = append(out.Accounts, view)
	}
	jobIDs := make([]string, 0, len(m.jobs.Jobs))
	for id := range m.jobs.Jobs {
		jobIDs = append(jobIDs, id)
	}
	sort.Strings(jobIDs)
	for _, id := range jobIDs {
		job, live, err := m.syncJob(m.jobs.Jobs[id])
		if err != nil {
			return protocol.AccountsReply{}, err
		}
		if activeAccountJob(job.State) || job.State == "failed" {
			out.Jobs = append(out.Jobs, m.jobView(job, live))
		}
	}
	return out, nil
}

func accountAPIError(err error) error {
	if errors.Is(err, protocol.ErrAccountMoveUnmanaged) {
		return protocol.ErrAccountMoveUnmanaged
	}
	if errors.Is(err, accounts.ErrRevisionConflict) {
		return protocol.ErrAccountsStaleRevision
	}
	return protocol.ErrAccountsUnavailable
}

func (m *accountManager) Accounts(req protocol.AccountsReq) (protocol.AccountsReply, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unavailable != nil || m.store == nil {
		return protocol.AccountsReply{}, protocol.ErrAccountsUnavailable
	}
	if req.Action == "list" {
		return m.snapshot()
	}
	r, err := m.store.Snapshot()
	if err != nil {
		return protocol.AccountsReply{}, accountAPIError(err)
	}
	if req.Action != "status" && req.Action != "cancel" && req.Action != "refresh" && req.ExpectedRevision != r.Revision {
		return protocol.AccountsReply{}, protocol.ErrAccountsStaleRevision
	}
	switch req.Action {
	case "start", "import":
		return m.start(req, r)
	case "status", "cancel", "admit":
		job, ok := m.jobs.Jobs[req.JobID]
		if !ok {
			return protocol.AccountsReply{}, protocol.ErrAccountsUnavailable
		}
		if req.Action == "cancel" {
			if job.State == "admitting" {
				job, _, err = m.syncJob(job)
				if err != nil {
					return protocol.AccountsReply{}, err
				}
			}
			if job.State == "admitted" {
				out, snapshotErr := m.snapshot()
				view := m.jobView(job, nil)
				out.Job = &view
				return out, snapshotErr
			}
			if job.State != "cancelled" {
				job.State = "cancelling"
				if err = m.saveJob(job); err != nil {
					return protocol.AccountsReply{}, err
				}
				job, err = m.stopCancelledJob(job)
				if err != nil {
					return protocol.AccountsReply{}, err
				}
			}
		} else {
			var live *enrollment.LiveStatus
			job, live, err = m.syncJob(job)
			if err != nil {
				return protocol.AccountsReply{}, err
			}
			if req.Action == "admit" {
				if job.State != "ready" || req.AccountID != job.TargetAccountID {
					return protocol.AccountsReply{}, protocol.ErrAccountsUnavailable
				}
				job.State = "admitting"
				if err = m.saveJob(job); err != nil {
					return protocol.AccountsReply{}, err
				}
				var account accounts.Account
				if job.TargetAccountID != "" {
					_, account, err = m.store.Reauthenticate(req.ExpectedRevision, job.TargetAccountID, job.Candidate)
				} else {
					_, account, err = m.store.Admit(req.ExpectedRevision, job.Candidate, req.Label)
				}
				if err != nil {
					if !errors.Is(err, accounts.ErrDurabilityUncertain) {
						job.State = "ready"
						_ = m.saveJob(job)
					} else {
						m.unavailable = protocol.ErrAccountsUnavailable
					}
					return protocol.AccountsReply{}, accountAPIError(err)
				}
				job.State = "admitted"
				job.AdmittedAccountID = account.ID
				if err = m.saveJob(job); err != nil {
					return protocol.AccountsReply{}, err
				}
			}
			out, err := m.snapshot()
			view := m.jobView(job, live)
			out.Job = &view
			return out, err
		}
		out, err := m.snapshot()
		view := m.jobView(job, nil)
		out.Job = &view
		return out, err
	case "enable":
		_, err = m.store.SetEnabled(req.ExpectedRevision, req.Provider, req.Enabled)
	case "update":
		if req.Label != "" {
			_, err = m.store.SetLabel(req.ExpectedRevision, req.AccountID, req.Label)
		} else {
			lifecycle := accounts.LifecycleEnabled
			if req.Paused {
				lifecycle = accounts.LifecyclePaused
			}
			if req.Retire {
				lifecycle = accounts.LifecycleRetiring
			}
			_, err = m.store.SetLifecycle(req.ExpectedRevision, req.AccountID, lifecycle)
		}
	case "remove":
		// Retirement drains assignments. Erasure requires a separately characterized
		// cache inventory and is never inferred from an ended history reference.
		_, err = m.store.SetLifecycle(req.ExpectedRevision, req.AccountID, accounts.LifecycleRetiring)
	case "move":
		if m.move == nil || m.list == nil {
			return protocol.AccountsReply{}, protocol.ErrAccountsUnavailable
		}
		var source *persist.Meta
		for _, meta := range m.list() {
			if meta.ID == req.SessionID {
				copy := meta
				source = &copy
				break
			}
		}
		if source == nil {
			return protocol.AccountsReply{}, protocol.ErrAccountsUnavailable
		}
		if source.AccountBinding == nil {
			return protocol.AccountsReply{}, protocol.ErrAccountMoveUnmanaged
		}
		binding, bindingErr := m.store.CurrentBinding(req.AccountID, source.AccountBinding.ConfigurationGeneration)
		if bindingErr != nil || binding.Provider != source.AgentType {
			return protocol.AccountsReply{}, protocol.ErrAccountsUnavailable
		}
		err = m.move(source.ID, binding)
	case "refresh":
		if m.quota != nil {
			account, exists := r.Accounts[req.AccountID]
			if !exists {
				err = accounts.ErrIneligible
			} else {
				err = m.quota.requestLocked(account, time.Now(), true)
			}
		} else if m.refresh == nil {
			err = protocol.ErrAccountsUnavailable
		} else {
			err = m.refresh(req.AccountID)
		}
	case "retry":
		if m.retry == nil {
			err = protocol.ErrAccountsUnavailable
		} else {
			err = m.retry(req.AccountID)
		}
	default:
		err = protocol.ErrAccountsUnavailable
	}
	if err != nil {
		return protocol.AccountsReply{}, accountAPIError(err)
	}
	return m.snapshot()
}

func (m *accountManager) start(req protocol.AccountsReq, r accounts.Registry) (protocol.AccountsReply, error) {
	supported := false
	for _, method := range m.methods()[req.Provider] {
		supported = supported || (method.ID == req.Method && method.Available)
	}
	if !supported {
		return protocol.AccountsReply{}, protocol.ErrAccountsUnavailable
	}
	if err := m.pruneTerminalJobs(); err != nil || len(m.jobs.Jobs) >= 256 {
		return protocol.AccountsReply{}, protocol.ErrAccountsUnavailable
	}
	for _, job := range m.jobs.Jobs {
		if job.Provider == req.Provider && activeAccountJob(job.State) {
			return protocol.AccountsReply{}, protocol.ErrAccountsUnavailable
		}
	}
	if req.AccountID != "" {
		a, ok := r.Accounts[req.AccountID]
		if !ok || a.Provider != req.Provider || a.Lifecycle == accounts.LifecycleRetiring {
			return protocol.AccountsReply{}, protocol.ErrAccountsUnavailable
		}
	}
	// Resolve a native installation before creating durable candidate intent.
	// A removed startup installation must not leave an unstartable pending job.
	var nativePath string
	if req.Method != "import-native" && req.Method != "token-manual" {
		native := m.native[req.Provider]
		var pathErr error
		nativePath, pathErr = filepath.EvalSymlinks(native.Path)
		if pathErr != nil || enrollment.ValidateNativePath(nativePath) != nil {
			return protocol.AccountsReply{}, protocol.ErrAccountsUnavailable
		}
	}
	var candidate accounts.Candidate
	var err error
	switch req.Method {
	case "import-native":
		source := filepath.Clean(req.SourcePath)
		info, statErr := os.Lstat(source)
		if statErr != nil || !filepath.IsAbs(source) {
			return protocol.AccountsReply{}, protocol.ErrAccountsUnavailable
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// Standard ~/.codex and ~/.claude aliases may select a profile
			// directory. The selected credential file itself stays no-follow.
			resolved, resolveErr := filepath.EvalSymlinks(source)
			if resolveErr != nil {
				return protocol.AccountsReply{}, protocol.ErrAccountsUnavailable
			}
			resolvedInfo, resolveErr := os.Lstat(resolved)
			if resolveErr != nil || !resolvedInfo.IsDir() {
				return protocol.AccountsReply{}, protocol.ErrAccountsUnavailable
			}
			source, info = resolved, resolvedInfo
		}
		if info.IsDir() {
			if req.Provider == "codex" {
				source = filepath.Join(source, "auth.json")
			} else {
				source = filepath.Join(source, ".credentials.json")
			}
		}
		if req.Provider == "codex" {
			candidate, err = m.store.ImportCodex(source)
		} else {
			identity := filepath.Join(filepath.Dir(source), ".claude.json")
			if _, statErr = os.Lstat(identity); errors.Is(statErr, os.ErrNotExist) {
				identity = filepath.Join(filepath.Dir(filepath.Dir(source)), ".claude.json")
			}
			candidate, err = m.store.ImportClaude(source, identity)
		}
	case "token-manual":
		candidate, err = m.store.ImportClaudeToken(req.Token)
	default:
		candidate, err = m.store.CreateCandidate(req.Provider, accounts.KindNative)
	}
	req.Token = ""
	if err != nil {
		return protocol.AccountsReply{}, accountAPIError(err)
	}
	job := accountJob{ID: candidate.ID, Generation: 1, Provider: req.Provider, Method: req.Method, State: "pending", Candidate: candidate, TargetAccountID: req.AccountID, Deadline: time.Now().Add(15 * time.Minute).UTC()}
	if req.Method == "import-native" || req.Method == "token-manual" {
		job.State = "ready"
		job.Email, job.Plan, _ = m.store.CandidateDisplayIdentity(candidate)
	}
	if err = m.saveJob(job); err != nil {
		return protocol.AccountsReply{}, err
	}
	if job.State == "pending" {
		native := m.native[req.Provider]
		cfg := enrollment.Config{SchemaVersion: enrollment.SchemaVersion, StateRoot: m.stateRoot, JobID: job.ID, CandidateID: candidate.ID, CandidateProfileGeneration: candidate.ProfileGeneration, Generation: job.Generation, Provider: job.Provider, Method: job.Method, NativePath: nativePath, NativeVersion: native.Version, Deadline: job.Deadline}
		ref, startErr := enrollment.Start(context.Background(), m.executable, cfg)
		if startErr != nil {
			job.State = "failed"
			job.ErrorCode = "worker-start-failed"
		} else {
			job.Worker = ref.Worker
			job.State = "authenticating"
		}
		if err = m.saveJob(job); err != nil {
			return protocol.AccountsReply{}, err
		}
	}
	out, err := m.snapshot()
	view := m.jobView(job, nil)
	out.Job = &view
	return out, err
}

func (a *coreAPI) Accounts(req protocol.AccountsReq) (protocol.AccountsReply, error) {
	if a.accounts == nil {
		return protocol.AccountsReply{}, protocol.ErrAccountsUnavailable
	}
	return a.accounts.Accounts(req)
}
