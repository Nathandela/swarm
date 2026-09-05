package swarmmobile

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	remotecrypto "github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/relayv2"
	"github.com/Nathandela/swarm/internal/remotegw"
)

func TestRelayV2PairingErrorsMapToNativeStates(t *testing.T) {
	for code, want := range map[string]string{
		"pairing_not_found":      pairExpired,
		"pairing_rate_limited":   pairRateLimited,
		"pairing_full":           pairRateLimited,
		"pairing_directory_full": pairRateLimited,
		"not_authorized":         "",
	} {
		err := fmt.Errorf("pairing transport: %w", &relayv2.ProtocolError{Code: code})
		if got := relayV2PairState(err); got != want {
			t.Errorf("relayV2PairState(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestPairingUsesNativeRelayV2Transport(t *testing.T) {
	baseURL := os.Getenv("MOBILE_RELAY_V2_HTTP")
	if baseURL == "" {
		t.Skip("MOBILE_RELAY_V2_HTTP is set by the fresh-workerd pairing gate")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	machineSeed := make([]byte, ed25519.SeedSize)
	for i := range machineSeed {
		machineSeed[i] = byte(i)
	}
	machinePriv := ed25519.NewKeyFromSeed(machineSeed)
	machinePub := machinePriv.Public().(ed25519.PublicKey)
	machineRID := relayv2.RoutingID(machinePub)
	if machineRID != "88564c8ede170d2ed321e21e61354184" {
		t.Fatalf("deterministic machine RID = %s", machineRID)
	}
	profile := relayv2.Profile{RelayURL: baseURL, MachineRID: machineRID,
		OperatorNamespace: "local-test", Security: relay.Security{AllowLoopbackCleartext: true}}
	control, err := relayv2.Dial(ctx, profile, relayv2.Auth{
		PublicKey: machinePub, Role: relayv2.RoleMachine, Purpose: relayv2.PurposeControl,
		Sign: func(message []byte) ([]byte, error) { return ed25519.Sign(machinePriv, message), nil },
	})
	if err != nil {
		t.Fatalf("Dial machine control: %v", err)
	}
	defer control.Close()

	machineIdentity, err := remotecrypto.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	machineSignPub, machineSignPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var ceremonyID [16]byte
	var secret [32]byte
	if _, err := rand.Read(ceremonyID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatal(err)
	}
	machineSAS := make(chan [6]string, 1)
	approveMachine := make(chan struct{})
	transport := relayv2.NewMachinePairTransport(control)
	type machineResult struct {
		out *pairing.MachineOutcome
		err error
	}
	machineDone := make(chan machineResult, 1)
	go func() {
		out, err := pairing.NewMachine(pairing.MachineParams{
			Static: machineIdentity.NoiseStatic(), Secret: secret, RendezvousID: ceremonyID,
			LocalConsole: true,
			Confirm: func(ctx context.Context, sas [6]string, _ string) (bool, error) {
				machineSAS <- sas
				select {
				case <-approveMachine:
					return true, nil
				case <-ctx.Done():
					return false, ctx.Err()
				}
			},
			Payload: pairing.MachinePayload{
				Hostname: "workerd-mobile.test", MachineRoutingID: mustDecodeRID(t, machineRID),
				MachineRelayAuthPub: machinePub, RecipientPub: machineIdentity.RecipientPublic(),
				MachineSignPub: machineSignPub, MachineEndpointID: "workerd-mobile",
				RelayTLSPolicy: "webpki", OperatorNamespace: "local-test", EpochID: 1,
			},
		}).Pair(ctx, transport)
		machineDone <- machineResult{out: out, err: err}
	}()
	select {
	case <-transport.Created():
	case <-ctx.Done():
		t.Fatal("machine did not create relay-v2 ceremony")
	}
	qr, err := pairing.EncodeQR(pairing.QRPayload{RelayURL: baseURL, RendezvousID: ceremonyID, PairingSecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	app, err := NewApp(&Config{StateDir: stateDir, RelayURL: baseURL}, r4r3Custody{})
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	defer func() { _ = app.Close() }()
	p, err := app.BeginPairing(qr)
	if err != nil {
		t.Fatalf("BeginPairing: %v", err)
	}
	origin, err := p.Origin()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ConfirmOrigin(origin); err != nil {
		t.Fatalf("ConfirmOrigin: %v", err)
	}
	var phoneSAS string
	for phoneSAS == "" {
		phoneSAS, err = p.SAS()
		if err == nil && phoneSAS != "" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("phone did not derive SAS: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	var machineWords [6]string
	select {
	case machineWords = <-machineSAS:
	case <-ctx.Done():
		t.Fatal("machine did not derive SAS")
	}
	if want := strings.Join(machineWords[:], " "); phoneSAS != want {
		t.Fatalf("cross-end SAS mismatch: phone=%q machine=%q", phoneSAS, want)
	}
	if err := p.Confirm(); err != nil {
		t.Fatalf("Confirm phone SAS: %v", err)
	}
	close(approveMachine)
	var paired machineResult
	select {
	case paired = <-machineDone:
	case <-ctx.Done():
		t.Fatal("machine did not receive the pairing ACK")
	}
	if paired.err != nil {
		t.Fatalf("machine pairing: %v", paired.err)
	}
	if paired.out == nil {
		t.Fatal("machine pairing returned no authenticated device")
	}
	binding, err := control.Authorize(ctx, ed25519.PublicKey(paired.out.Device.DeviceRelayAuthPub), paired.out.Device.ConsentSig)
	if err != nil {
		t.Fatalf("relay-v2 rejected mobile consent: %v", err)
	}
	for {
		state, err := p.State()
		if err == nil && state == pairPaired {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("phone did not publish paired state: state=%q err=%v", state, err)
		case <-time.After(20 * time.Millisecond):
		}
	}

	machineStream, err := relayv2.Dial(ctx, profile, relayv2.Auth{
		PublicKey: machinePub, Role: relayv2.RoleMachine, Purpose: relayv2.PurposeStream,
		Sign: func(message []byte) ([]byte, error) { return ed25519.Sign(machinePriv, message), nil },
	})
	if err != nil {
		t.Fatalf("Dial machine stream: %v", err)
	}
	defer machineStream.Close()
	machineSub, err := machineStream.Subscribe(ctx, binding, relayv2.Checkpoint{})
	if err != nil {
		t.Fatalf("Subscribe machine inbox: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start native phone stream: %v", err)
	}
	waitMobileV2(t, ctx, "phone stream online", func() bool {
		state, stateErr := app.ConnectionState()
		return stateErr == nil && state == connOnline
	})
	liveBeforeProbe, err := app.conn()
	if err != nil {
		t.Fatal(err)
	}
	if err := app.probeWebPKI(ctx, "127.0.0.1"); err != nil {
		t.Fatalf("native authenticated WebPKI probe: %v", err)
	}
	liveAfterProbe, err := app.conn()
	if err != nil || liveAfterProbe != liveBeforeProbe {
		t.Fatalf("WebPKI probe superseded live phone stream: before=%p after=%p err=%v", liveBeforeProbe, liveAfterProbe, err)
	}
	phoneKeys := app.core.KeyStore()
	probeConn, err := relayv2.Dial(ctx, profile, relayv2.Auth{
		PublicKey: ed25519.PublicKey(phoneKeys.RelayAuthPublic()), Role: relayv2.RolePhone, Purpose: relayv2.PurposeProbe,
		Sign: phoneKeys.SignRelayAuth,
	})
	if err != nil {
		t.Fatalf("Dial native phone probe: %v", err)
	}
	err = probeConn.Revoke(ctx, binding)
	var protocolErr *relayv2.ProtocolError
	if !errors.As(err, &protocolErr) || protocolErr.Code != "not_authorized" {
		t.Fatalf("probe REVOKE = %v, want not_authorized", err)
	}
	probeConn.Close()
	liveAfterDeniedRPC, err := app.conn()
	if err != nil || liveAfterDeniedRPC != liveBeforeProbe {
		t.Fatalf("probe RPC affected live phone stream: before=%p after=%p err=%v", liveBeforeProbe, liveAfterDeniedRPC, err)
	}
	phoneBinding, ok := app.core.PhoneBinding()
	if !ok || !phoneBinding.Active || phoneBinding.Generation != binding.Generation {
		t.Fatalf("durable phone binding = (%+v,%v), want active generation %d", phoneBinding, ok, binding.Generation)
	}

	keys, err := remotecrypto.NewEpochKeys()
	if err != nil {
		t.Fatal(err)
	}
	sealedGrant, err := remotecrypto.SealEpochGrant(machineSignPriv, paired.out.Device.RecipientPub, 1, 1, keys)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := grant.MarshalBootstrap(sealedGrant)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := machineStream.Append(ctx, binding, "mobile-grant", bootstrap); err != nil {
		t.Fatalf("machine -> phone append: %v", err)
	}
	waitMobileV2(t, ctx, "durable machine -> phone receive/ACK", func() bool {
		st := app.core.State()
		return st.RelayCursor > 0 && st.Keys.ContentKey == keys.ContentKey
	})

	// Authenticated but malformed plaintext is discardable, not a page barrier. Its
	// cumulative ACK must not stop the following valid frame from committing.
	malformedEnvelope, err := remotecrypto.SealMailbox(keys.ContentKey, remotecrypto.EnvelopeHeader{
		Version: remotecrypto.VersionV1, EpochID: 1, Seq: 1, IssuedAt: time.Now().UnixMilli(),
	}, []byte("{"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := machineStream.Append(ctx, binding, "malformed-authenticated", malformedEnvelope.Marshal()); err != nil {
		t.Fatalf("append authenticated malformed plaintext: %v", err)
	}
	validAfterMalformed, err := remotegw.SealControlReply(keys.ContentKey, 1, 2,
		schema.Control{Op: protocol.OpOK, OperationID: "valid-after-malformed"})
	if err != nil {
		t.Fatal(err)
	}
	validAppend, err := machineStream.Append(ctx, binding, "valid-after-malformed", validAfterMalformed)
	if err != nil {
		t.Fatalf("append valid tail after malformed plaintext: %v", err)
	}
	waitMobileV2(t, ctx, "valid tail after malformed plaintext", func() bool {
		return app.core.State().RelayCursor >= validAppend.Cursor
	})

	phone, err := app.conn()
	if err != nil {
		t.Fatalf("native phone stream: %v", err)
	}
	if _, err := phone.MailboxAppend(ctx, machineRID, []byte("phone-to-machine")); err != nil {
		t.Fatalf("phone -> machine append: %v", err)
	}
	fromPhone, err := machineSub.Recv(ctx)
	if err != nil || string(fromPhone.Ciphertext) != "phone-to-machine" {
		t.Fatalf("machine receive = (%+v,%v)", fromPhone, err)
	}
	if err := machineSub.Ack(ctx, fromPhone.Cursor); err != nil {
		t.Fatalf("machine ACK: %v", err)
	}
	commandRecv := remotecrypto.NewMailboxReceiver()
	healthyStream, err := app.conn()
	if err != nil {
		t.Fatal(err)
	}
	healthyIncarnation := app.core.State().RelayIncarnation
	if err := app.RefreshRoster(); err != nil {
		t.Fatalf("healthy RefreshRoster: %v", err)
	}
	stillHealthy, err := app.conn()
	if err != nil || stillHealthy != healthyStream || app.core.State().RelayIncarnation != healthyIncarnation {
		t.Fatalf("healthy PROBE replaced stream/incarnation: before=%p/%q after=%p/%q err=%v",
			healthyStream, healthyIncarnation, stillHealthy, app.core.State().RelayIncarnation, err)
	}
	healthyRefresh, err := machineSub.Recv(ctx)
	if err != nil {
		t.Fatalf("receive healthy roster command: %v", err)
	}
	openedRefresh, err := remotegw.OpenMailboxFrame(commandRecv, keys.ContentKey, healthyRefresh.Ciphertext)
	if err != nil || openedRefresh.Command.Action != "journal_resync" {
		t.Fatalf("open healthy roster command = (%+v,%v)", openedRefresh, err)
	}
	if err := machineSub.Ack(ctx, healthyRefresh.Cursor); err != nil {
		t.Fatalf("ACK healthy roster command: %v", err)
	}
	// The remainder exercises a distinct destructive refresh transaction without
	// sleeping through the user-facing per-stream rate budget.
	app.mu.Lock()
	app.resyncAt[phonecore.StreamJournal] = nil
	app.mu.Unlock()

	beforeReconnect := app.core.State()
	if err := app.Stop(); err != nil {
		t.Fatalf("Stop before reconnect: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start reconnect: %v", err)
	}
	waitMobileV2(t, ctx, "exact checkpoint reconnect", func() bool {
		state, _ := app.ConnectionState()
		st := app.core.State()
		return state == connOnline && st.RelayCursor == beforeReconnect.RelayCursor &&
			st.RelayIncarnation == beforeReconnect.RelayIncarnation
	})

	// Native Resync retires and joins the exact subscription before resetting its
	// binding-aware checkpoint. The blank subscription adopts the server's effective
	// durable ACK floor, so an immediate exact reconnect works even with no new delivery.
	beforeResync := app.core.State()
	if err := app.Resync(phonecore.StreamJournal); err != nil {
		t.Fatalf("native Resync: %v", err)
	}
	afterResync := app.core.State()
	if beforeResync.RelayCursor == 0 || afterResync.RelayCursor != beforeResync.RelayCursor || afterResync.RelayIncarnation != beforeResync.RelayIncarnation {
		t.Fatalf("native resync checkpoint = (%d,%q), before (%d,%q)", afterResync.RelayCursor, afterResync.RelayIncarnation, beforeResync.RelayCursor, beforeResync.RelayIncarnation)
	}
	if err := app.Stop(); err != nil {
		t.Fatalf("stop immediately after blank recovery: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("restart immediately after blank recovery: %v", err)
	}
	waitMobileV2(t, ctx, "exact reconnect from effective blank baseline", func() bool {
		state, _ := app.ConnectionState()
		return state == connOnline
	})
	resyncDelivery, err := machineSub.Recv(ctx)
	if err != nil {
		t.Fatalf("receive facade resync command: %v", err)
	}
	if strings.Contains(string(resyncDelivery.Ciphertext), "journal_resync") {
		t.Fatal("facade command action appeared in relay ciphertext")
	}
	opened, err := remotegw.OpenMailboxFrame(commandRecv, keys.ContentKey, resyncDelivery.Ciphertext)
	if err != nil || opened.Command.Action != "journal_resync" {
		t.Fatalf("open facade resync command = (%+v,%v)", opened, err)
	}
	if err := machineSub.Ack(ctx, resyncDelivery.Cursor); err != nil {
		t.Fatalf("ACK facade resync command: %v", err)
	}
	app.mu.Lock()
	app.resyncAt[phonecore.StreamJournal] = nil
	app.mu.Unlock()

	// Lose a successful DISCARD response locally. With no pending recovery intent the
	// reconnect must fence the rejected incarnation, reset durably, and blank-subscribe.
	oldIncarnation := app.core.State().RelayIncarnation
	staleEnvelope, err := remotecrypto.SealMailbox(keys.ContentKey, remotecrypto.EnvelopeHeader{
		Version: remotecrypto.VersionV1, EpochID: 1, Seq: 3,
		IssuedAt: time.Now().Add(-phonecore.InboundMaxAge - time.Minute).UnixMilli(),
	}, []byte(`{"kind":"command_reply","op":"ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	staleAppend, err := machineStream.Append(ctx, binding, "stale-authenticated-head", staleEnvelope.Marshal())
	if err != nil {
		t.Fatalf("append stale authenticated head: %v", err)
	}
	freshTail, err := remotegw.SealControlReply(keys.ContentKey, 1, 4,
		schema.Control{Op: protocol.OpOK, OperationID: "fresh-tail-after-discard"})
	if err != nil {
		t.Fatal(err)
	}
	freshAppend, err := machineStream.Append(ctx, binding, "fresh-tail-after-stale", freshTail)
	if err != nil {
		t.Fatalf("append fresh tail: %v", err)
	}
	waitMobileV2(t, ctx, "stale head retained", app.core.Router().InboundAgeRefused)
	phone, err = app.dialPhoneStream(ctx)
	if err != nil {
		t.Fatalf("redial at external discard seam: %v", err)
	}
	if _, err := phone.sub.Probe(ctx); err != nil {
		t.Fatalf("probe external discard head: %v", err)
	}
	discardedExternally, err := phone.conn.Discard(ctx, phone.binding, oldIncarnation, staleAppend.Cursor)
	if err != nil {
		t.Fatalf("external incarnation rotation: %v", err)
	}
	if err := app.Stop(); err != nil {
		t.Fatalf("stop after external rotation: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("restart after external rotation: %v", err)
	}
	waitMobileV2(t, ctx, "incarnation mismatch blank recovery", func() bool {
		st := app.core.State()
		state, _ := app.ConnectionState()
		return state == connOnline && st.RelayIncarnation == discardedExternally.Incarnation &&
			st.RelayCursor >= freshAppend.Cursor
	})

	// Repeat with durable discard intent. The reconnect must retry exact old-incarnation
	// DISCARD, adopt its returned checkpoint, then redial because successful Discard is terminal.
	secondStale, err := remotecrypto.SealMailbox(keys.ContentKey, remotecrypto.EnvelopeHeader{
		Version: remotecrypto.VersionV1, EpochID: 1, Seq: 5,
		IssuedAt: time.Now().Add(-phonecore.InboundMaxAge - time.Minute).UnixMilli(),
	}, []byte(`{"kind":"command_reply","op":"ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	secondStaleAppend, err := machineStream.Append(ctx, binding, "second-stale-head", secondStale.Marshal())
	if err != nil {
		t.Fatalf("append second stale head: %v", err)
	}
	secondFresh, err := remotegw.SealControlReply(keys.ContentKey, 1, 6,
		schema.Control{Op: protocol.OpOK, OperationID: "fresh-after-crash-retry"})
	if err != nil {
		t.Fatal(err)
	}
	secondFreshAppend, err := machineStream.Append(ctx, binding, "fresh-after-crash-retry", secondFresh)
	if err != nil {
		t.Fatalf("append fresh crash-retry tail: %v", err)
	}
	waitMobileV2(t, ctx, "second stale head retained", app.core.Router().InboundAgeRefused)
	if _, err := app.core.BeginRelayDiscardRecovery(secondStaleAppend.Cursor); err != nil {
		t.Fatalf("persist discard intent: %v", err)
	}
	phone, err = app.dialPhoneStream(ctx)
	if err != nil {
		t.Fatalf("redial at crash discard seam: %v", err)
	}
	if _, err := phone.sub.Probe(ctx); err != nil {
		t.Fatalf("probe crash discard head: %v", err)
	}
	oldIncarnation = app.core.State().RelayIncarnation
	discarded, err := phone.conn.Discard(ctx, phone.binding, oldIncarnation, app.core.State().DiscardRecoveryCursor)
	if err != nil {
		t.Fatalf("discard at crash seam: %v", err)
	}
	if err := app.Stop(); err != nil {
		t.Fatalf("stop at discard crash seam: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("restart at discard crash seam: %v", err)
	}
	waitMobileV2(t, ctx, "idempotent discard retry/adoption", func() bool {
		st := app.core.State()
		state, _ := app.ConnectionState()
		return state == connOnline && st.RelayIncarnation == discarded.Incarnation && st.RelayCursor >= secondFreshAppend.Cursor
	})
	// A crash after local adoption but before the echoed roster leaves the token pending.
	// Retrying RefreshRoster must publish that token without DISCARDing the replacement
	// mailbox a second time.
	adoptedIncarnation := app.core.State().RelayIncarnation
	if err := app.RefreshRoster(); err != nil {
		t.Fatalf("refresh after adopted discard: %v", err)
	}
	if got := app.core.State().RelayIncarnation; got != adoptedIncarnation {
		t.Fatalf("refresh discarded replacement mailbox: incarnation %q -> %q", adoptedIncarnation, got)
	}
	if app.core.DiscardRecoveryToken() == "" {
		t.Fatal("refresh cleared recovery before an authenticated matching roster echo")
	}
	refreshDelivery, err := machineSub.Recv(ctx)
	if err != nil {
		t.Fatalf("receive post-discard roster command: %v", err)
	}
	if _, err := remotegw.OpenMailboxFrame(commandRecv, keys.ContentKey, refreshDelivery.Ciphertext); err != nil {
		t.Fatalf("open post-discard roster command: %v", err)
	}

	if err := control.Revoke(ctx, binding); err != nil {
		t.Fatalf("revoke generation: %v", err)
	}
	staleMachine, err := relayv2.Dial(ctx, profile, relayv2.Auth{
		PublicKey: machinePub, Role: relayv2.RoleMachine, Purpose: relayv2.PurposeStream,
		Sign: func(message []byte) ([]byte, error) { return ed25519.Sign(machinePriv, message), nil },
	})
	if err != nil {
		t.Fatalf("redial machine for stale-generation check: %v", err)
	}
	defer staleMachine.Close()
	if _, err := staleMachine.Append(ctx, binding, "old-generation", []byte("refused")); err == nil {
		t.Fatal("old-generation append succeeded after revocation")
	} else {
		var protocolErr *relayv2.ProtocolError
		if !errors.As(err, &protocolErr) || protocolErr.Code != "stale_generation" {
			t.Fatalf("old-generation append = %v, want stale_generation", err)
		}
	}
	if _, active := app.core.PhoneBinding(); !active {
		t.Fatal("core lost its generation floor after relay revocation")
	}
	waitMobileV2(t, ctx, "phone revocation terminal state", func() bool {
		state, _ := app.ConnectionState()
		summary, summaryErr := app.StateSummary()
		running, runningErr := app.IsRunning()
		return state == connRevoked && summaryErr == nil && !summary.Paired &&
			runningErr == nil && !running && app.core.State().Disowned
	})

	// Revocation is a durable ownership transition, not merely the terminal socket state.
	// A process restart stays offline and unpaired. A later authenticated same-home pairing
	// commit is the one operation allowed to clear Disowned, and that recovery survives a
	// second restart as paired without silently starting transport.
	if err := app.Close(); err != nil {
		t.Fatalf("Close revoked app: %v", err)
	}
	app, err = NewApp(&Config{StateDir: stateDir, RelayURL: baseURL}, r4r3Custody{})
	if err != nil {
		t.Fatalf("reopen revoked app: %v", err)
	}
	summary, err := app.StateSummary()
	if err != nil || summary.Paired {
		t.Fatalf("reopened revoked summary = (%+v,%v), want unpaired", summary, err)
	}
	if running, runErr := app.IsRunning(); runErr != nil || running {
		t.Fatalf("reopened revoked IsRunning = (%v,%v), want offline", running, runErr)
	}
	revokedState := app.core.State()
	if err := app.pin(&pairing.DeviceOutcome{
		MachineStatic: append([]byte(nil), revokedState.MachineStatic...),
		Machine: pairing.MachinePayload{
			Hostname:            revokedState.MachineName,
			MachineRoutingID:    mustDecodeRID(t, machineRID),
			MachineRelayAuthPub: append([]byte(nil), revokedState.MachineRelayAuthPub...),
			RecipientPub:        machineIdentity.RecipientPublic(),
			MachineSignPub:      append([]byte(nil), revokedState.MachineSignPub...),
			MachineEndpointID:   revokedState.Machine,
			RelaySPKIPin:        append([]byte(nil), revokedState.RelaySPKIPin...),
			RelayTLSPolicy:      revokedState.RelayTLSPolicy,
			OperatorNamespace:   revokedState.OperatorNamespace,
			EpochID:             revokedState.EpochID,
		},
	}); err != nil {
		t.Fatalf("same-home re-pair commit: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("Close repaired app: %v", err)
	}
	app, err = NewApp(&Config{StateDir: stateDir, RelayURL: baseURL}, r4r3Custody{})
	if err != nil {
		t.Fatalf("reopen repaired app: %v", err)
	}
	summary, err = app.StateSummary()
	if err != nil || !summary.Paired {
		t.Fatalf("reopened same-home repair summary = (%+v,%v), want paired", summary, err)
	}
}

func TestWorkerdPairingCommitFailurePublishesNoOutcomeOrAuthorization(t *testing.T) {
	baseURL := os.Getenv("MOBILE_RELAY_V2_HTTP")
	if baseURL == "" {
		t.Skip("MOBILE_RELAY_V2_HTTP is set by the fresh-workerd pairing gate")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	machinePriv := ed25519.NewKeyFromSeed(seed)
	machinePub := machinePriv.Public().(ed25519.PublicKey)
	machineRID := relayv2.RoutingID(machinePub)
	profile := relayv2.Profile{RelayURL: baseURL, MachineRID: machineRID,
		OperatorNamespace: "local-test", Security: relay.Security{AllowLoopbackCleartext: true}}
	control, err := relayv2.Dial(ctx, profile, relayv2.Auth{PublicKey: machinePub,
		Role: relayv2.RoleMachine, Purpose: relayv2.PurposeControl,
		Sign: func(message []byte) ([]byte, error) { return ed25519.Sign(machinePriv, message), nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()

	app, err := NewApp(&Config{StateDir: t.TempDir(), RelayURL: baseURL}, r4r3Custody{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	failing := &publicationMemoryStore{state: app.core.State(), failNext: true}
	failedCore, err := phonecore.Resume(phonecore.Config{Dir: app.coreDir, State: failing,
		WakeSealer: app.wakeSealer, ContentSealer: app.contentSealer})
	if err != nil {
		t.Fatal(err)
	}
	app.core = failedCore
	failedCore.TerminalControl().BindCoalescer(app.coalesce)

	machineIdentity, err := remotecrypto.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	machineSignPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var ceremony [16]byte
	var secret [32]byte
	_, _ = rand.Read(ceremony[:])
	_, _ = rand.Read(secret[:])
	approve := make(chan struct{})
	transport := relayv2.NewMachinePairTransport(control)
	type result struct {
		out *pairing.MachineOutcome
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := pairing.NewMachine(pairing.MachineParams{
			Static: machineIdentity.NoiseStatic(), Secret: secret, RendezvousID: ceremony,
			LocalConsole: true, Confirm: func(ctx context.Context, _ [6]string, _ string) (bool, error) {
				select {
				case <-approve:
					return true, nil
				case <-ctx.Done():
					return false, ctx.Err()
				}
			},
			Payload: pairing.MachinePayload{Hostname: "commit-fails.test",
				MachineRoutingID: mustDecodeRID(t, machineRID), MachineRelayAuthPub: machinePub,
				RecipientPub: machineIdentity.RecipientPublic(), MachineSignPub: machineSignPub,
				MachineEndpointID: "commit-fails", RelayTLSPolicy: "webpki",
				OperatorNamespace: "local-test", EpochID: 1},
		}).Pair(ctx, transport)
		done <- result{out, err}
	}()
	<-transport.Created()
	qr, err := pairing.EncodeQR(pairing.QRPayload{RelayURL: baseURL, RendezvousID: ceremony, PairingSecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	p, err := app.BeginPairing(qr)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := p.Origin()
	if err := p.ConfirmOrigin(origin); err != nil {
		t.Fatal(err)
	}
	for {
		if sas, _ := p.SAS(); sas != "" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("phone never reached SAS")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := p.Confirm(); err != nil {
		t.Fatal(err)
	}
	close(approve)
	got := <-done
	if got.out != nil || got.err == nil {
		t.Fatalf("machine result after phone commit failure = (%+v,%v), want no outcome", got.out, got.err)
	}
	if st := failedCore.State(); st.Machine != "" || len(st.MachineRelayAuthPub) != 0 {
		t.Fatalf("failed commit installed authority: %+v", st)
	}
	phonePub := ed25519.PublicKey(failedCore.KeyStore().RelayAuthPublic())
	_, err = relayv2.Dial(ctx, profile, relayv2.Auth{PublicKey: phonePub,
		Role: relayv2.RolePhone, Purpose: relayv2.PurposeStream, Sign: failedCore.KeyStore().SignRelayAuth})
	var protocolErr *relayv2.ProtocolError
	if !errors.As(err, &protocolErr) || protocolErr.Code != "not_authorized" {
		t.Fatalf("phone stream after failed commit = %v, want not_authorized", err)
	}
}

func waitMobileV2(t *testing.T, ctx context.Context, what string, ready func() bool) {
	t.Helper()
	for !ready() {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", what)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func mustDecodeRID(t *testing.T, rid string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(rid)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
