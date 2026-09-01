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
	// MaxDurableObjects caps caller-controlled durable rows and nested mailbox
	// buckets across the whole relay. It is the public-service Sybil fence: a
	// distributed caller cannot evade it by minting more source addresses or
	// relay identities. A value <= 0 disables the fence.
	MaxDurableObjects int64 `json:"max_durable_objects"`
	// DurableGrowthWritesPerMin caps, gateway-wide, transactions that can add
	// durable state. Cleanup, acknowledgement, token deletion, and revocation
	// are deliberately exempt so a full relay can always shed state. A value
	// <= 0 disables the fence.
	DurableGrowthWritesPerMin int `json:"durable_growth_writes_per_min"`
	// MaxDBBytes refuses durable-growth transactions once the bbolt file has
	// reached this size. Deleting transactions remain available; bbolt reuses
	// freed pages even though its file does not shrink. A value <= 0 disables
	// the fence.
	MaxDBBytes int64 `json:"max_db_bytes"`
	// MaxConcurrentRendezvous caps live pairing rendezvous slots.
	MaxConcurrentRendezvous int `json:"max_concurrent_rendezvous"`
	// MaxConcurrentConnections is the global cap on live websocket connections
	// admitted at once (CR-1 admission control). A value <= 0 means unlimited;
	// the (cap+1)th concurrent connection is cleanly closed, not served.
	MaxConcurrentConnections int `json:"max_concurrent_connections"`
	// MaxConcurrentConnectionsPerSource caps live websocket connections admitted
	// at once from a single transport source (CR-1 slice 2), alongside the global
	// MaxConcurrentConnections cap, so one source cannot monopolize the whole
	// connection pool. A value <= 0 means unlimited.
	MaxConcurrentConnectionsPerSource int `json:"max_concurrent_connections_per_source"`
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
	// DiskFreeMinBytes is the low-disk alarm threshold (playbook 6.5): /readyz
	// on the admin listener refuses (503) once free space on the DBPath
	// filesystem falls below this many bytes, and the relay logs one bounded
	// warning per transition into the low state (never once per poll). A value
	// <= 0 disables the check.
	DiskFreeMinBytes int64 `json:"disk_free_min_bytes"`
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
	// AdminListen is the TCP listen address for the health/readiness admin
	// surface (playbook 6.5: /healthz, /readyz) -- a SEPARATE port from Listen,
	// never the public one. Start REFUSES a non-loopback value: the doctor
	// rule ("the normal public protocol gains no privileged unauthenticated
	// endpoint") applies to health too, so this is not a flag that can turn
	// off the loopback restriction, only one that names which loopback
	// address/port to use. Empty disables admin serving entirely.
	AdminListen string `json:"admin_listen"`
	// OperatorSecretFile is the path to the generated high-entropy relay
	// operator secret (playbook 6.5: "for diagnostic/admin authority"). If the
	// file does not yet exist, the relay generates one at boot and persists it
	// here at 0600 (EnsureOperatorSecret); if it exists, the existing secret is
	// reused. The secret is NEVER logged. It is not a substitute for Web-PKI
	// server authentication (playbook 6.5) -- the future `swarm relay doctor`
	// capability (a separate R2 slice) is its consumer. Empty disables
	// generation entirely, matching PushCredentials' opt-in shape.
	OperatorSecretFile string `json:"operator_secret_file"`
	// PushCredentials is the path to a Google service-account JSON document. Set, the
	// shipped binary constructs the FCM sender (internal/remote/push) and injects it via
	// WithPushSink; EMPTY, the relay runs with NO push transport at all, which is a
	// supported configuration -- pushes are dropped and every other path is unaffected
	// (PB-PUSH-5). A path that is set but unreadable or invalid fails the boot: silently
	// running push-less because a credential moved is precisely the failure an operator
	// only learns about from a user who missed a hand-off.
	PushCredentials string `json:"push_credentials"`

	// TrustedProxies lists CIDRs whose connections are a trusted reverse proxy,
	// never a client (playbook 6.5). Default empty means today's behavior
	// unchanged: every per-source quota keys off the raw TCP peer address. When
	// the peer that reached the relay falls inside one of these CIDRs, the
	// relay instead keys quotas off the client address recovered from the LAST
	// (rightmost) X-Forwarded-For hop -- the one hop that proxy itself
	// appended -- so a Caddy-in-front-of-a-loopback-relay deployment
	// (docs/operations/relay-vps-deploy.md) stops collapsing every real client
	// into Caddy's one shared bucket. See resolveSourceAddr.
	TrustedProxies []string `json:"trusted_proxies"`

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
	// MaxServerWait is the ceiling on a bounded server-side wait (mailbox_wait,
	// ADR-007 B7): with nothing to deliver the relay answers a clean empty page
	// rather than holding the socket open indefinitely. §6.0 sets 25 s, chosen to
	// sit under the 30-60 s idle timeout common to intermediaries, so the relay
	// always answers before a proxy severs the connection. A value <= 0 selects
	// defaultMaxServerWait.
	MaxServerWait time.Duration `json:"max_server_wait"`
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
		AdminListen:      "", // off unless configured; deploy/relay/relay.config.example sets the documented port
		TLSMode:          "off",
		DBPath:           "relay.db",
		HandshakeTimeout: 30 * time.Second,
		PresenceTimeout:  30 * time.Second,
		RendezvousTTL:    60 * time.Second,
		MaxServerWait:    defaultMaxServerWait,
		RetentionCap:     7 * 24 * time.Hour,
		Quotas: Quotas{
			// Public defaults are generous but finite. They are global backstops,
			// independent of the per-source and per-mailbox controls below.
			MaxDurableObjects:         2_000_000,
			DurableGrowthWritesPerMin: 60_000,
			MaxDBBytes:                16 * 1024 * 1024 * 1024,
			MaxConcurrentRendezvous:   1024,
			MaxConcurrentConnections:  4096,
			// ponytail: generous-but-bounded per-source default, same spirit as the
			// MailboxMaxItems default below — high enough that no legitimate single
			// source (one client, one NAT'd fleet) trips it, low enough that one
			// source cannot exhaust the global connection pool. Tunable.
			MaxConcurrentConnectionsPerSource: 64,
			MailboxAppendPerMin:               600,
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
			// ponytail: generous default (1 GiB) -- enough headroom on any real VPS
			// disk that a legitimate operator never trips it, low enough that it
			// still fires well before bbolt writes start failing outright. Tunable.
			DiskFreeMinBytes: 1024 * 1024 * 1024,
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
