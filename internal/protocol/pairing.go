package protocol

import (
	"context"
	"sync"
	"time"
)

// PairStartReq is the pairing request handlePairStart translates from the wire
// (Control.Pairing) and hands the PairingHost: the capability tier the new device
// should be granted and the rendezvous TTL.
type PairStartReq struct {
	Capability string
	TTLSeconds int
}

// PairView is the synchronous rendezvous view BeginPairing returns (the pair_start
// reply): the QR to display, the rendezvous correlation id, and the daemon-
// authoritative expiry.
type PairView struct {
	QR           string
	RendezvousID string
	ExpiresAt    *time.Time
}

// PairResult is the terminal pairing outcome the host reports via the result
// callback (exactly once). On success DeviceID/Name/Capability describe the newly
// paired device; on failure Err is set and the identity fields are empty.
type PairResult struct {
	DeviceID   string
	Name       string
	Capability string
	Err        error
}

// PairingHost is the OPTIONAL interface the assembled daemon implements so an
// owner-tier Server can host a pairing. BeginPairing creates the rendezvous + QR
// SYNCHRONOUSLY (returned in PairView) and runs the handshake in a background
// goroutine: it calls confirm(sas, deviceName) at the anti-MITM SAS gate (blocking
// until the human decides) and result(...) EXACTLY ONCE at the terminal outcome.
// ctx cancellation (connection drop / TTL) MUST make an in-flight confirm return a
// NON-NIL error — fail closed, i.e. decline.
type PairingHost interface {
	BeginPairing(ctx context.Context, req PairStartReq,
		confirm func(sas []string, deviceName string) (bool, error),
		result func(PairResult)) (PairView, error)
}

// pairSession is one connection's in-flight pairing state. confirm carries the
// human's SAS-gate decision from handlePairConfirm to the blocked confirm closure
// (buffered cap 1, non-blocking send). cancel ends the connection-derived ctx at
// the terminal outcome. rvz (the rendezvous id, known only after BeginPairing
// returns the PairView) is guarded by mu because the confirm closure — running in
// the host's background goroutine — may read it concurrently with handlePairStart
// setting it.
type pairSession struct {
	confirm chan bool
	cancel  context.CancelFunc

	mu  sync.Mutex
	rvz string
}
