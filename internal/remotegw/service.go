package remotegw

import (
	"context"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
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
	Now            func() time.Time  // envelope issued-at clock (nil => time.Now)
}

// Service is the supervised gateway runtime (R-GW.1): it composes the journal-OUT
// bridge (Gateway.RunJournal delivering to a RelaySink that seals and appends to the
// phone's mailbox) and the command-IN loop (CommandBridge polling the machine's
// mailbox) over one relay connection. It is the body of the cmd/swarm-remote sidecar
// process; a crash leaves the daemon and its sessions untouched (S1) and the runtime
// resumes journal delivery from its last durable cursor.
type Service struct {
	cfg    ServiceConfig
	gw     *Gateway
	bridge *CommandBridge
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
	sink := NewRelaySink(RelayConfig{
		Appender:       cfg.Relay,
		Target:         cfg.PhoneTarget,
		EpochID:        cfg.EpochID,
		Key:            cfg.Key,
		RecipientKeyID: cfg.RecipientKeyID,
		SenderKeyID:    cfg.SenderKeyID,
		Now:            cfg.Now,
	})
	gw := New(cfg.DaemonSocket, sink)
	forwarder := cfg.Forwarder
	if forwarder == nil {
		forwarder = gw
	}
	bridge := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     cfg.Relay,
		Forwarder:   forwarder,
		Key:         cfg.Key,
		EpochID:     cfg.EpochID,
		ReplyTarget: cfg.PhoneTarget,
	})
	return &Service{cfg: cfg, gw: gw, bridge: bridge}
}

// Gateway exposes the underlying journal bridge (e.g. to seed or read its cursor).
func (s *Service) Gateway() *Gateway { return s.gw }

// CommandBridge exposes the underlying command loop (e.g. to seed its cursor).
func (s *Service) CommandBridge() *CommandBridge { return s.bridge }

// Run drives both loops until ctx is cancelled, then returns ctx.Err(). The two
// loops are independent: a failing journal connection (retried with ReconnectDelay)
// does not stall the command loop, and vice versa.
func (s *Service) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.runJournal(ctx) }()
	go func() { defer wg.Done(); _ = s.bridge.Run(ctx, s.cfg.PollInterval) }()
	wg.Wait()
	return ctx.Err()
}

// runJournal runs the journal bridge, reconnecting after ReconnectDelay whenever the
// connection drops, until ctx is cancelled. RunJournal resumes from the last delivered
// cursor, so a reconnect loses no events.
func (s *Service) runJournal(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		_ = s.gw.RunJournal(ctx)
		if ctx.Err() != nil {
			return
		}
		// Back off before reconnecting, but wake immediately on cancel.
		t := time.NewTimer(s.cfg.ReconnectDelay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}
