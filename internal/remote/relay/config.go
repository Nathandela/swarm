package relay

import (
	"encoding/json"
	"os"
	"time"
)

// Quotas are the day-one abuse controls (R-REL.8). Every window is evaluated on
// the injected clock. Defaults are generous; an operator tightens them per
// deployment.
type Quotas struct {
	// MaxConcurrentRendezvous caps live pairing rendezvous slots.
	MaxConcurrentRendezvous int `json:"max_concurrent_rendezvous"`
	// MaxConcurrentConnections is the global cap on live websocket connections
	// admitted at once (CR-1 admission control). A value <= 0 means unlimited;
	// the (cap+1)th concurrent connection is cleanly closed, not served.
	MaxConcurrentConnections int `json:"max_concurrent_connections"`
	// MailboxAppendPerMin caps appends per target routing id per minute.
	MailboxAppendPerMin int `json:"mailbox_append_per_min"`
	// MailboxMaxItems is the per-mailbox depth cap (CR-4): an append that would
	// push a target's live mailbox past this many items is refused with a clean
	// ErrQuotaExceeded before storing, so a device that never drains cannot drive
	// unbounded growth. The cap is on live depth, so capacity recovers on ack. A
	// value <= 0 means no depth cap.
	MailboxMaxItems int `json:"mailbox_max_items"`
	// PushPerMin caps push triggers per target routing id per minute.
	PushPerMin int `json:"push_per_min"`
	// ConnPerMin caps pre-signature authentication attempts (auth_init) per
	// TRANSPORT SOURCE (client IP; the presented, still-unproven relay-auth pubkey
	// is NEVER a rate key). There is no global auth counter (ADR-007 amendment
	// 2026-07-20).
	ConnPerMin int `json:"conn_per_min"`
	// OpsPerMin is the per-source cap applied to every state-touching control op
	// (auth_resp, authorize_device, mailbox_read/ack, token_register/delete,
	// presence, device_revoke, and the rendezvous ops). mailbox_append and
	// push_trigger keep their own dedicated windows above. A value <= 0 means
	// unlimited.
	OpsPerMin int `json:"ops_per_min"`
}

// Config is the relay's on-disk configuration (R-REL.9). cmd/swarm-relay reads
// exactly one of these and boots.
type Config struct {
	// Listen is the TCP listen address (host:port; :0 for an ephemeral port).
	Listen string `json:"listen"`
	// TLSMode is "off" for plain ws:// (E2EE does not depend on TLS) or "on"
	// for a metadata-defense TLS terminator.
	TLSMode string `json:"tls_mode"`
	// DBPath is the bbolt persistence file.
	DBPath string `json:"db_path"`

	// HandshakeTimeout bounds a read on a connection that has not yet
	// authenticated or joined a rendezvous: an idle socket that completes the ws
	// handshake but sends no frame is closed within it (CR-1 slowloris defense).
	// A value <= 0 disables the bound.
	HandshakeTimeout time.Duration `json:"handshake_timeout"`
	// PresenceTimeout is how long after a gateway drop presence goes offline and
	// the silent-push path fires (R-REL.3).
	PresenceTimeout time.Duration `json:"presence_timeout"`
	// RendezvousTTL is the hard relay-side pairing-rendezvous lifetime (R-PAIR.6).
	RendezvousTTL time.Duration `json:"rendezvous_ttl"`
	// RetentionCap purges mailbox items this old even if never acked (R-REL.10).
	RetentionCap time.Duration `json:"retention_cap"`
	// SweepInterval is the cadence at which Start runs the clock-driven maintenance
	// sweeps (presence-went-silent pushes + retention purges) on a timer (CR-3). A
	// value <= 0 disables the loop, leaving the sweeps to be invoked manually — the
	// DefaultConfig value, so existing manual-sweep tests stay deterministic. The
	// shipped binary (cmd/swarm-relay) sets a non-zero production value.
	SweepInterval time.Duration `json:"sweep_interval"`

	Quotas Quotas `json:"quotas"`
}

// DefaultConfig returns a config with safe, generous defaults. Callers override
// Listen/TLSMode/DBPath (and tighten quotas) before New.
func DefaultConfig() Config {
	return Config{
		Listen:           "127.0.0.1:0",
		TLSMode:          "off",
		DBPath:           "relay.db",
		HandshakeTimeout: 30 * time.Second,
		PresenceTimeout:  30 * time.Second,
		RendezvousTTL:    60 * time.Second,
		RetentionCap:     7 * 24 * time.Hour,
		Quotas: Quotas{
			MaxConcurrentRendezvous:  1024,
			MaxConcurrentConnections: 4096,
			MailboxAppendPerMin:      600,
			// ponytail: CR-4 per-mailbox depth cap, ON by default. Enforcement rejects
			// an over-cap append with ErrQuotaExceeded (server.go:719) rather than
			// dropping data, and on the journal-OUT path the gateway's ack-gated
			// cursor (GW-H1) means a rejected append is re-read/retried, not lost —
			// so hitting the cap applies back-pressure (delivery stalls until the
			// device drains) instead of silent loss. 10000 is generous for a
			// legitimately-offline device while bounding unbounded growth. Tunable.
			MailboxMaxItems: 10000,
			PushPerMin:      600,
			ConnPerMin:      600,
			OpsPerMin:       600,
		},
	}
}

// WriteConfigFile writes cfg as JSON, 0600.
func WriteConfigFile(path string, cfg Config) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// LoadConfig reads and parses a config file. A missing or malformed file is a
// clean error (the binary fails closed), never silent defaults.
func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	cfg := DefaultConfig()
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
