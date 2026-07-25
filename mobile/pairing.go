package swarmmobile

// The pairing surface (PB-PAIR-2/-4/-5/-6, PB-SAS-1/-2).
//
// Decoding a QR and JOINING what it names are separate calls on purpose: PB-PAIR-6
// requires the destination to be displayed and confirmed before anything is joined, and
// that is impossible if the two are one call.
//
// The SAS is computed by the SHARED Go core from the Noise channel binding and crosses as
// ONE display string. The emoji table is never re-implemented on the Kotlin side: a
// second table is a second source of truth, and the two ends disagreeing is
// indistinguishable, to the user, from the man-in-the-middle the SAS exists to catch.

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"sync"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// DecodeQR parses a scanned pairing QR into what the scanner screen may DISPLAY. The
// pairing secret is deliberately not part of the result: it never leaves the Go core.
// Fails closed on a malformed payload.
func DecodeQR(qr string) (p *QRPayload, err error) {
	defer barrier(&err)
	payload, err := pairing.DecodeQR(qr)
	if err != nil {
		return nil, err
	}
	return &QRPayload{
		RelayURL:     payload.RelayURL,
		RendezvousID: hex.EncodeToString(payload.RendezvousID[:]),
		HasStaticPub: len(payload.MachineStaticPub) == 32,
	}, nil
}

// Pairing is one in-flight pairing attempt. It is a handle, not a value: the handshake
// runs on a Go goroutine and the screen polls it.
type Pairing struct {
	mu     sync.Mutex
	origin string
	sas    string
	state  string
	err    error

	confirmed chan struct{}
	once      sync.Once
	cancel    context.CancelFunc
	conn      *relay.Conn
	app       *App
}

// BeginPairing joins the rendezvous the scanned QR names and drives the device side of
// the handshake. Nothing is pinned until Confirm.
func (a *App) BeginPairing(qr string) (p *Pairing, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	payload, err := pairing.DecodeQR(qr)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	conn, err := relay.DialRaw(ctx, payload.RelayURL)
	if err != nil {
		cancel()
		return nil, err
	}

	pr := &Pairing{
		origin:    payload.RelayURL,
		state:     "pairing",
		confirmed: make(chan struct{}),
		cancel:    cancel,
		conn:      conn,
		app:       a,
	}

	ks := core.KeyStore()
	params := pairing.DeviceParams{
		Static:           ks.NoiseStatic(),
		Secret:           payload.PairingSecret,
		RendezvousID:     payload.RendezvousID,
		MachineStaticPub: payload.MachineStaticPub,
		Payload: pairing.DevicePayload{
			DeviceName:           "swarm phone",
			DeviceRoutingID:      []byte(relay.RoutingID(ks.RelayAuthPublic())),
			DeviceRelayAuthPub:   ks.RelayAuthPublic(),
			RecipientPub:         ks.RecipientPublic(),
			DeviceCommandSignPub: ks.CommandSigningPublic(),
		},
		// The SAS gate: surfaced to the screen, then held until the operator has compared
		// it against the machine's own display. Returning an error here fails the pairing
		// CLOSED -- nothing is pinned.
		DeviceSAS: func(ctx context.Context, sas [6]string) error {
			pr.setSAS(strings.Join(sas[:], " "))
			select {
			case <-pr.confirmed:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	rt := &rendezvous{conn: conn, label: hex.EncodeToString(payload.RendezvousID[:])}
	go pr.run(ctx, params, rt)
	return pr, nil
}

func (p *Pairing) run(ctx context.Context, params pairing.DeviceParams, rt pairing.RendezvousTransport) {
	out, err := pairing.RunDevice(ctx, params, rt)
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.conn.Close()
	if p.state == "cancelled" {
		return
	}
	switch {
	case err == nil:
		p.state = "paired"
	case errors.Is(err, pairing.ErrPairingDeclined):
		p.state = "declined"
	case errors.Is(err, pairing.ErrRateLimited):
		p.state = "rate_limited"
	case ctx.Err() != nil:
		p.state = "cancelled"
	default:
		p.state, p.err = "failed", err
	}
	if out != nil {
		p.app.pin(out)
	}
}

// pin records the machine coordinates the handshake authenticated (PB-PAIR-7). It is the
// only place MachineRelayAuthPub is learned: the phone's send target derives from it, and
// without it the restored phone would know who the machine is and not how to reach it.
//
// A pairing that lands in a DIFFERENT epoch invalidates every epoch-scoped coordinate at
// once, and both of them are re-armed here rather than at the next process start. The
// tier keys belong to the old epoch: sealing under them while labelling the frame with
// the new epoch id yields frames the machine cannot open. The adopted rollback
// authorities belong to the old epoch too, and bound nothing in the new one -- so
// mutating ops must be refused until the machine republishes (PB-SYNC-7). NewApp already
// re-arms both on the next launch by comparing ReconciledEpoch against EpochID; on
// Android that launch can be hours away, and the whole window is one in which the live
// App permits mutations it cannot bound.
func (a *App) pin(out *pairing.DeviceOutcome) {
	st := a.core.State()
	st.MachineStatic = out.MachineStatic
	st.MachineSignPub = out.Machine.MachineSignPub
	st.MachineRelayAuthPub = out.Machine.MachineRelayAuthPub
	newEpoch := st.EpochID != out.Machine.EpochID
	if newEpoch {
		st.Keys = crypto.EpochKeys{}
	}
	st.EpochID = out.Machine.EpochID
	if err := a.core.Save(st); err != nil {
		return
	}
	if newEpoch {
		a.mu.Lock()
		a.reconciled = false
		a.mu.Unlock()
	}
	a.setDestination(out.Machine.MachineRelayAuthPub)
}

func (p *Pairing) setSAS(s string) {
	p.mu.Lock()
	if p.state == "pairing" {
		p.sas = s
	}
	p.mu.Unlock()
}

// SAS is the six-emoji short authentication string as ONE display string, computed by the
// shared Go core. It errors until the handshake has derived it, and on a dead session.
func (p *Pairing) SAS() (sas string, err error) {
	defer barrier(&err)
	if p == nil {
		return "", errNoReceiver
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state != "pairing" && p.state != "confirming" {
		msg := "swarmmobile: pairing session is " + p.state + "; there is no SAS to compare"
		if p.err != nil {
			return "", errors.New(msg + ": " + p.err.Error())
		}
		return "", errors.New(msg)
	}
	if p.sas == "" {
		return "", errors.New("swarmmobile: the handshake has not derived a SAS yet")
	}
	return p.sas, nil
}

// Origin is the destination the scanned QR named, for the confirm sheet to render.
func (p *Pairing) Origin() (origin string, err error) {
	defer barrier(&err)
	if p == nil {
		return "", errNoReceiver
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state == "" {
		return "", errNoReceiver
	}
	return p.origin, nil
}

// State is the pairing state machine as a user-legible string: pairing, confirming,
// paired, declined, cancelled, rate_limited, failed.
func (p *Pairing) State() (state string, err error) {
	defer barrier(&err)
	if p == nil {
		return "", errNoReceiver
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state == "" {
		return "", errNoReceiver
	}
	return p.state, nil
}

// Confirm records that the operator compared the two SAS displays and they matched. It
// releases the handshake; it does not block on the machine's decision.
func (p *Pairing) Confirm() (err error) {
	defer barrier(&err)
	if p == nil {
		return errNoReceiver
	}
	p.mu.Lock()
	if p.state != "pairing" {
		state := p.state
		p.mu.Unlock()
		if state == "" {
			return errNoReceiver
		}
		return errors.New("swarmmobile: cannot confirm a pairing that is " + state)
	}
	if p.sas == "" {
		p.mu.Unlock()
		return errors.New("swarmmobile: cannot confirm before a SAS has been derived")
	}
	p.state = "confirming"
	p.mu.Unlock()
	p.once.Do(func() { close(p.confirmed) })
	return nil
}

// Cancel abandons the pairing. It is a TERMINAL state, not a hang: the rendezvous is
// dropped, the handshake fails closed, and nothing is pinned.
func (p *Pairing) Cancel() (err error) {
	defer barrier(&err)
	if p == nil {
		return errNoReceiver
	}
	p.mu.Lock()
	if p.state == "" {
		p.mu.Unlock()
		return errNoReceiver
	}
	p.state, p.sas = "cancelled", ""
	cancel, conn := p.cancel, p.conn
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.Close()
	}
	return nil
}

// rendezvous adapts a raw relay connection to the pairing package's transport seam. The
// relay only ever forwards opaque bytes; it never sees the pairing secret or any
// handshake plaintext.
type rendezvous struct {
	conn  *relay.Conn
	label string
}

func (r *rendezvous) Create(ctx context.Context, id string) error {
	return r.conn.RendezvousCreate(ctx, id)
}
func (r *rendezvous) Claim(ctx context.Context, id string) error {
	return r.conn.RendezvousClaim(ctx, id)
}
func (r *rendezvous) Send(ctx context.Context, msg []byte) error {
	return r.conn.RendezvousSend(ctx, r.label, msg)
}
func (r *rendezvous) Recv(ctx context.Context) ([]byte, error) { return r.conn.RendezvousRecv(ctx) }
func (r *rendezvous) Complete(ctx context.Context, id string) error {
	return r.conn.RendezvousComplete(ctx, id)
}

var _ pairing.RendezvousTransport = (*rendezvous)(nil)
