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
	// MailboxAppendPerMin caps appends per target routing id per minute.
	MailboxAppendPerMin int `json:"mailbox_append_per_min"`
	// PushPerMin caps push triggers per target routing id per minute.
	PushPerMin int `json:"push_per_min"`
	// ConnPerMin caps authenticated connections accepted per minute.
	ConnPerMin int `json:"conn_per_min"`
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

	// PresenceTimeout is how long after a gateway drop presence goes offline and
	// the silent-push path fires (R-REL.3).
	PresenceTimeout time.Duration `json:"presence_timeout"`
	// RendezvousTTL is the hard relay-side pairing-rendezvous lifetime (R-PAIR.6).
	RendezvousTTL time.Duration `json:"rendezvous_ttl"`
	// RetentionCap purges mailbox items this old even if never acked (R-REL.10).
	RetentionCap time.Duration `json:"retention_cap"`

	Quotas Quotas `json:"quotas"`
}

// DefaultConfig returns a config with safe, generous defaults. Callers override
// Listen/TLSMode/DBPath (and tighten quotas) before New.
func DefaultConfig() Config {
	return Config{
		Listen:          "127.0.0.1:0",
		TLSMode:         "off",
		DBPath:          "relay.db",
		PresenceTimeout: 30 * time.Second,
		RendezvousTTL:   60 * time.Second,
		RetentionCap:    7 * 24 * time.Hour,
		Quotas: Quotas{
			MaxConcurrentRendezvous: 1024,
			MailboxAppendPerMin:     600,
			PushPerMin:              600,
			ConnPerMin:              600,
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
