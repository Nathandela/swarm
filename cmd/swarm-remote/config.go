// Package main's config assembler for the gateway binary (slice G1):
// resolveGatewayParams reads the provisioned state (machine identity,
// relay.json, the paired-device registry) and returns everything
// remotegw.Service needs except the dialed relay Mailbox (that dial happens
// in slice G2). It fails closed on any missing or ambiguous provisioning
// state rather than returning a partially-populated gatewayParams.
package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/machineid"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/relaycfg"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// gatewayParams is everything remotegw.Service needs to run, minus the
// dialed relay Mailbox (assembled by G2).
type gatewayParams struct {
	DaemonSocket string
	RelayURL     string
	RelayAuth    relay.ClientAuth
	// RelaySecurity is the transport policy the sidecar dials under (PB-NET-2), resolved
	// from the SAME relay.json the URL came from: verified TLS, cleartext refused except
	// to a loopback IP literal, and the operator's SPKI pin when one is configured
	// (ADR-007 B34). It is a resolved VALUE rather than a flag the dial re-derives, so
	// the policy cannot differ between assembly and dial.
	RelaySecurity relay.Security
	// Profile is ADR-016 "profile"'s first real publisher: the machine's relay TLS policy,
	// host and pin, built from the SAME relaycfg.Config the dial policy above reads, and
	// carried into remotegw.ServiceConfig.Profile so every reconcile record publishes it.
	Profile     protocol.RemoteProfileV1
	PhoneTarget string
	Key         crypto.ContentKey
	// WakeKey is the content-free key the push trigger seals its wakes under (PB-PUSH-0).
	// machineid.Load already materialises it in this process -- marshal/unmarshal read one
	// buffer holding both signing privates, the content key AND this -- so resolving it
	// here is dropping a `_` rather than admitting a new secret (ADR-007 B19).
	WakeKey        crypto.WakeKey
	EpochID        uint32
	RecipientKeyID [8]byte
	SenderKeyID    [8]byte
	// GrantSeq is the machine identity's grant-issuance coordinate, the second half of the
	// reconcile record's grant watermark (PB-STATE-4(c), with EpochID). Without it the record
	// carries grant_seq 0, which a phone adopts monotonically -- so it changes nothing, fails
	// nowhere, and silently leaves that coordinate un-anchored after a rollback.
	GrantSeq uint64
	// Post-revocation confidentiality (codex#1): the gateway re-reads <StateDir>/devices on
	// each journal reconnect and exits if DeviceID is gone, so a revoke-then-reconnect can no
	// longer reseal epoch frames to the revoked device under the stale (pre-rotation) key.
	StateDir string
	DeviceID string
	// Durable OUTBOUND seq high-waters (C2b): journal/terminal and command replies are
	// two independent per-(sender,epoch) streams on the phone, so each has its own file.
	// They resume STRICTLY ABOVE the phone's high-water after a restart instead of
	// resetting to 1 and being stale-dropped.
	JournalSeq remotegw.SeqSource
	ReplySeq   remotegw.SeqSource
	// PushSeq is the third outbound stream's durable seq: the wake's replay coordinate
	// (PB-PUSH-3). It is separate because a wake is sealed under a different key and
	// checked by a different receiver on the phone; sharing the journal counter would have
	// the two streams stale-drop each other.
	PushSeq remotegw.SeqSource
	// PushPrefs is the durable record of which transitions may wake the paired device
	// (PB-PUSH-8, PB-PUSH-10). Without it the gateway refuses the push_prefs verb and
	// suppresses every wake, so it is resolved here rather than left to a default.
	PushPrefs remotegw.PushPrefsSource
	// Durable OUTBOUND journal outbox (PB-GW-8): {journal cursor, sealed envelope, relay
	// outcome}. Without it Gateway.cursor is in-memory, so every restart re-reads from 0 and
	// re-appends the WHOLE journal at fresh seqs into the same 600-per-tumbling-minute
	// mailbox -- and a delivery-unknown append is re-sealed at a fresh seq instead of
	// re-appended verbatim, getting the record accepted twice.
	Outbox remotegw.Outbox
	// Durable INBOUND checkpoint (PB-GW-1): the mailbox read cursor and the
	// per-(sender,epoch) replay high-water. Without it a restarted gateway builds a fresh
	// receiver, whose staleness check is SKIPPED on the first frame of every stream, so a
	// relay that never honoured an ack can replay everything it still retains.
	Inbound remotegw.InboundState

	// C5 grant delivery (ADR-007 2026-07-24): the paired device's relay-auth pub is the
	// AuthorizeDevice target that opens the machine->device mailbox route; Grant is the
	// persisted sealed EpochGrant the gateway appends to that mailbox as the phone's
	// bootstrap. Grant is nil when no sidecar was persisted (a pre-grant pairing), which
	// deliverEpochGrant treats as a no-op.
	DeviceRelayAuthPub ed25519.PublicKey
	// DeviceConsentSig is the paired device's relay-route consent for this machine
	// (ADR-007 B27/B38), carried from its registry record. Without it the relay refuses
	// the AuthorizeDevice above and the grant append behind it, so it is resolved here
	// with the key it accompanies rather than looked up later.
	DeviceConsentSig []byte
	Grant            *crypto.EpochGrant

	// PushGateway configures the ADR-015 P9/P12 wake-obligation machine (nil => this
	// pairing has not migrated off legacy_relay, which is every pairing until the
	// optional push-gateway.json below exists). See loadPushGatewayConfig's TODO.
	PushGateway *remotegw.PushGatewayConfig
}

// resolveGatewayParams loads the machine identity, relay URL, and the single
// paired device from stateDir and assembles gatewayParams. It fails closed:
// any missing/corrupt identity, missing/empty/malformed relay.json, or a
// paired-device count other than exactly one is an error, and the returned
// gatewayParams is always the zero value on error.
func resolveGatewayParams(stateDir, daemonSocket string) (gatewayParams, error) {
	id, err := machineid.Load(filepath.Join(stateDir, "remote", "machine.key"))
	if err != nil {
		return gatewayParams{}, fmt.Errorf("load machine identity: %w", err)
	}

	relayCfg, err := loadRelayConfig(stateDir)
	if err != nil {
		return gatewayParams{}, err
	}
	// The transport policy is resolved during ASSEMBLY, alongside the identity and the
	// device registry, so a malformed pin is a provisioning failure the supervision unit
	// reports rather than a dial failure that reads as "the relay is down" (ADR-007 B33).
	relaySecurity, err := relayCfg.Security()
	if err != nil {
		return gatewayParams{}, err
	}
	// ADR-016 "profile": the FIRST real publisher of RelayTLSPolicy/RelayHost/RelaySPKIPin,
	// built from the same relaycfg.Config the transport policy above came from. Pin() is
	// the ONE decoder of relayCfg.SPKIPin (relaycfg's own invariant), so it is reused here
	// rather than a second base64 decode.
	relayPin, err := relayCfg.Pin()
	if err != nil {
		return gatewayParams{}, err
	}
	profile := protocol.RemoteProfileV1{
		RelayTLSPolicy: relayCfg.TLSPolicy,
		RelaySPKIPin:   relayPin,
	}
	if u, uerr := url.Parse(relayCfg.RelayURL); uerr == nil {
		profile.RelayHost = u.Hostname()
	}

	reg, err := device.Open(filepath.Join(stateDir, "devices"))
	if err != nil {
		return gatewayParams{}, fmt.Errorf("open device registry: %w", err)
	}
	devices := reg.List()
	if len(devices) != 1 {
		return gatewayParams{}, fmt.Errorf("resolveGatewayParams: want exactly one paired device, got %d", len(devices))
	}
	rec := devices[0]

	// Load the paired device's sealed grant sidecar (persisted by the daemon at enroll,
	// co-located with the registry). Absent -> nil (a pre-grant pairing; delivery no-ops);
	// present-but-corrupt -> fail closed, like the registry itself.
	sealedGrant, err := grant.Load(filepath.Join(stateDir, "devices"), rec.DeviceID)
	if err != nil {
		return gatewayParams{}, fmt.Errorf("load device grant: %w", err)
	}

	remoteDir := filepath.Join(stateDir, "remote")
	journalSeq, err := remotegw.OpenSeqSource(filepath.Join(remoteDir, "outbound-journal.seq"))
	if err != nil {
		return gatewayParams{}, fmt.Errorf("open outbound journal seq: %w", err)
	}
	replySeq, err := remotegw.OpenSeqSource(filepath.Join(remoteDir, "outbound-reply.seq"))
	if err != nil {
		return gatewayParams{}, fmt.Errorf("open outbound reply seq: %w", err)
	}
	pushSeq, err := remotegw.OpenSeqSource(filepath.Join(remoteDir, "outbound-push.seq"))
	if err != nil {
		return gatewayParams{}, fmt.Errorf("open outbound push seq: %w", err)
	}
	// The push preference is opened, not read, here: LoadPrefs re-reads on every wake so a
	// setting the phone changes mid-run takes effect on the next transition. A record that
	// exists but cannot be parsed surfaces at that read as an error AND a suppression --
	// deliberately not as a boot failure, because a corrupt preference must not stop the
	// gateway bridging the journal.
	pushPrefs, err := remotegw.OpenPushPrefs(filepath.Join(remoteDir, "push-prefs.json"))
	if err != nil {
		return gatewayParams{}, fmt.Errorf("open push preference: %w", err)
	}
	// The outbound journal outbox sits beside its seq file: the seq says which numbers may
	// never be reissued, the outbox says which journal cursors were actually delivered and
	// which envelope is still in flight.
	outbox, err := remotegw.OpenOutbox(filepath.Join(remoteDir, "outbound-journal.outbox"))
	if err != nil {
		return gatewayParams{}, fmt.Errorf("open outbound journal outbox: %w", err)
	}
	// Bind the checkpoint to THIS identity: `swarm remote init` regenerates machine.key
	// (epoch id back to 1) without touching its siblings here, so an unbound file would
	// hand the fresh identity the previous one's epoch-1 high-water -- stale-dropping the
	// newly paired phone's first frames, take_control included -- and a cursor past the end
	// of a mailbox that restarted at 1. Both are silent and permanent. The routing id is
	// the right stamp: it is the coordinate the cursor indexes into, and it changes with
	// any identity regeneration.
	inbound, err := remotegw.OpenInboundState(
		filepath.Join(remoteDir, "inbound-state.json"),
		relay.RoutingID(id.RelayAuthPublic()),
	)
	if err != nil {
		return gatewayParams{}, fmt.Errorf("open inbound state: %w", err)
	}
	// ADR-015 P9/P12: an OPTIONAL migration off legacy_relay. Absent (every pairing until
	// it migrates) leaves PushGateway nil, which NewService reads as "wire the push path
	// exactly as it is today". See loadPushGatewayConfig's TODO(pairing-conveyance).
	pushGateway, err := resolvePushGatewayConfig(remoteDir)
	if err != nil {
		return gatewayParams{}, err
	}

	return gatewayParams{
		DaemonSocket:  daemonSocket,
		RelayURL:      relayCfg.RelayURL,
		RelaySecurity: relaySecurity,
		Profile:       profile,
		RelayAuth: relay.ClientAuth{
			RelayAuthPub: id.RelayAuthPublic(),
			// The MACHINE identity is a software key with no custody gate, so it never
			// refuses; relay.ClientAuth.Sign is failable for the PHONE (ADR-007 B18(a)).
			Sign: func(challenge []byte) ([]byte, error) { return id.RelayAuthSign(challenge), nil },
			// NO Peer, deliberately, for the reason cmd/swarm/remote.go withMachineRelay
			// states in full (ADR-007 B49): asking the relay whether the paired handset has
			// revoked this machine turns a stolen handset into a permanent kill switch over
			// the gateway, and no legitimate flow revokes a machine at the relay.
		},
		// C5 (finding, re-audit): the relay keys the phone's mailbox by
		// relay.RoutingID(its relay-auth pub) -- the SAME deriver the relay (client.go:
		// RoutingID(auth.RelayAuthPub)) and machineid use. Derive PhoneTarget the same way,
		// NOT from the phone's self-reported (unverifiable) rec.RoutingID: a phone that
		// supplied a non-canonical routing id then cannot make the gateway misroute the grant.
		PhoneTarget:        relay.RoutingID(ed25519.PublicKey(rec.RelayAuthPub)),
		Key:                id.EpochKeys().ContentKey,
		WakeKey:            id.EpochKeys().WakeKey,
		EpochID:            id.EpochID(),
		GrantSeq:           id.GrantSeq(),
		RecipientKeyID:     crypto.KeyID(rec.RecipientPub),
		SenderKeyID:        crypto.KeyID(id.RecipientPublic()),
		JournalSeq:         journalSeq,
		ReplySeq:           replySeq,
		PushSeq:            pushSeq,
		PushPrefs:          pushPrefs,
		Outbox:             outbox,
		Inbound:            inbound,
		DeviceRelayAuthPub: ed25519.PublicKey(rec.RelayAuthPub),
		DeviceConsentSig:   rec.ConsentSig,
		Grant:              sealedGrant,
		PushGateway:        pushGateway,
		StateDir:           stateDir,
		DeviceID:           rec.DeviceID,
	}, nil
}

// loadRelayConfig reads the machine's relay provisioning through the one parser that owns
// the file (relaycfg). Unlike internal/skeleton's reader -- which treats an absent file as
// "no relay configured" -- the gateway binary requires a relay to run, so a missing,
// unreadable, unparseable, or empty relay_url is a fail-closed error here.
func loadRelayConfig(stateDir string) (relaycfg.Config, error) {
	cfg, found, err := relaycfg.Load(stateDir)
	if err != nil {
		return relaycfg.Config{}, err
	}
	if !found {
		return relaycfg.Config{}, fmt.Errorf("read relay.json: %w", os.ErrNotExist)
	}
	if cfg.RelayURL == "" {
		return relaycfg.Config{}, fmt.Errorf("relay.json present but relay_url is empty")
	}
	return cfg, nil
}

// pushGatewayFile is <StateDir>/remote/push-gateway.json's shape: the SCAFFOLD this wave
// provides for ADR-015 P9/P12's gateway-url/submit-capability/push-address plumbing.
//
// TODO(pairing-conveyance): this file stands in for PG-MIG-2's real per-pairing
// conveyance -- Android gateway registration, address allocation, an authenticated
// pairing-update acknowledgement, and a successful gateway test wake, ending in the
// atomic push_transport transition (internal/remotegw/pushtransport.go's own
// TODO(pairing-conveyance)). Until that slice lands, nothing in this tree WRITES this
// file; an operator (or a future migration tool) provisions it by hand. Its presence
// alone does not flip push_transport to gateway -- OpenTransportStore still starts at
// legacy_relay (PG-MIG-1) until something calls SetTransport, which this wave does not
// do either. This is deliberately just plumbing, not a migration trigger.
type pushGatewayFile struct {
	GatewayURL       string `json:"gateway_url"`
	SubmitCapability string `json:"submit_capability"`
	// MachineRevokeCapability is the pairing's machine-revoke capability (spec 2.2/3.4,
	// distinct from submit -- PG-AUTH-9), presented as "Swarm-Revoke <cap>" by the R4
	// revoke producer (bead agents-tracker-u37c). OPTIONAL: every push-gateway.json
	// provisioned before the producer existed carries only the first two capabilities,
	// and refusing such a file would take down the working wake path to add a revoke
	// path. Empty means the producer cannot run and discloses that instead.
	MachineRevokeCapability string `json:"machine_revoke_capability,omitempty"`
	// PushAddress is PG-ALLOC-1's 16 opaque bytes, hex-encoded (32 hex characters).
	PushAddress string `json:"push_address"`
}

// validateGatewayURL fails closed on a gateway_url that would otherwise reach
// HTTPWakeSubmitter.SubmitWake only to be refused THERE as a plain (therefore
// unconditionally retryable, see wakesubmitter.go's header) error -- turning a bad
// config into a push path that silently retries forever without ever delivering,
// instead of a startup refusal (PG-TR-1's https-only check is otherwise enforced only
// per request, never at load). It also rejects a path, query or fragment: SubmitWake
// builds the request URL as TrimRight(BaseURL, "/") + "/v1/wakes", so an operator who
// already included the spec's /v1 prefix in gateway_url would silently get
// /v1/v1/wakes with no error anywhere.
func validateGatewayURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("gateway_url is not a valid URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("gateway_url must use https (PG-TR-1), got %q", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("gateway_url has no host: %q", raw)
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("gateway_url must be a bare origin with no path, query or fragment "+
			"-- SubmitWake appends /v1/wakes itself -- got %q", raw)
	}
	return nil
}

// parsePushGatewayFile reads and validates <remoteDir>/push-gateway.json WITHOUT
// opening any durable store, so the revoke redrive can consult it in a quiescent state
// dir. present=false with a nil error is the missing-file case: every pairing's state
// until it migrates.
func parsePushGatewayFile(remoteDir string) (pushGatewayFile, remotegw.PushAddress, bool, error) {
	var f pushGatewayFile
	var addr remotegw.PushAddress
	data, err := os.ReadFile(filepath.Join(remoteDir, "push-gateway.json"))
	if errors.Is(err, os.ErrNotExist) {
		return f, addr, false, nil
	}
	if err != nil {
		return f, addr, false, fmt.Errorf("read push-gateway.json: %w", err)
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, addr, false, fmt.Errorf("parse push-gateway.json: %w", err)
	}
	if f.GatewayURL == "" || f.SubmitCapability == "" || f.PushAddress == "" {
		return f, addr, false, fmt.Errorf("push-gateway.json present but missing a required field " +
			"(gateway_url, submit_capability, push_address)")
	}
	if err := validateGatewayURL(f.GatewayURL); err != nil {
		return f, addr, false, fmt.Errorf("push-gateway.json: %w", err)
	}
	raw, err := hex.DecodeString(f.PushAddress)
	if err != nil || len(raw) != len(addr) {
		return f, addr, false, fmt.Errorf("push-gateway.json: push_address must be %d hex-encoded bytes", len(addr))
	}
	copy(addr[:], raw)
	return f, addr, true, nil
}

// resolvePushGatewayConfig reads the optional push-gateway.json and, only if present,
// opens the three durable stores the wake-obligation machine needs (a dedicated wake_seq
// file, distinct from PushSeq -- see remotegw.PushGatewayConfig.WakeSeq's doc comment for
// why sharing one would be wrong). A missing file returns (nil, nil): this is NOT an
// error, it is every pairing's state until it migrates.
func resolvePushGatewayConfig(remoteDir string) (*remotegw.PushGatewayConfig, error) {
	f, addr, present, err := parsePushGatewayFile(remoteDir)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}

	wakeSeq, err := remotegw.OpenSeqSource(filepath.Join(remoteDir, "outbound-wake.seq"))
	if err != nil {
		return nil, fmt.Errorf("open outbound wake seq: %w", err)
	}
	obligations, err := remotegw.OpenObligationStore(filepath.Join(remoteDir, "wake-obligations.json"))
	if err != nil {
		return nil, fmt.Errorf("open wake-obligation store: %w", err)
	}
	transport, err := remotegw.OpenTransportStore(filepath.Join(remoteDir, "push-transport.json"))
	if err != nil {
		return nil, fmt.Errorf("open push-transport store: %w", err)
	}
	return &remotegw.PushGatewayConfig{
		GatewayURL:              f.GatewayURL,
		SubmitCapability:        f.SubmitCapability,
		MachineRevokeCapability: f.MachineRevokeCapability,
		Address:                 addr,
		Transport:               transport,
		Obligations:             obligations,
		WakeSeq:                 wakeSeq,
	}, nil
}
