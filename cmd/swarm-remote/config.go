// Package main's config assembler for the gateway binary (slice G1):
// resolveGatewayParams reads the provisioned state (machine identity,
// relay.json, the paired-device registry) and returns everything
// remotegw.Service needs except the dialed relay Mailbox (that dial happens
// in slice G2). It fails closed on any missing or ambiguous provisioning
// state rather than returning a partially-populated gatewayParams.
package main

import (
	"crypto/ed25519"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/machineid"
	"github.com/Nathandela/swarm/internal/remote/relaycfg"
	"github.com/Nathandela/swarm/internal/remote/relayv2"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// gatewayParams is everything remotegw.Service needs to run, minus the
// dialed relay Mailbox (assembled by G2).
type gatewayParams struct {
	DaemonSocket string
	RelayAuth    relayv2.Auth
	// RelayV2Profile contains the relay-v2 route and transport policy the sidecar dials
	// under (PB-NET-2), resolved
	// from the SAME relay.json the URL came from: verified TLS, cleartext refused except
	// to a loopback IP literal, and the operator's SPKI pin when one is configured
	// (ADR-007 B34). It is a resolved VALUE rather than a flag the dial re-derives, so
	// the policy cannot differ between assembly and dial.
	RelayV2Profile relayv2.Profile
	// Profile is ADR-016 "profile"'s first real publisher: the machine's relay TLS policy,
	// host and pin, built from the SAME relaycfg.Config the dial policy above reads, and
	// carried into remotegw.ServiceConfig.Profile so every reconcile record publishes it.
	Profile        protocol.RemoteProfileV1
	PhoneTarget    string
	Key            crypto.ContentKey
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
	// native AUTHORIZE target that opens the machine->device mailbox route; Grant is the
	// persisted sealed EpochGrant the gateway appends to that mailbox as the phone's
	// bootstrap. Grant is nil when no sidecar was persisted (a pre-grant pairing), which
	// deliverEpochGrant treats as a no-op.
	DeviceRelayAuthPub ed25519.PublicKey
	// DeviceConsentSig is the paired device's relay-route consent for this machine
	// (ADR-007 B27/B38), carried from its registry record. Without it the relay refuses
	// AUTHORIZE and the grant append behind it, so it is resolved here
	// with the key it accompanies rather than looked up later.
	DeviceConsentSig []byte
	Grant            *crypto.EpochGrant

	// PushGateway configures the ADR-015 P9/P12 wake-obligation machine. A negotiated
	// pairing derives it wholly from the sole registry record; nil means a foreground/
	// legacy record with no compatibility sidecar.
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
	// THE PRODUCTION PROFILE, POPULATED (ADR-017 T5-a, round-2 blocker 3). This literal is
	// the ONLY construction of RemoteProfileV1 in the shipped tree, and it used to set the
	// three ADR-016 relay fields and leave the rest zero. Since RouteSession fails closed on
	// TrustsCapabilityRecord() FIRST, a zero capability_record_version routed EVERY session
	// -- healthy Claude included -- to the status card and, because ComposerAvailable is the
	// same predicate, took R6/R7's chat composer away from all of them. The wiring, not the
	// versions, was the defect: these are the constants the machine has been implementing
	// since this wave's schema landed.
	//
	// The three bounds are declared explicitly rather than left zero, even though zero
	// clamps to the phone's conservative built-in: a machine that declares its ceiling is
	// checkable against what it actually sends, and a machine that declares nothing is not.
	profile := protocol.RemoteProfileV1{
		Version:                  schema.CurrentProfileVersion,
		InteractionSchemaVersion: daemon.InteractionSchemaVersion,
		TerminalViewVersion:      schema.CurrentTerminalViewVersion,
		CapabilityRecordVersion:  schema.CurrentCapabilityRecordVersion,
		RelayTLSPolicy:           relayCfg.TLSPolicy,
		RelaySPKIPin:             relayPin,
		TerminalViewMaxLineBytes: schema.DeclaredTerminalViewMaxLineBytes,
		TerminalViewMaxRows:      schema.DeclaredTerminalViewMaxRows,
		TerminalViewMaxRateHz:    schema.DeclaredTerminalViewMaxRateHz,
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
		relayv2.RoutingID(id.RelayAuthPublic()),
	)
	if err != nil {
		return gatewayParams{}, fmt.Errorf("open inbound state: %w", err)
	}
	// ADR-015 P9/P12: only the registry's validated atomic Push binding may supply gateway
	// authority. A record without one is foreground-only; obsolete sidecars are ignored.
	var pushGateway *remotegw.PushGatewayConfig
	if rec.Push != nil {
		pushGateway, err = resolveRegistryPushGatewayConfig(remoteDir, *rec.Push)
		if err != nil {
			return gatewayParams{}, err
		}
	}

	return gatewayParams{
		DaemonSocket: daemonSocket,
		RelayV2Profile: relayv2.Profile{
			RelayURL: relayCfg.RelayURL, MachineRID: relayv2.RoutingID(id.RelayAuthPublic()),
			OperatorNamespace: relayCfg.OperatorNamespace, Security: relaySecurity,
		},
		Profile: profile,
		RelayAuth: relayv2.Auth{
			PublicKey: id.RelayAuthPublic(),
			// The MACHINE identity is a software key with no custody gate, so it never
			// refuses; the native Sign seam is failable for the PHONE (ADR-007 B18(a)).
			Sign: func(challenge []byte) ([]byte, error) { return id.RelayAuthSign(challenge), nil },
			// NO Peer, deliberately, for the reason cmd/swarm/remote.go withMachineRelay
			// states in full (ADR-007 B49): asking the relay whether the paired handset has
			// revoked this machine turns a stolen handset into a permanent kill switch over
			// the gateway, and no legitimate flow revokes a machine at the relay.
		},
		// C5 (finding, re-audit): the relay keys the phone's mailbox by
		// relayv2.RoutingID(its relay-auth pub) -- the SAME deriver the relay and machineid
		// use. Derive PhoneTarget the same way,
		// NOT from the phone's self-reported (unverifiable) rec.RoutingID: a phone that
		// supplied a non-canonical routing id then cannot make the gateway misroute the grant.
		PhoneTarget:        relayv2.RoutingID(ed25519.PublicKey(rec.RelayAuthPub)),
		Key:                id.EpochKeys().ContentKey,
		EpochID:            id.EpochID(),
		GrantSeq:           id.GrantSeq(),
		RecipientKeyID:     crypto.KeyID(rec.RecipientPub),
		SenderKeyID:        crypto.KeyID(id.RecipientPublic()),
		JournalSeq:         journalSeq,
		ReplySeq:           replySeq,
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

// resolveRegistryPushGatewayConfig derives every authority-bearing runtime coordinate from
// the validated sole registry row.
func resolveRegistryPushGatewayConfig(remoteDir string, push device.PushBinding) (*remotegw.PushGatewayConfig, error) {
	if err := device.ValidatePushBinding(push); err != nil {
		return nil, fmt.Errorf("registry push binding: %w", err)
	}
	var addr remotegw.PushAddress
	copy(addr[:], push.Address)
	var wake crypto.WakeKey
	copy(wake[:], push.WakeKey)
	wakeSeq, err := remotegw.OpenSeqSource(remotegw.WakeSeqPath(remoteDir, addr))
	if err != nil {
		return nil, fmt.Errorf("open registry push wake seq: %w", err)
	}
	obligations, err := remotegw.OpenObligationStore(filepath.Join(remoteDir, "wake-obligations.json"))
	if err != nil {
		return nil, fmt.Errorf("open wake-obligation store: %w", err)
	}
	return &remotegw.PushGatewayConfig{
		GatewayURL: push.GatewayURL, SubmitCapability: push.SubmitCapability,
		WakeKey: wake,
		Address: addr, Obligations: obligations, WakeSeq: wakeSeq,
	}, nil
}
