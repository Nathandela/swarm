package skeleton

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/loginworker"
	"github.com/Nathandela/swarm/internal/persist"
)

type codexAuthSelection struct {
	id              string
	epoch, revision uint64
}

func selectedCodexAuth(r logins.Registry) (codexAuthSelection, error) {
	p := r.Providers[logins.ProviderCodex]
	if r.MigrationPending || p.AuthOwnerMode != logins.CodexNativeGrantMode || p.Epoch == 0 {
		return codexAuthSelection{}, errBackendAuthUnavailable
	}
	if op, pending := r.Switches[logins.ProviderCodex]; pending && op.Phase != "committed" && op.Phase != "resuming" {
		return codexAuthSelection{}, errBackendAuthUnavailable
	}
	l, ok := loginByID(r, p.ActiveID)
	if !ok || l.Provider != logins.ProviderCodex || l.AuthState != logins.AuthReady || l.CredentialRevision == 0 {
		return codexAuthSelection{}, errBackendAuthUnavailable
	}
	if p.DesiredCredentialRevision == 0 || p.DesiredCredentialRevision != l.CredentialRevision {
		return codexAuthSelection{}, errBackendAuthUnavailable
	}
	return codexAuthSelection{l.ID, p.Epoch, l.CredentialRevision}, nil
}

type codexAccessFlight struct {
	selection codexAuthSelection
	done      chan struct{}
	finished  time.Time
	err       error
}

// One caller cancelling must not interrupt renewal needed by another discussion.
// Completed flights briefly absorb duplicate native callbacks without retaining tokens.
type codexAccessBroker struct {
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	closed   bool
	wg       sync.WaitGroup
	flights  map[string]*codexAccessFlight
	snapshot func() (logins.Registry, error)
	pending  func(string) error
	read     func(string) (logins.AccessCredential, error)
	refresh  func(context.Context, string, persist.CLIIdentity) error
}

func newCodexAccessBroker(ctx context.Context) *codexAccessBroker {
	ctx, cancel := context.WithCancel(ctx)
	return &codexAccessBroker{ctx: ctx, cancel: cancel, flights: make(map[string]*codexAccessFlight)}
}

func (b *codexAccessBroker) close() {
	b.mu.Lock()
	b.closed = true
	b.cancel()
	b.mu.Unlock()
	b.wg.Wait()
}

func (b *codexAccessBroker) selected() (codexAuthSelection, error) {
	if b.ctx.Err() != nil {
		return codexAuthSelection{}, errBackendAuthUnavailable
	}
	r, err := b.snapshot()
	if err != nil {
		return codexAuthSelection{}, errBackendAuthUnavailable
	}
	selection, err := selectedCodexAuth(r)
	if err != nil || b.pending == nil || b.pending(selection.id) != nil {
		return codexAuthSelection{}, errBackendAuthUnavailable
	}
	return selection, nil
}

func (b *codexAccessBroker) tokens(ctx context.Context, cli persist.CLIIdentity, force bool) (backendAuthTokens, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return backendAuthTokens{}, errBackendAuthUnavailable
	}
	b.wg.Add(1)
	b.mu.Unlock()
	defer b.wg.Done()
	if err := ctx.Err(); err != nil {
		return backendAuthTokens{}, err
	}
	selection, err := b.selected()
	if err != nil {
		return backendAuthTokens{}, err
	}
	current := func() bool { now, err := b.selected(); return err == nil && now == selection }
	var access logins.AccessCredential
	if !force {
		access, err = b.read(selection.id)
		if err != nil && !errors.Is(err, logins.ErrInvalidCredentials) && !errors.Is(err, logins.ErrInUse) {
			return backendAuthTokens{}, errBackendAuthUnavailable
		}
	}
	if force || err != nil || !time.Now().Add(time.Minute).Before(access.ExpiresAt) {
		if err = b.renew(ctx, selection, cli); err != nil {
			return backendAuthTokens{}, err
		}
		if !current() {
			return backendAuthTokens{}, errBackendAuthUnavailable
		}
		access, err = b.read(selection.id)
	}
	if err != nil || access.Token == "" || access.AccountID == "" || !time.Now().Before(access.ExpiresAt) || !current() {
		return backendAuthTokens{}, errBackendAuthUnavailable
	}
	if err := ctx.Err(); err != nil {
		return backendAuthTokens{}, err
	}
	expires := access.ExpiresAt
	return backendAuthTokens{AccessToken: access.Token, AccountID: access.AccountID, current: func() bool {
		return time.Now().Before(expires) && current()
	}}, nil
}

func (b *codexAccessBroker) renew(ctx context.Context, selection codexAuthSelection, cli persist.CLIIdentity) error {
	b.mu.Lock()
	if b.closed || b.ctx.Err() != nil {
		b.mu.Unlock()
		return errBackendAuthUnavailable
	}
	now := time.Now()
	for id, flight := range b.flights {
		if !flight.finished.IsZero() && now.Sub(flight.finished) >= 2*time.Second {
			delete(b.flights, id)
		}
	}
	flight := b.flights[selection.id]
	if flight != nil && flight.selection != selection {
		// A new owner/epoch cannot borrow an older renewal's result or run alongside it.
		b.mu.Unlock()
		return errBackendAuthUnavailable
	}
	if flight == nil {
		if len(b.flights) >= 8 {
			b.mu.Unlock()
			return errBackendAuthUnavailable
		}
		flight = &codexAccessFlight{selection: selection, done: make(chan struct{})}
		b.flights[selection.id] = flight
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			work, cancel := context.WithTimeout(b.ctx, 30*time.Second)
			defer cancel()
			selected, err := b.selected()
			if err == nil && selected == selection {
				err = b.refresh(work, selection.id, cli)
			} else {
				err = errBackendAuthUnavailable
			}
			b.mu.Lock()
			if err != nil {
				flight.err = errBackendAuthUnavailable
			}
			flight.finished = time.Now()
			close(flight.done)
			b.mu.Unlock()
		}()
	}
	b.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.ctx.Done():
		return errBackendAuthUnavailable
	case <-flight.done:
		return flight.err
	}
}

func (m *loginManager) configureCodexAccess(ctx context.Context) {
	b := newCodexAccessBroker(ctx)
	b.snapshot = m.store.Snapshot
	b.pending = m.store.CheckCodexGrantPending
	b.read = func(id string) (logins.AccessCredential, error) {
		lease, err := m.store.AcquireCodexGrant(id)
		if err != nil {
			return logins.AccessCredential{}, err
		}
		access, err := lease.Access(time.Now())
		if closeErr := lease.Close(); closeErr != nil {
			return logins.AccessCredential{}, closeErr
		}
		return access, err
	}
	b.refresh = m.refreshCodexAccess
	m.codexAccess = b
	m.d.backendAuthAccess = func(ctx context.Context, local string, refresh bool) (backendAuthTokens, error) {
		meta, ok := m.get(local)
		if !ok || meta.AgentType != logins.ProviderCodex || meta.LoginRuntime == nil || !meta.LoginRuntime.ExternalAuth || meta.CLIIdentity == nil {
			return backendAuthTokens{}, errBackendAuthUnavailable
		}
		return b.tokens(ctx, *meta.CLIIdentity, refresh)
	}
}

func (m *loginManager) refreshCodexAccess(ctx context.Context, id string, cli persist.CLIIdentity) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	ref, err := loginworker.StartCodexRefresh(ctx, executable, loginworker.RefreshConfig{
		StateRoot: m.store.StateRoot(), LoginID: id, Generation: uint64(time.Now().UnixNano()),
		NativePath: cli.Path, NativeVersion: cli.Version, NativeFingerprint: cli.Fingerprint,
		Deadline: time.Now().Add(25 * time.Second).UTC(),
	})
	if err != nil {
		return err
	}
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		p, err := loginworker.ReadCodexRefresh(ref)
		if err != nil {
			return err
		}
		if !ref.Worker.Alive() {
			if p.Phase == loginworker.PhaseReady && p.WritersStopped {
				return nil
			}
			return errBackendAuthUnavailable
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
