package swarmmobile

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// mailboxAppender is the narrow relay seam the durable publisher needs. *relay.Client is the
// production implementation; keeping the seam at one append makes commit-unknown crash tests
// deterministic without replacing the relay protocol.
type mailboxAppender interface {
	MailboxAppend(context.Context, string, []byte) (uint64, error)
}

// mailboxAppendObserver is a package-private deterministic test seam for parking a publisher
// after sealing but before the final authority fence. No production relay client implements it.
type mailboxAppendObserver interface {
	beforeMailboxAppend()
}

var (
	errPublicationIdentityChanged = classed(ErrClassNotPaired, errors.New("swarmmobile: pending publication belongs to another registration"))
	errPublicationNoConnection    = classed(ErrClassOffline, errors.New("swarmmobile: no relay connection for pending publication"))
)

const (
	publicationRetryInitial = 250 * time.Millisecond
	publicationRetryCeiling = 5 * time.Second
)

func publicationRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := publicationRetryInitial
	for i := 1; i < attempt && d < publicationRetryCeiling; i++ {
		d *= 2
		if d > publicationRetryCeiling {
			d = publicationRetryCeiling
		}
	}
	return d
}

func actionablePublication(pending []phonecore.PendingPublication) bool {
	for _, p := range pending {
		if p.Phase == phonecore.PublicationPrepared || p.Phase == phonecore.PublicationSealed {
			return true
		}
	}
	return false
}

func nextPublicationExpiry(pending []phonecore.PendingPublication, now time.Time) (time.Duration, bool) {
	var earliest time.Time
	for _, p := range pending {
		var deadline time.Time
		switch p.Phase {
		case phonecore.PublicationPrepared, phonecore.PublicationSealed:
			deadline = p.Command.ExpiresAt
		case phonecore.PublicationAdmitted:
			if !p.CreatedAt.IsZero() {
				deadline = p.CreatedAt.Add(phonecore.PublicationOutcomeTimeout)
			}
		}
		if !deadline.IsZero() && (earliest.IsZero() || deadline.Before(earliest)) {
			earliest = deadline
		}
	}
	if earliest.IsZero() {
		return 0, false
	}
	wait := earliest.Sub(now)
	if wait < 0 {
		wait = 0
	}
	return wait, true
}

// expirePublicationOutcomes makes locally knowable terminal publication states visible through
// the same durable Outcome surface as machine replies. Only the two local timeout codes enter
// here; authenticated accept/refuse replies still resolve through onReply exactly once.
func (a *App) expirePublicationOutcomes(core *phonecore.Core, now time.Time) error {
	if err := core.ExpirePublications(now); err != nil {
		return err
	}
	st := core.State()
	for _, p := range st.PendingPublications {
		if p.Phase != phonecore.PublicationTerminal ||
			(p.TerminalCode != phonecore.PublicationExpired &&
				p.TerminalCode != phonecore.PublicationOutcomeUnknown) {
			continue
		}
		ctrl, ok := st.OpOutcomes[p.OperationID]
		if !ok {
			continue
		}
		a.mu.Lock()
		tracked := a.inflight[p.OperationID]
		delete(a.inflight, p.OperationID)
		a.mu.Unlock()
		if tracked && a.events != nil {
			a.events.emit(&Event{
				Kind: "outcome", Stream: "publication", SessionID: p.SessionID,
				State: string(ctrl.ErrorCode), Message: p.OperationID,
			})
		}
	}
	return nil
}

// ComposerPublications returns the durable composer FIFO as a content-only read model. Android
// hydrates its process-local ledger from this on every surface construction; the Go journal is
// the authority and therefore repeated hydration is idempotent rather than another send.
func (a *App) ComposerPublications() (list *ComposerPublicationList, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	if err := a.expirePublicationOutcomes(core, time.Now()); err != nil {
		return nil, err
	}
	list = &ComposerPublicationList{}
	for _, p := range core.PendingPublications() {
		if p.Kind != phonecore.PublicationComposer {
			continue
		}
		list.items = append(list.items, ComposerPublication{
			LogicalID: p.LogicalID, OperationID: p.OperationID, SessionID: p.SessionID,
			SessionInstance: p.SessionInstance, ExpectedTurn: p.ExpectedTurn, Text: p.Text,
			Phase: string(p.Phase), TerminalCode: p.TerminalCode,
			CreatedAtMillis: p.CreatedAt.UnixMilli(),
		})
	}
	return list, nil
}

// wakePublicationPump is only a hint. Durable PendingPublications, re-read on every wake,
// owns both the work and its order; dropping a duplicate hint cannot drop a publication.
func (a *App) wakePublicationPump() {
	if a == nil || a.publicationWake == nil {
		return
	}
	select {
	case a.publicationWake <- struct{}{}:
	default:
	}
}

// preparePublicationLocked closes the sendContext-to-custody TOCTOU. The caller holds bucketMu;
// this function takes publicationAuthorityMu, validates the pre-lock transport context against
// one current durable State, fills every authority field from that same snapshot, and persists
// before releasing the authority fence. Network append happens later, without this lock held.
func (a *App) preparePublicationLocked(
	core *phonecore.Core,
	sc sendCtx,
	p phonecore.PendingPublication,
	retryOf string,
) error {
	a.publicationAuthorityMu.Lock()
	defer a.publicationAuthorityMu.Unlock()
	st := core.State()
	if st.Disowned || len(st.MachineRelayAuthPub) != ed25519.PublicKeySize ||
		sc.epoch != st.EpochID || sc.key != st.Keys.ContentKey ||
		sc.target != relay.RoutingID(ed25519.PublicKey(st.MachineRelayAuthPub)) ||
		p.Command.Machine != st.Machine {
		return errPublicationIdentityChanged
	}
	p.Machine = st.Machine
	p.EpochID = st.EpochID
	p.Target = sc.target
	p.AuthorityPub = bytes.Clone(st.MachineRelayAuthPub)
	if retryOf == "" {
		return core.PreparePublication(p)
	}
	for _, current := range st.PendingPublications {
		if current.OperationID == retryOf {
			p.LogicalID = current.LogicalID
			return core.PreparePublicationRetry(retryOf, p)
		}
	}
	return phonecore.ErrPublicationState
}

// runPublicationPump is the one redrive loop for a live relay connection. It never keeps a
// content key across its idle wait: resolveSend is called afresh for each attempt so custody
// remains the authority, and the local copy is cleared before sleeping. A persistent relay
// failure therefore costs a bounded exponential cadence rather than a forward/append storm.
func (a *App) runPublicationPump(ctx context.Context, resolve func() (sendCtx, error)) {
	attempt := 0
	for {
		core, err := a.ready()
		if err != nil {
			return
		}
		if err = a.expirePublicationOutcomes(core, time.Now()); err == nil {
			pending := core.PendingPublications()
			if !actionablePublication(pending) {
				attempt = 0
				wait, scheduled := nextPublicationExpiry(pending, time.Now())
				if !scheduled {
					select {
					case <-ctx.Done():
						return
					case <-a.publicationWake:
						continue
					}
				}
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					if !timer.Stop() {
						<-timer.C
					}
					return
				case <-a.publicationWake:
					if !timer.Stop() {
						<-timer.C
					}
					continue
				case <-timer.C:
					continue
				}
			}
		}
		if err == nil {
			var sc sendCtx
			sc, err = resolve()
			if err == nil {
				a.bucketMu.Lock()
				err = a.flushPendingPublicationsLocked(ctx, sc)
				a.bucketMu.Unlock()
				// Do not retain epoch content material across the retry wait. The next attempt
				// must pass through custody again if the core has purged its in-memory key.
				sc.key = [32]byte{}
			}
		}
		if err == nil {
			attempt = 0
			continue
		}

		attempt++
		timer := time.NewTimer(publicationRetryDelay(attempt))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

// flushPendingPublicationsLocked advances the durable publication FIFO as far as this relay
// connection permits. The caller holds bucketMu: pending publications and every other producer
// share one sequence stream, so a later input/command must never overtake a sealed, unadmitted
// envelope after a restart.
//
// A failed append leaves PublicationSealed durable. Its next attempt reuses the exact envelope
// bytes and sequence. An admitted publication remains in the journal awaiting its authenticated
// outcome, but no longer blocks later appends.
func (a *App) flushPendingPublicationsLocked(ctx context.Context, sc sendCtx) error {
	if sc.cl == nil {
		return errPublicationNoConnection
	}
	core, err := a.ready()
	if err != nil {
		return err
	}
	if err := core.ExpirePublications(time.Now()); err != nil {
		return err
	}
	for _, pending := range core.PendingPublications() {
		if pending.Phase == phonecore.PublicationAdmitted || pending.Phase == phonecore.PublicationTerminal {
			continue
		}
		st := core.State()
		if len(st.MachineRelayAuthPub) != ed25519.PublicKeySize ||
			pending.Target != relay.RoutingID(ed25519.PublicKey(st.MachineRelayAuthPub)) ||
			!bytes.Equal(pending.AuthorityPub, st.MachineRelayAuthPub) {
			return errPublicationIdentityChanged
		}
		if pending.Machine != st.Machine || pending.EpochID != st.EpochID ||
			pending.EpochID != sc.epoch || pending.Target != sc.target || st.Disowned {
			return errPublicationIdentityChanged
		}
		if pending.Phase == phonecore.PublicationPrepared {
			seq, err := core.Seq().NextCommand()
			if err != nil {
				return err
			}
			envelope, err := sealPendingPublication(sc, pending, seq)
			if err != nil {
				return err
			}
			if err := core.SealPublication(pending.OperationID, seq, envelope); err != nil {
				return err
			}
			pending.Phase, pending.Sequence, pending.Envelope = phonecore.PublicationSealed, seq, envelope
		}
		if pending.Phase != phonecore.PublicationSealed {
			return classed(ErrClassInternal, fmt.Errorf("%w: operation %s has phase %q",
				phonecore.ErrPublicationState, pending.OperationID, pending.Phase))
		}
		if observer, ok := sc.cl.(mailboxAppendObserver); ok {
			observer.beforeMailboxAppend()
		}
		// Pairing, purge and terminal unpair can all revoke the state observed above while
		// sealing is in progress. Re-read the exact durable authority under their shared fence
		// and keep that fence through the append boundary; a generation-only check would still
		// permit an old envelope after same-process replacement.
		a.publicationAuthorityMu.Lock()
		st = core.State()
		if len(st.MachineRelayAuthPub) != ed25519.PublicKeySize ||
			pending.Target != relay.RoutingID(ed25519.PublicKey(st.MachineRelayAuthPub)) ||
			!bytes.Equal(pending.AuthorityPub, st.MachineRelayAuthPub) ||
			pending.Machine != st.Machine || pending.EpochID != st.EpochID ||
			pending.EpochID != sc.epoch || pending.Target != sc.target || st.Disowned {
			a.publicationAuthorityMu.Unlock()
			return errPublicationIdentityChanged
		}
		_, appendErr := sc.cl.MailboxAppend(ctx, pending.Target, pending.Envelope)
		a.publicationAuthorityMu.Unlock()
		if appendErr != nil {
			return appendErr
		}
		if err := core.MarkPublicationAdmitted(pending.OperationID); err != nil {
			return err
		}
	}
	return nil
}

func sealPendingPublication(sc sendCtx, pending phonecore.PendingPublication, seq uint64) ([]byte, error) {
	switch pending.Kind {
	case phonecore.PublicationComposer:
		return phonecore.SealComposerSendEnvelope(sc.key, sc.epoch, seq, pending.Command, pending.Composer)
	case phonecore.PublicationHistory:
		return phonecore.SealInteractionHistoryEnvelope(sc.key, sc.epoch, seq, pending.Command, pending.History)
	case phonecore.PublicationDetail:
		return phonecore.SealInteractionDetailEnvelope(sc.key, sc.epoch, seq, pending.Command, pending.Detail)
	default:
		return nil, classed(ErrClassInternal,
			fmt.Errorf("%w: unsupported kind %q", phonecore.ErrPublicationState, pending.Kind))
	}
}
