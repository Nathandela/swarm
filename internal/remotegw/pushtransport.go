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

var (
	_ PushTriggerer         = (*TransportRouter)(nil)
	_ obligationPreAppender = (*TransportRouter)(nil)
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
