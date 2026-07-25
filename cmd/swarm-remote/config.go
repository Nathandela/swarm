// Package main's config assembler for the gateway binary (slice G1):
// resolveGatewayParams reads the provisioned state (machine identity,
// relay.json, the paired-device registry) and returns everything
// remotegw.Service needs except the dialed relay Mailbox (that dial happens
// in slice G2). It fails closed on any missing or ambiguous provisioning
// state rather than returning a partially-populated gatewayParams.
package main

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/machineid"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// gatewayParams is everything remotegw.Service needs to run, minus the
// dialed relay Mailbox (assembled by G2).
type gatewayParams struct {
	DaemonSocket   string
	RelayURL       string
	RelayAuth      relay.ClientAuth
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
	Grant              *crypto.EpochGrant
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

	relayURL, err := loadRelayURL(stateDir)
	if err != nil {
		return gatewayParams{}, err
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

	return gatewayParams{
		DaemonSocket: daemonSocket,
		RelayURL:     relayURL,
		RelayAuth: relay.ClientAuth{
			RelayAuthPub: id.RelayAuthPublic(),
			Sign:         id.RelayAuthSign,
		},
		// C5 (finding, re-audit): the relay keys the phone's mailbox by
		// relay.RoutingID(its relay-auth pub) -- the SAME deriver the relay (client.go:
		// RoutingID(auth.RelayAuthPub)) and machineid use. Derive PhoneTarget the same way,
		// NOT from the phone's self-reported (unverifiable) rec.RoutingID: a phone that
		// supplied a non-canonical routing id then cannot make the gateway misroute the grant.
		PhoneTarget:        relay.RoutingID(ed25519.PublicKey(rec.RelayAuthPub)),
		Key:                id.EpochKeys().ContentKey,
		EpochID:            id.EpochID(),
		GrantSeq:           id.GrantSeq(),
		RecipientKeyID:     crypto.KeyID(rec.RecipientPub),
		SenderKeyID:        crypto.KeyID(id.RecipientPublic()),
		JournalSeq:         journalSeq,
		ReplySeq:           replySeq,
		Outbox:             outbox,
		Inbound:            inbound,
		DeviceRelayAuthPub: ed25519.PublicKey(rec.RelayAuthPub),
		Grant:              sealedGrant,
		StateDir:           stateDir,
		DeviceID:           rec.DeviceID,
	}, nil
}

// loadRelayURL reads <stateDir>/remote/relay.json ({"relay_url":"..."}),
// matching the shape internal/skeleton/pairing_config.go's loadRelayURL
// reads. Unlike that helper (which treats an absent file as "no relay
// configured"), the gateway binary requires a relay to run: a missing,
// unreadable, unparseable, or empty relay_url is a fail-closed error here.
func loadRelayURL(stateDir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(stateDir, "remote", "relay.json"))
	if err != nil {
		return "", fmt.Errorf("read relay.json: %w", err)
	}
	var rc struct {
		RelayURL string `json:"relay_url"`
	}
	if err := json.Unmarshal(b, &rc); err != nil {
		return "", fmt.Errorf("parse relay.json: %w", err)
	}
	if rc.RelayURL == "" {
		return "", fmt.Errorf("relay.json present but relay_url is empty")
	}
	return rc.RelayURL, nil
}
