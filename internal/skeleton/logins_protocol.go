package skeleton

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/Nathandela/swarm/internal/adapter/detect"
	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/loginworker"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

func (a *coreAPI) Logins(req protocol.LoginsReq) (reply protocol.LoginsReply, err error) {
	if a.logins == nil {
		return reply, protocol.ErrLoginsUnavailable
	}
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	defer func() {
		if errors.Is(err, logins.ErrConflict) {
			err = protocol.ErrLoginsStaleRevision
		}
	}()
	m := a.logins
	r, err := m.store.Snapshot()
	if err != nil {
		return reply, err
	}
	if req.Action != "list" && req.Action != "status" && req.Action != "complete" && req.ExpectedRevision != r.Revision {
		return reply, logins.ErrConflict
	}
	var selectedJob string
	switch req.Action {
	case "list":
	case "start", "relogin":
		provider, label := req.Provider, strings.TrimSpace(req.Label)
		if req.Action == "relogin" {
			l, ok := loginByID(r, req.LoginID)
			if !ok {
				return reply, logins.ErrIneligible
			}
			provider, label = l.Provider, l.Label
		}
		jobs, err := loginworker.Jobs(m.store.StateRoot())
		if err != nil {
			return reply, err
		}
		for _, c := range jobs {
			view, _ := a.loginJob(c)
			if c.Provider == provider && view.State != "complete" && view.State != "failed" && view.State != "cancelled" {
				return reply, logins.ErrPending
			}
		}
		cfg, err := newLoginWorkerConfig(m.store.StateRoot(), provider, label, req.LoginID)
		if err != nil {
			return reply, err
		}
		executable, err := os.Executable()
		if err != nil {
			return reply, err
		}
		if _, err = loginworker.Start(context.Background(), executable, cfg); err != nil {
			return reply, err
		}
		selectedJob = cfg.JobID
	case "status", "complete", "cancel_login":
		cfg, err := loginworker.ReadConfig(m.store.StateRoot(), req.JobID)
		if err != nil {
			return reply, err
		}
		selectedJob = cfg.JobID
		if req.Action == "cancel_login" {
			if err := a.receiptAcceptedLoginJobs(r, "", cfg.JobID); err != nil {
				return reply, err
			}
		}
		switch req.Action {
		case "complete":
			if err = a.completeLoginJob(cfg); err != nil {
				return reply, err
			}
		case "cancel_login":
			complete, err := loginworker.Completed(cfg)
			if err != nil {
				return reply, err
			}
			if !complete {
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				err = loginworker.CancelJob(ctx, cfg)
				cancel()
				if err != nil {
					return reply, err
				}
				if err = cleanupLoginJob(cfg); err != nil {
					return reply, err
				}
			}
		}
	case "activate":
		l, ok := loginByID(r, req.LoginID)
		if !ok {
			return reply, logins.ErrIneligible
		}
		if _, err := m.BeginSwitchAtRevision(req.ExpectedRevision, l.Provider, l.ID, "owner selected login"); err != nil {
			return reply, err
		}
	case "remove":
		if err := a.receiptAcceptedLoginJobs(r, req.LoginID, ""); err != nil {
			return reply, err
		}
		if _, err := m.store.Remove(req.ExpectedRevision, req.LoginID); err != nil {
			return reply, err
		}
	case "cancel_switch":
		if op, ok := r.Switches[req.Provider]; ok {
			if err := a.receiptAcceptedLoginJobs(r, "", op.LoginJobID); err != nil {
				return reply, err
			}
		}
		if err := m.CancelSwitchAtRevision(req.ExpectedRevision, req.Provider); err != nil {
			return reply, err
		}
	default:
		return reply, logins.ErrIneligible
	}
	reply, err = a.loginList()
	if err != nil {
		return reply, err
	}
	for i := range reply.Jobs {
		if reply.Jobs[i].ID == selectedJob {
			job := reply.Jobs[i]
			reply.Job = &job
			break
		}
	}
	return reply, nil
}

// A removable login or switch must never be the only proof that a job was
// consumed. Persist its receipt before owner actions can remove that marker.
func (a *coreAPI) receiptAcceptedLoginJobs(r logins.Registry, loginID, jobID string) error {
	if loginID == "" && jobID == "" {
		return nil
	}
	accepted := map[string]bool{}
	for _, l := range r.Logins {
		if l.LastLoginJobID != "" {
			accepted[l.LastLoginJobID] = true
		}
	}
	for _, op := range r.Switches {
		if op.LoginJobID != "" {
			accepted[op.LoginJobID] = true
		}
	}
	jobs, err := loginworker.Jobs(a.logins.store.StateRoot())
	if err != nil {
		return err
	}
	for _, c := range jobs {
		if !accepted[c.JobID] || (jobID != "" && c.JobID != jobID) || (loginID != "" && c.LoginID != loginID && c.JobID != loginID) {
			continue
		}
		done, err := loginworker.Completed(c)
		if err != nil {
			return err
		}
		if !done {
			if err := loginworker.MarkCompleted(c); err != nil {
				return err
			}
		}
	}
	return nil
}

func loginByID(r logins.Registry, id string) (logins.Login, bool) {
	for _, l := range r.Logins {
		if l.ID == id {
			return l, true
		}
	}
	return logins.Login{}, false
}

func newLoginWorkerConfig(state, provider, label, loginID string) (loginworker.Config, error) {
	if !managedLoginProvider(provider) || strings.TrimSpace(label) == "" || len(label) > 128 || strings.ContainsFunc(label, unicode.IsControl) {
		return loginworker.Config{}, logins.ErrIneligible
	}
	path, err := exec.LookPath(provider)
	if err != nil {
		return loginworker.Config{}, err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return loginworker.Config{}, err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return loginworker.Config{}, err
	}
	if err = loginworker.ValidateNativePath(path); err != nil {
		return loginworker.Config{}, protocol.ErrLoginsUnsafeNativeCLI
	}
	ad, _ := registry.New(provider)
	banner, err := (detect.Host{}).Run(path, ad.VersionArgs())
	if err != nil {
		return loginworker.Config{}, err
	}
	version, ok := ad.ParseVersion(banner)
	if !ok || version == "" {
		return loginworker.Config{}, loginworker.ErrUnsupported
	}
	fingerprint, err := persist.CLIFingerprint(path)
	if err != nil {
		return loginworker.Config{}, err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return loginworker.Config{}, err
	}
	method := loginworker.MethodNativeLogin
	if provider == "codex" {
		method = loginworker.MethodDeviceCode
	}
	return loginworker.Config{SchemaVersion: loginworker.SchemaVersion, StateRoot: state, JobID: hex.EncodeToString(nonce[:]), Generation: 1, Provider: provider, Label: label, LoginID: loginID, Method: method, NativePath: path, NativeVersion: version, NativeFingerprint: fingerprint, Deadline: time.Now().Add(15 * time.Minute).UTC()}, nil
}

func (a *coreAPI) loginList() (protocol.LoginsReply, error) {
	r, err := a.logins.store.Snapshot()
	if err != nil {
		return protocol.LoginsReply{}, err
	}
	reply := protocol.LoginsReply{Revision: r.Revision, Logins: []protocol.LoginView{}}
	for _, l := range r.Logins {
		v := protocol.LoginView{ID: l.ID, Provider: l.Provider, Label: l.Label, State: l.AuthState, Active: r.Providers[l.Provider].ActiveID == l.ID}
		v.FiveHour, v.Weekly = a.logins.loginUsage(r, l, time.Now())
		if l.LimitKnown && (l.ExhaustedUntil.IsZero() || time.Now().Before(l.ExhaustedUntil)) {
			v.State = "limited"
			if !l.ExhaustedUntil.IsZero() {
				until := l.ExhaustedUntil
				v.ExhaustedUntil = &until
			}
		}
		reply.Logins = append(reply.Logins, v)
	}
	for _, provider := range []string{"claude", "codex"} {
		v := protocol.ProviderLoginView{Provider: provider, ActiveID: r.Providers[provider].ActiveID, State: "native"}
		if v.ActiveID != "" {
			v.State = "active"
		}
		if op, ok := r.Switches[provider]; ok {
			v.State = "switching"
			v.Message = loginMessage(op.LastError)
			if v.Message == "" {
				v.Message = "Waiting for discussions to finish their current work and detach."
			}
		}
		if r.MigrationPending {
			v.State, v.Message = "waiting", "Offline login migration is unfinished."
		}
		reply.Providers = append(reply.Providers, v)
	}
	jobs, err := loginworker.Jobs(a.logins.store.StateRoot())
	if err != nil {
		return reply, err
	}
	for _, cfg := range jobs {
		view, err := a.loginJob(cfg)
		if err != nil {
			return reply, err
		}
		reply.Jobs = append(reply.Jobs, view)
	}
	return reply, nil
}

func loginMessage(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	if len(s) > 512 {
		s = s[:512]
	}
	return s
}

func (a *coreAPI) loginJob(c loginworker.Config) (protocol.LoginJobView, error) {
	v := protocol.LoginJobView{ID: c.JobID, Provider: c.Provider, LoginID: c.LoginID, Label: c.Label, State: "starting"}
	done, err := loginworker.Completed(c)
	if err != nil {
		return v, err
	}
	if done {
		v.State = "complete"
		return v, nil
	}
	p, err := loginworker.ReadProgress(c.StateRoot, c.JobID)
	if errors.Is(err, os.ErrNotExist) {
		if time.Now().After(c.Deadline) {
			v.State, v.Message = "failed", "Sign-in did not start; cancel it and try again."
		}
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.State = p.Phase
	if v.State == loginworker.PhaseCanceled {
		v.State = "cancelled"
	}
	if p.ErrorCode != "" {
		v.Message = "Native sign-in did not finish. Try signing in again."
		switch p.ErrorCode {
		case "wrong_login":
			v.Message = "This is a different account. Add it as a new login, or sign in with the selected account."
		case "duplicate_login":
			v.Message = "This account is already saved. Select it and use Sign in again."
		}
	}
	if p.Worker.Alive() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		live, err := loginworker.Status(ctx, loginworker.Ref{StateRoot: c.StateRoot, JobID: c.JobID, Generation: c.Generation, Worker: p.Worker})
		cancel()
		if err == nil {
			if live.Device != nil {
				v.VerificationURL, v.UserCode = live.Device.VerificationURL, live.Device.UserCode
			}
			v.LoginSocket = live.LoginSocket
		}
	} else if v.State != "ready" && v.State != "failed" && v.State != "cancelled" {
		v.State, v.Message = "failed", "Sign-in worker stopped; cancel it and try again."
	}
	return v, nil
}

func (a *coreAPI) completeLoginJob(c loginworker.Config) error {
	done, err := loginworker.Completed(c)
	if err != nil {
		return err
	}
	if done {
		return cleanupLoginJob(c)
	}
	p, err := loginworker.ReadProgress(c.StateRoot, c.JobID)
	if err != nil {
		return err
	}
	if p.Phase != loginworker.PhaseReady {
		return loginworker.ErrUnavailable
	}
	if err = loginworker.Stopped(c); err != nil {
		return err
	}
	r, err := a.logins.store.Snapshot()
	if err != nil {
		return err
	}
	accepted := false
	for _, l := range r.Logins {
		if l.LastLoginJobID == c.JobID {
			accepted = true
		}
	}
	for _, op := range r.Switches {
		if op.LoginJobID == c.JobID {
			accepted = true
		}
	}
	ownedCodex := c.Provider == logins.ProviderCodex && r.Providers[c.Provider].AuthOwnerMode == logins.CodexNativeGrantMode
	if accepted && ownedCodex && c.LoginID != "" {
		if l, ok := loginByID(r, c.LoginID); ok && l.LastLoginJobID == c.JobID {
			if pendingErr := a.logins.store.CheckCodexGrantPending(c.LoginID); pendingErr != nil {
				if !errors.Is(pendingErr, logins.ErrPending) {
					return pendingErr
				}
				// Finish durable acceptance before destroying the enrollment copy.
				if err := a.consumeCodexRelogin(r.Revision, c, logins.Credential{}); err != nil {
					return err
				}
			}
		}
	}
	if !accepted {
		credential, err := logins.ReadNativeCredential(c.Provider, loginworker.NativeHome(c.StateRoot, c.JobID))
		if err != nil {
			return err
		}
		if c.LoginID == "" {
			identity, identityErr := logins.CodexIdentity(credential.Auth)
			if c.Provider == "claude" {
				identity, identityErr = logins.ClaudeIdentity(credential.Identity)
			}
			if identityErr != nil {
				return identityErr
			}
			for _, l := range r.Logins {
				if l.Provider == c.Provider && l.Identity == identity {
					if err := loginworker.Reject(c, "duplicate_login"); err != nil {
						return err
					}
					return cleanupLoginJob(c)
				}
			}
			_, _, err = a.logins.store.Import(r.Revision, logins.Login{ID: c.JobID, Provider: c.Provider, Label: c.Label, LastLoginJobID: c.JobID}, credential)
		} else if ownedCodex {
			err = a.consumeCodexRelogin(r.Revision, c, credential)
		} else if r.Providers[c.Provider].ActiveID == c.LoginID {
			_, err = a.logins.BeginReloginJob(r.Revision, c.JobID, c.LoginID, credential, "owner signed in again")
		} else {
			_, _, err = a.logins.store.ReloginJob(r.Revision, c.LoginID, credential, c.JobID)
		}
		if err != nil {
			if errors.Is(err, logins.ErrIdentityChanged) {
				if err := loginworker.Reject(c, "wrong_login"); err != nil {
					return err
				}
				return cleanupLoginJob(c)
			}
			return err
		}
	}
	if err = loginworker.MarkCompleted(c); err != nil {
		return err
	}
	return cleanupLoginJob(c)
}

func (a *coreAPI) consumeCodexRelogin(expected uint64, c loginworker.Config, credential logins.Credential) error {
	lease, err := a.logins.store.AcquireCodexRelogin(c.LoginID, c.JobID)
	if err != nil {
		return err
	}
	_, _, err = lease.ReplaceCredential(expected, credential, c.JobID)
	// A failed containment proof must retain the enrollment copy, even on a wrong identity.
	if closeErr := lease.Close(); closeErr != nil {
		return closeErr
	}
	return err
}

func cleanupLoginJob(c loginworker.Config) error {
	cleaned, err := loginworker.Cleaned(c)
	if err != nil || cleaned {
		return err
	}
	if err := loginworker.Stopped(c); err != nil {
		return err
	}
	if err := logins.EraseNativeCredential(c.Provider, loginworker.NativeHome(c.StateRoot, c.JobID)); err != nil {
		return err
	}
	return loginworker.MarkCleaned(c)
}

func (a *coreAPI) watchLoginJobs(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.loginMu.Lock()
			jobs, err := loginworker.Jobs(a.logins.store.StateRoot())
			if err == nil {
				for _, c := range jobs {
					if ctx.Err() != nil {
						break
					}
					p, progressErr := loginworker.ReadProgress(c.StateRoot, c.JobID)
					if progressErr == nil && (p.Phase == loginworker.PhaseFailed || p.Phase == loginworker.PhaseCanceled) {
						_ = cleanupLoginJob(c)
					} else {
						_ = a.completeLoginJob(c)
					}
					_ = loginworker.Prune(c)
				}
			}
			a.loginMu.Unlock()
		}
	}
}
