package remotegw

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
)

// ServiceConfig configures the gateway runtime. The Service depends only on the
// daemon remote socket path and the relay Mailbox seam, so it runs against a real
// relay in production and a fake one in tests.
type ServiceConfig struct {
	DaemonSocket   string            // the daemon remote.sock the journal bridge dials
	Relay          Mailbox           // the relay client (machine mailbox read + phone mailbox append)
	Forwarder      CommandForwarder  // optional override; nil => the built-in Gateway forwards commands
	PhoneTarget    string            // the phone's relay routing id (journal + reply target)
	Key            crypto.ContentKey // the epoch content key shared with the phone
	EpochID        uint32            // the epoch the content key belongs to
	RecipientKeyID [8]byte           // phone routing key id stamped on sealed journal envelopes
	SenderKeyID    [8]byte           // this machine's routing key id
	PollInterval   time.Duration     // command-IN poll cadence (default 500ms)
	ReconnectDelay time.Duration     // journal reconnect backoff (default 1s)
	LeaseAwait     time.Duration     // how long take_control waits for the lease grant (default 5s)
	Now            func() time.Time  // envelope issued-at clock (nil => time.Now)
	JournalSeq     SeqSource         // durable outbound seq for journal + terminal frames (nil => in-memory)
	ReplySeq       SeqSource         // durable outbound seq for command replies (nil => in-memory)
	// Post-revocation confidentiality (codex#1): the epoch key + phone target are fixed for
	// this process's lifetime, so after the owner revokes the paired device (rotating the
	// epoch key) a still-running gateway would reconnect and reseal epoch frames to the
	// revoked mailbox under the STALE key. On each journal reconnect the runtime re-reads
	// <StateDir>/devices and, if DeviceID is gone, exits instead. Both empty disables the
	// check (unit tests that do not provision a registry).
	StateDir string // state dir whose <StateDir>/devices registry is re-read on reconnect
	DeviceID string // this gateway's paired device; its removal triggers a graceful exit
}

// ErrDeviceRevoked is returned by Run when the gateway's paired device is no longer in the
// registry: the owner revoked it (rotating the epoch key), so the gateway shuts down rather
// than reconnecting and resealing epoch journal/snapshot frames to the revoked device's
// mailbox under the now-stale key (codex#1 / post-revocation confidentiality).
var ErrDeviceRevoked = errors.New("remotegw: paired device revoked; gateway exiting")

// Service is the supervised gateway runtime (R-GW.1): it composes the journal-OUT
// bridge (Gateway.RunJournal delivering to a RelaySink that seals and appends to the
// phone's mailbox) and the command-IN loop (CommandBridge polling the machine's
// mailbox) over one relay connection. It is the body of the cmd/swarm-remote sidecar
// process; a crash leaves the daemon and its sessions untouched (S1) and the runtime
// resumes journal delivery from its last durable cursor.
type Service struct {
	cfg      ServiceConfig
	gw       *Gateway
	bridge   *CommandBridge
	leases   *LeaseManager
	watchers *TerminalWatcher
}

// NewService builds a runtime over cfg. It wires a RelaySink onto a Gateway for the
// journal-OUT direction and a CommandBridge for the command-IN direction, both bound
// to the same content key and phone target.
func NewService(cfg ServiceConfig) *Service {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}
	if cfg.ReconnectDelay <= 0 {
		cfg.ReconnectDelay = time.Second
	}
	if cfg.LeaseAwait <= 0 {
		cfg.LeaseAwait = 5 * time.Second
	}
	sink := NewRelaySink(RelayConfig{
		Appender:       cfg.Relay,
		Target:         cfg.PhoneTarget,
		EpochID:        cfg.EpochID,
		Key:            cfg.Key,
		RecipientKeyID: cfg.RecipientKeyID,
		SenderKeyID:    cfg.SenderKeyID,
		Now:            cfg.Now,
		Seq:            cfg.JournalSeq,
	})
	gw := New(cfg.DaemonSocket, sink)
	forwarder := cfg.Forwarder
	if forwarder == nil {
		forwarder = gw
	}
	// The input plane: take_control opens a persistent lease conn on the daemon
	// remote.sock, and every keystroke/resize for that session rides THAT conn.
	leases := NewLeaseManager(cfg.DaemonSocket, cfg.LeaseAwait)
	// The peek plane: terminal_watch runs a read-only terminal_subscribe against the daemon
	// (via the SAME Gateway/RelaySink as the journal), sealing each rendered snapshot to the
	// phone. It reconnects on the journal backoff cadence.
	watchers := NewTerminalWatcher(gw, cfg.ReconnectDelay)
	bridge := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     cfg.Relay,
		Forwarder:   forwarder,
		Leases:      leases,
		Watchers:    watchers,
		Key:         cfg.Key,
		EpochID:     cfg.EpochID,
		ReplyTarget: cfg.PhoneTarget,
		ReplySeq:    cfg.ReplySeq,
	})
	return &Service{cfg: cfg, gw: gw, bridge: bridge, leases: leases, watchers: watchers}
}

// Gateway exposes the underlying journal bridge (e.g. to seed or read its cursor).
func (s *Service) Gateway() *Gateway { return s.gw }

// CommandBridge exposes the underlying command loop (e.g. to seed its cursor).
func (s *Service) CommandBridge() *CommandBridge { return s.bridge }

// Run drives both loops until ctx is cancelled, then returns ctx.Err(). The two
// loops are independent: a failing journal connection (retried with ReconnectDelay)
// does not stall the command loop, and vice versa.
func (s *Service) Run(ctx context.Context) error {
	// Tear down every live lease conn AND every terminal peek on shutdown so no daemon
	// connection (control gate or read-only tap) is left behind after the sidecar exits.
	defer func() { _ = s.leases.Close() }()
	defer func() { _ = s.watchers.Close() }()
	// Derive a cancelable context so the journal loop can tear the WHOLE Service down (both
	// loops) the moment it detects the paired device was revoked (codex#1).
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Parent the peek watchers to the Service ctx so a revoke (cancel below) stops every peek
	// reconnecting IMMEDIATELY and structurally -- not incidentally via the kill switch, and not
	// only when the deferred watchers.Close runs after wg.Wait returns (opus#2).
	s.watchers.bindParent(ctx)
	var revoked atomic.Bool
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if s.runJournal(ctx) {
			revoked.Store(true)
			cancel() // stop the command loop too, so Run returns promptly
		}
	}()
	go func() { defer wg.Done(); _ = s.bridge.Run(ctx, s.cfg.PollInterval) }()
	wg.Wait()
	if revoked.Load() {
		return ErrDeviceRevoked
	}
	return ctx.Err()
}

// runJournal runs the journal bridge, reconnecting after ReconnectDelay whenever the
// connection drops, until ctx is cancelled. RunJournal resumes from the last delivered
// cursor, so a reconnect loses no events. It returns true when it stopped because the
// paired device was definitively revoked (deviceRevoked) so the caller tears the whole
// Service down.
func (s *Service) runJournal(ctx context.Context) (revoked bool) {
	for {
		if ctx.Err() != nil {
			return false
		}
		_ = s.gw.RunJournal(ctx)
		if ctx.Err() != nil {
			return false
		}
		// The daemon severed the journal connection. A device REVOKE severs it (C2a) and
		// rotates the epoch key, so before reconnecting re-read the registry: if our paired
		// device is CONFIRMED gone we must NOT resume sealing epoch frames to its mailbox under
		// the now-stale key (codex#1). A device still present -- or a registry we cannot read
		// right now -- is an ordinary reconnect: back off and re-check next cycle.
		if s.deviceRevoked() {
			return true
		}
		// Back off before reconnecting, but wake immediately on cancel.
		t := time.NewTimer(s.cfg.ReconnectDelay)
		select {
		case <-ctx.Done():
			t.Stop()
			return false
		case <-t.C:
		}
	}
}

// deviceRevoked reports whether this gateway's paired device is DEFINITIVELY revoked: the
// on-disk registry read SUCCEEDED and this gateway's DeviceID is ABSENT (the owner revoked it,
// rotating the epoch key). It re-reads <StateDir>/devices FRESH on each call so a revocation is
// observed on the next journal reconnect.
//
// It deliberately distinguishes "definitively gone" from "cannot read right now": a device.Open
// error (a torn read, a transiently-unavailable/network-mounted stateDir, a MkdirAll/ReadFile
// hiccup) is NOT a confirmed revocation, so it returns false and the caller keeps reconnecting
// and re-checks next cycle. This check runs on EVERY routine daemon reconnect, so treating a
// transient FS error as a revocation (the prior behavior) would let one coincidental hiccup
// silently and permanently kill remote control until a human restarts the sidecar (Finding 1,
// codex#6 / sonnet#3 / opus#1). The fail-closed intent is preserved for the case we can actually
// confirm -- a successful read showing the device gone still exits promptly. An empty StateDir or
// DeviceID disables the check (returns false) -- used by unit tests that do not provision a
// registry.
//
// Follow-up: device.Open does MkdirAll/Chmod on the registry dir on this read path; a read-only
// registry accessor would avoid writing during the liveness check, but adding one means editing
// device/registry.go (owned elsewhere), so it is deferred.
func (s *Service) deviceRevoked() bool {
	if s.cfg.StateDir == "" || s.cfg.DeviceID == "" {
		return false
	}
	reg, err := device.Open(filepath.Join(s.cfg.StateDir, "devices"))
	if err != nil {
		return false // cannot read the registry right now: not a confirmed revocation -- retry next cycle
	}
	_, ok := reg.Get(s.cfg.DeviceID)
	return !ok
}
