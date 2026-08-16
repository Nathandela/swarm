package remotegw

// The machine-side revoke PRODUCER (bead agents-tracker-u37c; ADR-015 P6, playbook 3.2:
// "Machine-side device revocation uses the machine-revoke capability and retries
// deletion durably after local epoch rotation"). Until this file, HonorMachineRevoke on
// the phone existed with no production path that told the gateway to revoke: the owner
// revoked the device, the local epoch rotated, and the pairing's push address stayed
// live at the gateway forever.
//
// THE SHAPE mirrors the wake obligation one file over (obligation.go): a durable
// obligation is RECORDED before the first network attempt, DRIVEN against the gateway,
// and classified -- 2xx (the tombstone's idempotent 204 included) terminal, transport
// failure and 5xx retryable, so a crash anywhere between the revoke decision and the
// delete loses nothing.
//
// EPOCH INDEPENDENCE IS STRUCTURAL: the obligation record holds the machine-revoke
// capability VERBATIM -- pairing material of ADR-015 P7's family, not epoch material --
// and Drive reads only the record, so the local epoch rotation the revoke performs
// cannot change a byte of the request.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
)

// AddressRevoker deletes one push address at the gateway. It is an interface for the
// obligation machine's sake: the network half is injectable, the durable half is not.
type AddressRevoker interface {
	RevokeAddress(ctx context.Context, addr PushAddress) error
}

// HTTPAddressRevoker is the production AddressRevoker: DELETE /v1/addresses/{addr}
// bearing "Swarm-Revoke <capability>", no body -- push-gateway-api.md 3.4's machine
// arm, the exact request internal/pushgw's handleRevoke verifies. It must never present
// the submit capability's "Swarm-Capability" header: that crossover is PG-AUTH-8's
// forbidden one.
type HTTPAddressRevoker struct {
	BaseURL                 string
	MachineRevokeCapability string
	Client                  *http.Client // nil => http.DefaultClient
}

// errRevokeRetryable marks a refusal the obligation machine must leave pending: a
// transport failure, a 5xx, a 429 -- anything a later retry can fix.
type revokeRetryableError struct{ err error }

func (e *revokeRetryableError) Error() string { return e.err.Error() }
func (e *revokeRetryableError) Unwrap() error { return e.err }

// RevokeAddress presents the machine-revoke capability once. A 2xx -- including the
// PG-REV-2 tombstone's idempotent 204 on a re-presented delete -- is success. A 5xx or
// 429 and any transport failure are retryable; every other status is a terminal
// refusal (a dead capability cannot come alive by retrying).
func (r *HTTPAddressRevoker) RevokeAddress(ctx context.Context, addr PushAddress) error {
	base, err := url.Parse(r.BaseURL)
	if err != nil {
		return fmt.Errorf("remotegw: invalid gateway BaseURL: %w", err)
	}
	endpoint := strings.TrimRight(base.String(), "/") + "/v1/addresses/" +
		base64.RawURLEncoding.EncodeToString(addr[:])
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Swarm-Revoke "+r.MachineRevokeCapability)
	client := r.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return &revokeRetryableError{fmt.Errorf("remotegw: revoke transport failure: %w", err)}
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return &revokeRetryableError{fmt.Errorf("remotegw: gateway answered %d to the revoke; retryable", resp.StatusCode)}
	default:
		return fmt.Errorf("remotegw: gateway refused the revoke with %d", resp.StatusCode)
	}
}

// RevokeObligation is the durable record: the address to delete and nothing an epoch
// rotation can touch. The capability lives in the driving machine's config rather than
// the record so it is never written to disk beside the address it deletes; both halves
// are pairing material conveyed once, and Drive reads only them.
type RevokeObligation struct {
	Address PushAddress
	Done    bool
	// Refusal preserves WHY a terminally-refused obligation resolved: resolved-but-
	// refused is a different durable fact from delivered, and re-presenting a dead
	// capability on every start forever is a wedge, not durability.
	Refusal string
}

// RevokeObligationStore is durable custody for the one revoke obligation a pairing can
// owe, byte-file-backed like OpenObligationStore's wake half.
type RevokeObligationStore struct {
	mu   sync.Mutex
	path string
	ob   RevokeObligation
	live bool
}

// revokeObligationFile is the on-disk shape.
type revokeObligationFile struct {
	Address []byte `json:"address"`
	Done    bool   `json:"done"`
	Refusal string `json:"refusal,omitempty"`
}

// OpenRevokeObligationStore opens the durable revoke-obligation custody at path,
// loading any previously recorded obligation. A missing file is an empty store.
func OpenRevokeObligationStore(path string) (*RevokeObligationStore, error) {
	if path == "" {
		return nil, errors.New("remotegw: revoke obligation store requires a path")
	}
	s := &RevokeObligationStore{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("remotegw: open revoke obligation store: %w", err)
	}
	var f revokeObligationFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("remotegw: corrupt revoke obligation store: %w", err)
	}
	if len(f.Address) != len(PushAddress{}) {
		return nil, fmt.Errorf("remotegw: revoke obligation store holds a %d-byte address", len(f.Address))
	}
	copy(s.ob.Address[:], f.Address)
	s.ob.Done = f.Done
	s.ob.Refusal = f.Refusal
	s.live = true
	return s, nil
}

// Put records ob durably before returning (the reserve-before-effect discipline the
// outbox and the wake obligations already follow).
func (s *RevokeObligationStore) Put(ob RevokeObligation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(revokeObligationFile{Address: ob.Address[:], Done: ob.Done, Refusal: ob.Refusal})
	if err != nil {
		return err
	}
	if err := writeFileAtomic(s.path, ".revoke-obligation-*", data); err != nil {
		return err
	}
	s.ob, s.live = ob, true
	return nil
}

// Pending returns the recorded obligation if it is still undone.
func (s *RevokeObligationStore) Pending() (RevokeObligation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.live || s.ob.Done {
		return RevokeObligation{}, false
	}
	return s.ob, true
}

// RevokeObligationConfig assembles a RevokeObligationMachine. Revoker may be nil until
// Drive: Record must not need the network at all.
type RevokeObligationConfig struct {
	Store   *RevokeObligationStore
	Revoker AddressRevoker
	Address PushAddress
}

// RevokeObligationMachine drives the one durable revoke obligation of a pairing.
type RevokeObligationMachine struct {
	cfg RevokeObligationConfig
}

// NewRevokeObligationMachine constructs the machine over cfg.
func NewRevokeObligationMachine(cfg RevokeObligationConfig) *RevokeObligationMachine {
	return &RevokeObligationMachine{cfg: cfg}
}

// Record durably registers the obligation BEFORE any network attempt: a crash between
// the revoke decision and the delete must lose nothing. A PENDING obligation for a
// DIFFERENT address is never clobbered -- overwriting an undelivered revoke for
// address A with one for address B leaves A live at the gateway with no durable record
// that it should not be -- and re-recording the SAME address is idempotent, because
// the producer records on every revoked exit.
func (m *RevokeObligationMachine) Record() error {
	if m.cfg.Store == nil {
		return errors.New("remotegw: revoke obligation machine has no store")
	}
	if ob, ok := m.cfg.Store.Pending(); ok {
		if ob.Address == m.cfg.Address {
			return nil
		}
		return errors.New("remotegw: a revoke obligation for another address is still pending; refusing to overwrite undelivered custody")
	}
	return m.cfg.Store.Put(RevokeObligation{Address: m.cfg.Address})
}

// Drive presents the pending obligation at the gateway and classifies the outcome: a
// 2xx (the tombstone's idempotent 204 included) resolves it terminally; a transport
// failure or 5xx leaves it pending for the next drive. Driving a resolved obligation is
// a no-op -- a restart racing the terminal write must not error.
func (m *RevokeObligationMachine) Drive(ctx context.Context) error {
	if m.cfg.Store == nil {
		return errors.New("remotegw: revoke obligation machine has no store")
	}
	ob, ok := m.cfg.Store.Pending()
	if !ok {
		return nil
	}
	if m.cfg.Revoker == nil {
		return errors.New("remotegw: revoke obligation machine has no revoker; the delete cannot be presented")
	}
	if err := m.cfg.Revoker.RevokeAddress(ctx, ob.Address); err != nil {
		var retryable *revokeRetryableError
		if errors.As(err, &retryable) {
			return err
		}
		// A terminal refusal (RevokeAddress's own classification: a dead capability
		// cannot come alive by retrying) RESOLVES the obligation, durably, with the
		// reason preserved -- otherwise every start re-presents a request the gateway
		// has already refused, forever. The error still reaches the caller.
		ob.Done = true
		ob.Refusal = err.Error()
		if perr := m.cfg.Store.Put(ob); perr != nil {
			return errors.Join(err, perr)
		}
		return err
	}
	ob.Done = true
	return m.cfg.Store.Put(ob)
}
