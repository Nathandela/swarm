package main

// FAILING-FIRST tests for ADR-007 B120 finding F3 (HIGH): the stolen-handset revoke fails
// at the relay, silently, and the relay decides whether it does.
//
// THE DEFECT, at HEAD. runRemoteRevoke (cmd/swarm/remote.go) reads the routing id, revokes
// locally, then calls purgeRelayState -- a function that RETURNS NOTHING and whose every
// relay failure is a line on stderr -- and then unconditionally prints "revoked device <id>"
// and returns 0. So all three relay-side outcomes are reported identically at the exit code,
// and the one sentence the operator reads is the same in all three:
//
//	the relay acked the purge      -> "revoked device X", exit 0   (true)
//	the relay refused the purge    -> "revoked device X", exit 0   (false)
//	the relay was never reached    -> "revoked device X", exit 0   (false)
//
// Post-B133 the phone is trusted and the wire is the trust boundary, which makes this verb
// the product's ONLY safety net for a lost handset. B120 measured what survives a "successful"
// revoke whose relay half did not land: the revoked handset RETAINED mailbox drain, append
// into the owner's machine mailbox, push wake delivery, and a relay re-auth whose Peer query
// said it had not been revoked. `swarm remote revoke` said the phone was cut off; the phone
// was not cut off; nothing anywhere said otherwise.
//
// WHAT THE FIX OWES, and what these tests pin. ADR-007 D9 defines the relay side of the
// revocation as "device de-authorization + mailbox purge on revocation (a revoked device
// keeps neither connectivity nor a drainable pre-rotation mailbox; an offline-at-revoke
// machine defers the purge to reconnect)". Two obligations follow:
//
//	(1) SUCCESS MUST BE VERIFIED, NOT ASSUMED. Exit 0 is a claim about the relay, so it may
//	    only be made once the relay has acknowledged the purge.
//	(2) THE THREE OUTCOMES MUST READ DIFFERENTLY. D9's own parenthesis blesses a DEFERRED
//	    purge for a machine that is offline at revoke time -- so "purged" and "not purged
//	    yet, pending" are both legitimate states and the operator has to be able to tell
//	    which one they are in, because only one of them means the thief is locked out now.
//
// THE VERDICT VOCABULARY these tests pin, one token each so a regression names which arm
// broke rather than only that the output changed:
//
//	acked      exit 0,       output says "purged"  and never says "pending"
//	refused    exit nonzero, output carries the RELAY'S OWN reason (here: "quota")
//	unreachable exit nonzero, output says "pending"
//
// Nonzero for the unreachable arm is deliberate and is the whole point of the finding: a
// pending purge is a thief who still drains the mailbox, and a shell -- or an owner scanning
// for the one line that matters during an incident -- must not read that as done. The local
// half is durable by then either way, so both failing arms still assert the device is GONE
// from the registry: the exit code says "the relay half is not finished", never "nothing
// happened" (the epoch rotation of the 2026-07-24 amendment rides that same local half).
//
// WHY A PROXY FOR THE REFUSED ARM. relay.ErrNotAuthorized is the one refusal runRemoteRevoke
// deliberately treats as benign ("no mailbox of ours to empty"), and that ruling is not this
// slice's to reopen -- so the refusal here is a clean quota_exceeded, which is exactly what a
// real relay answers a device_revoke past its per-key OpsPerMin budget (harden_test.go
// enumerates device_revoke among the rate-limited ops). The proxy interposes only that one
// op; auth and every other frame are served by the REAL relay behind it.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/machineid"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// b1MailboxItems is how many frames the stranded handset's relay mailbox holds before the
// revoke. It only has to be non-zero: a purge assertion over an empty mailbox would pass
// whatever production does, and the refused arm asserts the SAME depth survives.
const b1MailboxItems = 3

// b1Rig is one machine provisioned for remote control (`swarm remote init --relay-url`),
// one paired device in its registry, a live daemon over both, and a real relay holding that
// device's mailbox -- the fixture all three arms start from.
//
// The device is SYNTHETIC rather than a real swarmmobile handset: what the revoke's relay
// half acts on is a routing id, a relay-auth key and a route consent, and those three are
// producible directly. The relay verifies the consent for real (handleAuthorizeDevice checks
// the device's own signature over relay.ConsentMessage), so the mailbox this fixture fills
// is a genuinely authorized route and not a test seam.
type b1Rig struct {
	relay     *relay.Server
	stateDir  string
	rec       device.Record
	routingID string
}

// b1NewRig provisions the machine and fills the device's mailbox. front, when non-nil,
// interposes something between the machine and the relay and returns the URL the machine
// must dial; the fixture's own appends always go to the relay directly, so only the CLI's
// connection is affected by it.
func b1NewRig(t *testing.T, front func(*testing.T, string) string) *b1Rig {
	t.Helper()
	// Off the real system: the supervisor seam is faked and swarm-remote is resolved from a
	// temp dir, so the revoke's gateway stop never reaches launchd or systemd.
	installFakeSupervisor(t)
	fakeGatewayBinaryOnPath(t)

	srv, relayURL := s18bFreshRelay(t)
	dialURL := relayURL
	if front != nil {
		dialURL = front(t, relayURL)
	}

	rig := &b1Rig{relay: srv, stateDir: shortStateDir(t)}
	t.Setenv(daemon.EnvStateDir, rig.stateDir)

	var out, errOut bytes.Buffer
	if exit := runRemote([]string{"init", "--relay-url", dialURL}, &out, &errOut); exit != 0 {
		t.Fatalf("`swarm remote init --relay-url %s` exit = %d, want 0; stderr=%q",
			dialURL, exit, errOut.String())
	}

	id, err := machineid.Load(filepath.Join(rig.stateDir, "remote", remoteIdentityFile))
	if err != nil {
		t.Fatalf("machineid.Load: %v", err)
	}
	rig.rec = b1SeedPairedDevice(t, rig.stateDir, id)
	rig.routingID = string(rig.rec.RoutingID)

	// The daemon opens the registry once, so the device has to be on disk before it starts.
	startCLIDaemon(t, rig.stateDir)
	rig.b1FillMailbox(t, relayURL, id)
	return rig
}

// b1SeedPairedDevice writes one paired device into the registry at <stateDir>/devices with
// everything the revoke's relay half needs: a real relay-auth keypair (so the routing id is
// the one relay.RoutingID derives), and a route consent this device signed for THIS machine
// (so the relay accepts the machine's authorize_device and the mailbox appends that follow).
//
// IT ALSO HAS TO SURVIVE THE DAEMON'S STARTUP RECONCILE, which is not decoration:
// skeleton.Serve's reconcilePairedDevices removes any device whose GrantedEpoch is not the
// machine's current one, and any device with no sealed grant sidecar (a crash between AddSole
// and grant.Save). A fixture that skipped either would hand the daemon an empty registry and
// every assertion below would be about the "no such device" refusal instead.
func b1SeedPairedDevice(t *testing.T, stateDir string, machine *machineid.Identity) device.Record {
	t.Helper()
	relayPub, relayPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate the device's relay-auth key: %v", err)
	}
	cmdPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate the device's command-signing key: %v", err)
	}
	devIdent, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("generate the device's noise/recipient identity: %v", err)
	}
	// The ceremony id is the rendezvous id in production (ADR-007 B47). Nothing here retires
	// one, and each rig has its own relay store, so a fixed label is a live id.
	const ceremonyID = "b1-pairing-ceremony"
	// relay.RoutingID, not machineid.Identity.RoutingID: the latter is the hex-DECODED form,
	// and the consent names the grantee's routing id as the relay spells it.
	machineRID := relay.RoutingID(machine.RelayAuthPublic())
	consent := relay.MarshalConsent(ceremonyID,
		ed25519.Sign(relayPriv, relay.ConsentMessage(ceremonyID, machineRID)))

	rec := device.Record{
		DeviceID:       device.DeviceIDFor(cmdPub),
		Name:           "Nathan's iPhone",
		NoiseStaticPub: devIdent.NoiseStaticPublic(),
		RelayAuthPub:   relayPub,
		CommandSignPub: cmdPub,
		RecipientPub:   devIdent.RecipientPublic(),
		RoutingID:      []byte(relay.RoutingID(relayPub)),
		Capability:     device.CapFull,
		PairedAt:       time.Now().Truncate(time.Second),
		GrantedEpoch:   machine.EpochID(),
		ConsentSig:     consent,
	}
	registryDir := filepath.Join(stateDir, "devices")
	reg, err := device.Open(registryDir)
	if err != nil {
		t.Fatalf("device.Open: %v", err)
	}
	if err := reg.Add(rec); err != nil {
		t.Fatalf("device registry Add: %v", err)
	}
	g, err := crypto.SealEpochGrant(machine.GrantSignPrivate(), rec.RecipientPub,
		machine.EpochID(), machine.NextGrantSeq(0), machine.EpochKeys())
	if err != nil {
		t.Fatalf("crypto.SealEpochGrant: %v", err)
	}
	if err := grant.Save(registryDir, rec.DeviceID, g); err != nil {
		t.Fatalf("grant.Save: %v", err)
	}
	return rec
}

// b1FillMailbox puts b1MailboxItems frames in the device's relay mailbox as the gateway
// does: the machine authenticates with its own relay-auth identity, authorizes the device,
// and appends. The payload is opaque to the relay, so plain bytes are honest.
//
// The connection is closed before it returns: the CLI dials the relay under the SAME routing
// id and a second connection supersedes the first.
func (r *b1Rig) b1FillMailbox(t *testing.T, relayURL string, id *machineid.Identity) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cl, err := relay.Dial(ctx, relayURL, relay.ClientAuth{
		RelayAuthPub: id.RelayAuthPublic(),
		Sign:         func(challenge []byte) ([]byte, error) { return id.RelayAuthSign(challenge), nil },
	})
	if err != nil {
		t.Fatalf("machine relay.Dial: %v", err)
	}
	if err := cl.AuthorizeDevice(ctx, ed25519.PublicKey(r.rec.RelayAuthPub), r.rec.ConsentSig); err != nil {
		t.Fatalf("machine AuthorizeDevice: %v", err)
	}
	for i := 0; i < b1MailboxItems; i++ {
		if _, err := cl.MailboxAppend(ctx, r.routingID, []byte(fmt.Sprintf("sealed-frame-%d", i))); err != nil {
			t.Fatalf("machine mailbox append %d: %v", i, err)
		}
	}
	if err := cl.Close(); err != nil {
		t.Fatalf("close the fixture's relay client: %v", err)
	}
	if got := r.relay.MailboxDepth(r.routingID); got != b1MailboxItems {
		t.Fatalf("the fixture left %d item(s) in the device's relay mailbox, want %d; every "+
			"assertion below is about what a revoke does to them", got, b1MailboxItems)
	}
}

// b1Revoke runs the real verb and returns its exit code and its whole operator-visible
// output, lowercased -- both channels together, because which one a verdict lands on is the
// implementation's choice and the operator reads both.
func (r *b1Rig) b1Revoke(t *testing.T) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exit := runRemote([]string{"revoke", r.rec.DeviceID}, &stdout, &stderr)
	return exit, strings.ToLower(stdout.String() + stderr.String())
}

// b1RequireDeviceGone asserts the LOCAL half of the revocation stood, whatever the relay
// half did. It is what stops a nonzero exit from being read as "nothing happened": the
// device is de-registered and the epoch rotated before the relay is ever dialled, and a fix
// that made the command fail by not revoking would trade this defect for a worse one.
func (r *b1Rig) b1RequireDeviceGone(t *testing.T) {
	t.Helper()
	var out, errOut bytes.Buffer
	if exit := runRemote([]string{"devices"}, &out, &errOut); exit != 0 {
		t.Fatalf("`swarm remote devices` after the revoke exit = %d; stderr=%q", exit, errOut.String())
	}
	if strings.Contains(out.String(), r.rec.DeviceID) {
		t.Errorf("the revoke reported the relay half unfinished AND left the device registered, "+
			"so the local revocation was rolled back too; devices:\n%s", out.String())
	}
}

// TestB120F3_RevokeConfirmsTheRelayPurgeBeforeReportingSuccess is the ACKED arm: the relay
// is reachable, the purge lands, and the mailbox is empty afterwards.
//
// It is the arm that is allowed to exit 0, and the only one -- which is why it also pins the
// WORDING. Today's output is "revoked device X" plus the re-pair pointer, and that sentence
// is identical in all three arms; an operator holding it cannot tell whether the stolen
// handset's mailbox is gone or merely not purged yet. So the confirmation has to SAY the
// relay-side purge happened, and must not hedge it as pending when it did not.
func TestB120F3_RevokeConfirmsTheRelayPurgeBeforeReportingSuccess(t *testing.T) {
	rig := b1NewRig(t, nil)

	exit, out := rig.b1Revoke(t)

	if exit != 0 {
		t.Fatalf("`swarm remote revoke` exit = %d against a relay that ACKED the purge, want 0; "+
			"output:\n%s", exit, out)
	}
	if got := rig.relay.MailboxDepth(rig.routingID); got != 0 {
		t.Fatalf("precondition: the relay still holds %d item(s) for the revoked device, so this "+
			"arm is not the acked one at all", got)
	}
	if !strings.Contains(out, "purged") {
		t.Errorf("ADR-007 B120 F3: the revoke confirmed nothing about the RELAY side -- the "+
			"operator reads the same sentence here as when the purge never happened, and only "+
			"here does it mean the handset is locked out now. output:\n%s", out)
	}
	if strings.Contains(out, "pending") {
		t.Errorf("the relay acknowledged the purge and the output still calls it pending, which "+
			"sends the owner chasing a step that is already done. output:\n%s", out)
	}
}

// TestB120F3_RevokeFailsWhenTheRelayRefusesThePurge is the REFUSED arm: the relay is up, the
// machine reaches it, and it answers the device_revoke with a clean refusal.
//
// The mailbox assertion is the finding itself, measured rather than argued: the frames are
// still there afterwards, so the handset this command told the owner was cut off can still
// drain them. Exit 0 over that state is the defect.
func TestB120F3_RevokeFailsWhenTheRelayRefusesThePurge(t *testing.T) {
	rig := b1NewRig(t, b1RefusingRelay)

	exit, out := rig.b1Revoke(t)

	if got := rig.relay.MailboxDepth(rig.routingID); got != b1MailboxItems {
		t.Fatalf("precondition: the relay purged %d of %d item(s) despite refusing the op, so "+
			"this arm is not the refused one", b1MailboxItems-got, b1MailboxItems)
	}
	if exit == 0 {
		t.Errorf("ADR-007 B120 F3: `swarm remote revoke` exit = 0 while the relay REFUSED the "+
			"purge and the revoked handset's mailbox still holds %d item(s) it can drain. The "+
			"exit code is the only thing a script reads, and this verb is the product's whole "+
			"safety net for a lost handset. output:\n%s", b1MailboxItems, out)
	}
	if !strings.Contains(out, "quota") {
		t.Errorf("the relay's own reason for refusing is not in the output, so the owner cannot "+
			"tell a refusal from an unreachable relay -- the two have different remedies. "+
			"output:\n%s", out)
	}
	rig.b1RequireDeviceGone(t)
}

// TestB120F3_RevokeReportsThePendingPurgeWhenTheRelayIsUnreachable is the OFFLINE arm: the
// machine cannot reach the relay at all, which is D9's own "an offline-at-revoke machine
// defers the purge to reconnect".
//
// A deferred purge is a legitimate state and a finished one is not the same state, so the
// output has to name it: until the machine reconnects and the purge lands, the revoked
// handset keeps its mailbox, its push token and its relay route. B120 recorded that no
// pending-purge state exists anywhere in the tree, which is why this arm is red twice over
// -- nothing defers it and nothing says so.
func TestB120F3_RevokeReportsThePendingPurgeWhenTheRelayIsUnreachable(t *testing.T) {
	rig := b1NewRig(t, nil)
	// The machine goes offline AFTER pairing and after the mailbox filled, which is the
	// incident's shape: the owner discovers the loss on a laptop that cannot reach the relay.
	if err := rig.relay.Close(); err != nil {
		t.Fatalf("close the relay to make it unreachable: %v", err)
	}

	exit, out := rig.b1Revoke(t)

	if exit == 0 {
		t.Errorf("ADR-007 B120 F3: `swarm remote revoke` exit = 0 with the relay UNREACHABLE. "+
			"Nothing was de-authorized and nothing was purged -- the handset keeps mailbox "+
			"drain, push wake and relay re-auth -- and the command reported the same success it "+
			"reports when the purge landed. output:\n%s", out)
	}
	if !strings.Contains(out, "pending") {
		t.Errorf("ADR-007 D9: the purge is DEFERRED to reconnect here, and the output does not "+
			"say so, so the owner cannot distinguish it from the purge that actually happened. "+
			"output:\n%s", out)
	}
	rig.b1RequireDeviceGone(t)
}

// b1RefusingRelay is a websocket proxy in front of the REAL relay that answers exactly one
// op -- device_revoke -- with a clean quota refusal, and forwards every other frame in both
// directions untouched. Auth, authorize_device and the appends are therefore served by the
// real relay: the only thing synthetic about this arm is the answer to the one op under test.
//
// It returns the ws:// URL the machine's relay.json is pointed at.
func b1RefusingRelay(t *testing.T, upstream string) string {
	t.Helper()
	// The relay's own wire error for a device_revoke past its per-key op budget: the client
	// maps the code back to relay.ErrQuotaExceeded, which is a refusal runRemoteRevoke has no
	// standing reason to treat as benign (unlike ErrNotAuthorized).
	//
	// AMENDED BY SH5 (2026-08-22): purgeRelayState now classifies a quota answer as
	// TRANSIENT -- a rate window clears by itself -- so this arm exercises the pending/
	// deferred path rather than a substantive refusal. Its assertions (nonzero exit, the
	// relay's reason visible, mailbox untouched, device gone locally) hold in both
	// classifications, which is why they are unchanged; the substantive-refusal arm is
	// exercised by sh5RefusingFront with a bad_request answer.
	return sh5RefusingFront(t, upstream, `{"code":"quota_exceeded","message":"relay: quota exceeded"}`)
}

// sh5RefusingFront is b1RefusingRelay generalized over the refusal answered to
// device_revoke; everything else forwards to the real relay untouched.
func sh5RefusingFront(t *testing.T, upstream, refusalJSON string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := relay.WriteFrame(&buf, relay.MsgError, []byte(refusalJSON)); err != nil {
		t.Fatalf("compose refusal frame: %v", err)
	}
	return sh5AnsweringFront(t, upstream, buf.Bytes())
}

// sh5AnsweringFront answers device_revoke with EXACTLY the given bytes -- a
// well-formed error frame or deliberate garbage -- and forwards everything else.
func sh5AnsweringFront(t *testing.T, upstream string, reply []byte) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		down, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = down.CloseNow() }()
		down.SetReadLimit(relay.MaxFrame + 64)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		up, _, err := websocket.Dial(ctx, upstream, nil)
		if err != nil {
			return
		}
		defer func() { _ = up.CloseNow() }()
		up.SetReadLimit(relay.MaxFrame + 64)

		done := make(chan struct{}, 2)
		go func() {
			defer func() { done <- struct{}{} }()
			for {
				mt, data, err := up.Read(ctx)
				if err != nil {
					return
				}
				if err := down.Write(ctx, mt, data); err != nil {
					return
				}
			}
		}()
		go func() {
			defer func() { done <- struct{}{} }()
			for {
				mt, data, err := down.Read(ctx)
				if err != nil {
					return
				}
				// The refused op never reaches the relay, so the mailbox it would have emptied
				// stays exactly as it was -- which is what the test measures.
				if b1IsDeviceRevoke(data) {
					if err := down.Write(ctx, websocket.MessageBinary, reply); err != nil {
						return
					}
					continue
				}
				if err := up.Write(ctx, mt, data); err != nil {
					return
				}
			}
		}()
		<-done
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// b1IsDeviceRevoke reports whether one client->server websocket message carries the
// device_revoke control op. Every relay frame is one binary message (Conn.writeFrame), and
// control requests ride MsgRelay with a JSON "op" discriminator.
func b1IsDeviceRevoke(frame []byte) bool {
	tag, payload, err := relay.ReadFrame(bytes.NewReader(frame))
	if err != nil || tag != relay.MsgRelay {
		return false
	}
	var req struct {
		Op string `json:"op"`
	}
	return json.Unmarshal(payload, &req) == nil && req.Op == "device_revoke"
}
