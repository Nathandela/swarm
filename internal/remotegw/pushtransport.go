package remotegw

// Per-pairing push_transport selection (ADR-015 P12, docs/specifications/push-gateway-api.md
// §10.1). Selection is durable and atomic, and wake DELIVERY rides exactly one transport:
// a gateway pairing never also fires the legacy relay's push_trigger, and foreground_only
// fires neither.
//
// SCOPE HONESTY: real per-pairing conveyance (the phone allocating an address and handing
// it to the machine over an authenticated pairing-update, PG-MIG-2) is a LATER slice --
// internal/phonecore, internal/remote/mobile and internal/remote/android are untouched
// here. This wave's transport selection is a single durable value for THIS machine,
// sourced from gateway params/config.
//
// TODO(pairing-conveyance): a later slice replaces the single static value this wave's
// config supplies with the real per-pairing PG-MIG-2 transition (address allocation,
// pairing-update ack, gateway test wake). TransportStore's contract does not change when
// that lands; only what calls SetTransport does. THE WAKE KEY (ServiceConfig.WakeKey,
// threaded into WakeObligationConfig by service.go) has the identical gap: ADR-015 P7
// requires it be PHONE-GENERATED PER PAIRING and conveyed alongside the address, NOT
// epoch material, so that an ADR-011 M5 epoch rotation cannot invalidate a push binding
// -- this wave sources it from id.EpochKeys().WakeKey instead, so until the real
// conveyance lands, an epoch rotation silently breaks every WakeV1 tag with nothing
// failing on the machine.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
)

// PushTransport is the closed, three-value migration state of PG-MIG-1.
type PushTransport string

const (
	TransportLegacyRelay    PushTransport = "legacy_relay"
	TransportGateway        PushTransport = "gateway"
	TransportForegroundOnly PushTransport = "foreground_only"
)

func validPushTransport(t PushTransport) bool {
	switch t {
	case TransportLegacyRelay, TransportGateway, TransportForegroundOnly:
		return true
	default:
		return false
	}
}

// TransportStore is the durable, atomic custody of one machine's selected
// push_transport (PG-MIG-1). A malformed or unrecognised value must never land
// durably -- the same fail-closed discipline as errCorruptOutbox/errCorruptSeqFile --
// and a rejected SetTransport must never leave a torn file: on failure, the PREVIOUS
// durable value is what a reopen still reports.
type TransportStore interface {
	Transport() (PushTransport, error)
	SetTransport(PushTransport) error
}

// gatewayObligationDriver is the seam TransportRouter drives under the gateway
// transport; *WakeObligationMachine satisfies it. A distinct interface (rather than the
// concrete type) so the router's own tests can inject a spy without wiring a full
// obligation machine.
type gatewayObligationDriver interface {
	Trigger() error
	Drive(ctx context.Context) error
}

// TransportRouter is the PushTriggerer PushNotifier is configured with (PushConfig.
// Pusher) once a pairing's transport may be anything other than legacy_relay. It makes
// selection EXCLUSIVE: push.go is untouched by this file, and the router alone decides
// which of the two channels -- or neither -- ever fires, reading the durable
// TransportStore fresh on every call rather than caching a choice at construction.
type TransportRouter struct {
	Transport TransportStore
	Legacy    PushTriggerer           // the relay's push_trigger op; P12's surviving legacy path
	Gateway   gatewayObligationDriver // *WakeObligationMachine satisfies this
}

// gatewayObligationSuperseder is the OPTIONAL cancellation half of the deferred-wake
// pre-append ruling (bd agents-tracker-hggx.4.4): *WakeObligationMachine satisfies it.
// It is asserted at the call site rather than folded into gatewayObligationDriver so
// every existing router test double (spies implementing only Trigger/Drive) keeps
// compiling unchanged -- a double without it simply cannot be superseded, which is the
// honest no-op for a spy that never pre-appended anything durable either. wakeSeq
// scopes the cancellation to the one provisional record the deferral created, and
// ownAppends to the coalesces that deferral cycle itself contributed to it
// (WakeObligationMachine.Supersede's identity and coalesce rules).
type gatewayObligationSuperseder interface {
	Supersede(wakeSeq uint64, ownAppends int, reason string) error
}

// gatewayProvisionalTriggerer is the OPTIONAL identity-reporting half of the same
// ruling: a gateway arm that can say WHICH wake_seq a pre-append trigger landed on,
// so the deferral's later supersede has an identity to scope itself to.
// *WakeObligationMachine satisfies it; an arm without it (an old spy) still gets its
// plain Trigger and the deferral simply has no identity to cancel by, the safe default.
type gatewayProvisionalTriggerer interface {
	TriggerProvisional() (uint64, error)
}

var (
	_ PushTriggerer                 = (*TransportRouter)(nil)
	_ obligationPreAppender         = (*TransportRouter)(nil)
	_ provisionalObligationAppender = (*TransportRouter)(nil)
	_ obligationSuperseder          = (*TransportRouter)(nil)
	_ gatewayObligationSuperseder   = (*WakeObligationMachine)(nil)
	_ gatewayProvisionalTriggerer   = (*WakeObligationMachine)(nil)
)

// PreAppendObligation durably records intent to wake for the CURRENT push_transport
// selection, before PushNotifier publishes the mailbox record the wake announces
// (PG-OBL-2, push.go's Event/preAppendObligation). Only the gateway leg does real work:
// legacy_relay's ordering guarantee is already "publish then push" (push.go, unaffected
// by this method's existence), and foreground_only has no durable obligation to record.
func (r *TransportRouter) PreAppendObligation() error {
	if r.Transport == nil {
		return nil
	}
	t, err := r.Transport.Transport()
	if err != nil {
		return err
	}
	if t != TransportGateway {
		return nil
	}
	if r.Gateway == nil {
		return nil
	}
	return r.Gateway.Trigger()
}

// PreAppendProvisionalObligation is PreAppendObligation for the DEFERRED wake
// (bd agents-tracker-hggx.4.4): it additionally reports the wake_seq the durable
// trigger landed on -- the identity SupersedeObligation later scopes the deferral
// timer's cancellation to -- with ok=false when no identity was recorded (a
// non-gateway transport, a nil arm, or an arm without the capability, in which case
// the plain pre-append still ran).
func (r *TransportRouter) PreAppendProvisionalObligation() (seq uint64, ok bool, err error) {
	if r.Transport == nil {
		return 0, false, nil
	}
	t, err := r.Transport.Transport()
	if err != nil {
		return 0, false, err
	}
	if t != TransportGateway || r.Gateway == nil {
		return 0, false, nil
	}
	if pt, capable := r.Gateway.(gatewayProvisionalTriggerer); capable {
		seq, err := pt.TriggerProvisional()
		return seq, err == nil, err
	}
	return 0, false, r.Gateway.Trigger()
}

// SupersedeObligation durably cancels the provisional obligation identified by wakeSeq
// -- in place, honestly, and ONLY it (WakeObligationMachine.Supersede's identity
// scoping) -- when the deferred-wake timer's at-send preference re-read suppressed the
// send whose record PreAppendProvisionalObligation created (bd agents-tracker-hggx.4.4).
// ownAppends is passed straight through: it is the deferral cycle's own pre-append count
// against that record, and only the machine has the coalesce total to compare it with.
// Only the gateway leg does real work: the other transports pre-append nothing, so
// there is nothing to supersede.
func (r *TransportRouter) SupersedeObligation(wakeSeq uint64, ownAppends int, reason string) error {
	if r.Transport == nil {
		return nil
	}
	t, err := r.Transport.Transport()
	if err != nil {
		return err
	}
	if t != TransportGateway {
		return nil
	}
	sup, ok := r.Gateway.(gatewayObligationSuperseder)
	if !ok {
		return nil
	}
	return sup.Supersede(wakeSeq, ownAppends, reason)
}

// PushTrigger routes to exactly one transport, selected fresh on every call.
//
// Every arm is nil-guarded. A TransportRouter is assembled with whichever arms its
// caller wired -- service.go leaves Legacy nil whenever cfg.Relay does not implement
// PushTriggerer, which is the SUPPORTED no-push configuration (PB-PUSH-5, "no push
// transport is not a failure"), not a construction error. Without the guard the
// DEFAULT transport (legacy_relay, OpenTransportStore's fresh-file starting state)
// dereferences that nil interface on the very first wake-worthy record. Gateway and
// Transport are guarded for the identical reason on an externally-constructed router.
func (r *TransportRouter) PushTrigger(ctx context.Context, target string, env []byte) error {
	if r.Transport == nil {
		return nil
	}
	t, err := r.Transport.Transport()
	if err != nil {
		return err
	}
	switch t {
	case TransportLegacyRelay:
		if r.Legacy == nil {
			return nil
		}
		return r.Legacy.PushTrigger(ctx, target, env)
	case TransportGateway:
		if r.Gateway == nil {
			return nil
		}
		if err := r.Gateway.Trigger(); err != nil {
			return err
		}
		return r.Gateway.Drive(ctx)
	case TransportForegroundOnly:
		return nil
	default:
		return fmt.Errorf("remotegw: unrecognised push_transport %q", t)
	}
}

// --- TransportStore: a single JSON file, in the same durability idiom as
// outbox.go/seqstore.go/obligation.go. ---

const transportSchemaVersion = 1

type transportFile struct {
	SchemaVersion int           `json:"schema_version"`
	Transport     PushTransport `json:"transport"`
}

// errCorruptTransportStore flags an unreadable, wrongly-versioned, or unrecognised
// push_transport file. Fails closed rather than silently defaulting: a silent default
// here could resurrect legacy_relay for a pairing that already migrated off it,
// double-firing wakes on two transports at once -- exactly what P12 forbids.
var errCorruptTransportStore = errors.New("remotegw: corrupt push-transport store file")

type fileTransportStore struct {
	mu        sync.Mutex
	path      string
	transport PushTransport
}

var _ TransportStore = (*fileTransportStore)(nil)

// OpenTransportStore opens the durable push_transport store at path. A missing file
// starts fresh at TransportLegacyRelay (PG-MIG-1/2: every pairing begins on
// legacy_relay). A present-but-corrupt or unrecognised-value file fails closed. An
// empty path returns a purely in-memory store.
func OpenTransportStore(path string) (TransportStore, error) {
	s := &fileTransportStore{path: path, transport: TransportLegacyRelay}
	if path == "" {
		return s, nil
	}
	t, err := loadTransport(path)
	if err != nil {
		return nil, err
	}
	s.transport = t
	return s, nil
}

func (s *fileTransportStore) Transport() (PushTransport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transport, nil
}

func (s *fileTransportStore) SetTransport(t PushTransport) error {
	if !validPushTransport(t) {
		return fmt.Errorf("remotegw: refusing unrecognised push_transport %q", t)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path != "" {
		if err := persistTransport(s.path, t); err != nil {
			return err
		}
	}
	s.transport = t
	return nil
}

func loadTransport(path string) (PushTransport, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return TransportLegacyRelay, nil
	}
	if err != nil {
		return "", fmt.Errorf("read push-transport store: %w", err)
	}
	var f transportFile
	if err := json.Unmarshal(data, &f); err != nil {
		return "", fmt.Errorf("%w: %s: %v", errCorruptTransportStore, path, err)
	}
	if f.SchemaVersion != transportSchemaVersion {
		return "", fmt.Errorf("%w: %s: schema version %d unsupported (want %d)",
			errCorruptTransportStore, path, f.SchemaVersion, transportSchemaVersion)
	}
	if !validPushTransport(f.Transport) {
		return "", fmt.Errorf("%w: %s: unrecognised transport %q", errCorruptTransportStore, path, f.Transport)
	}
	return f.Transport, nil
}

func persistTransport(path string, t PushTransport) error {
	data, err := json.Marshal(transportFile{SchemaVersion: transportSchemaVersion, Transport: t})
	if err != nil {
		return err
	}
	return writeFileAtomic(path, ".push-transport-*", data)
}
