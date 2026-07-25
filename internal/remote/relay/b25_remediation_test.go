package relay

// FENCES FOR THE MACHINERY THAT REMEDIATES ADR-007 B25, as opposed to for the defect
// itself — b25_authority_test.go fences the defect and is not touched here.
//
// The remediation replaced the pairing gate (`isPaired(caller, target)`, satisfiable by
// the caller alone) with store.mayActOn: THE TARGET MUST HAVE AUTHORIZED THE CALLER, OR
// HAVE AUTHORIZED NOBODY AT ALL. Everything fenced here fails SILENTLY — it leaves the
// three B25 tests green while breaking something real:
//
//   - the second clause EXISTS (TestB25Fix_AFirstPairingCanStillDeliverTheEpochGrant).
//     Without it every first pairing is refused, which is the measurement that falsified
//     the mutual-pairing direction recorded in ADR-007 B25.
//   - the second clause CLOSES FOR GOOD (TestB25Fix_ARevokeDoesNotReopenTheBootstrapWindow).
//     A revoke deletes the authorization it severs, so a machine that revoked its only
//     device has no live grant left — and "has authorized nobody" would be true of it
//     again, handing back the exact permanent lockout B25 describes.
//   - the separate availability defect ADR-007 B26 names, which the authority fix does
//     NOT address (TestB25Fix_OneSendersBacklogDoesNotRefuseAnothersAppend): the depth
//     cap was charged to the mailbox rather than to the sender who filled it.
//   - the hazard that fix introduced (TestB25Fix_APreVersionItemRecordIsSkippedRatherThanMisread):
//     charging a sender means storing one, which changed the item record's layout.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// TestB25Fix_AFirstPairingCanStillDeliverTheEpochGrant is the bootstrap path, at the
// relay, in the shape cmd/swarm-remote/deliver.go uses it.
//
// deliverEpochGrant authorizes the paired device and IMMEDIATELY appends the sealed
// epoch grant — the append that delivers the ContentKey, so nothing about the pairing
// works until it lands — and its failure is fatal (cmd/swarm-remote/main.go). The phone
// need not have connected to the relay yet, so it cannot have authorized the machine
// yet, and it cannot authorize anything before it holds the grant either. Any authority
// rule that demands the target's prior authorization therefore forbids first pairings.
//
// This is the constraint that killed the mutual-pairing direction, measured there as
// TestDeliverEpochGrant_AuthorizesAndAppendsBootstrap failing with "not authorized for
// route". It is fenced HERE as well as there because the rule it constrains lives here:
// a change to mayActOn that breaks pairing should fail in the package that made it.
func TestB25Fix_AFirstPairingCanStillDeliverTheEpochGrant(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)

	machine := newB25Party(t, srv)
	phonePub, phonePriv := newRelayAuthKey(t)
	phoneRID := RoutingID(phonePub)

	// The phone has never connected: it exists only as a public key the machine learned
	// over the pairing channel. This is the whole point — the relay cannot witness the
	// ceremony that conveyed its consent.
	if err := machine.cl.AuthorizeDevice(testCtx(t), phonePub); err != nil {
		t.Fatalf("machine authorizes the freshly paired phone: %v", err)
	}

	sp := newSealParty(t, []byte("machine-sender-pub-000000000000x"), []byte("device-recipient-pub-0000000000x"))
	if _, err := machine.cl.MailboxAppend(testCtx(t), phoneRID, sp.sealMailbox(t, 1, []byte("epoch-grant"), clk)); err != nil {
		t.Fatalf("the epoch grant could not be delivered to a phone that has not connected yet: %v\n"+
			"store.mayActOn's bootstrap clause is what admits this append: the target has authorized "+
			"nobody, so its first peer is admitted. Without it deliverEpochGrant fails at its second "+
			"call, the failure is FATAL to the gateway, and no device can ever complete a first "+
			"pairing — the measurement that falsified ADR-007 B25's mutual-pairing direction.", err)
	}

	// And the window is not a free-for-all: the exception admits the FIRST peer, so once
	// the phone has authorized the machine back, the ordinary rule carries the route.
	phoneCl := dialAuthed(t, srv.URL(), authFor(phonePub, phonePriv))
	if err := phoneCl.AuthorizeDevice(testCtx(t), machine.pub); err != nil {
		t.Fatalf("phone authorizes the machine (mobile/relay.go onConnected): %v", err)
	}
	if _, err := machine.cl.MailboxAppend(testCtx(t), phoneRID, sp.sealMailbox(t, 2, []byte("journal"), clk)); err != nil {
		t.Fatalf("the machine cannot append to the phone that just authorized it: %v", err)
	}
}

// TestB25Fix_ARevokeDoesNotReopenTheBootstrapWindow is the hole the bootstrap exception
// opens on its own, and it is reachable through the product's own recovery flow.
//
// The exception keys on "the target has authorized nobody". revokeAndPurge DELETES the
// authorization it severs, so a machine whose only device has been revoked — `swarm
// remote revoke`, PB-STATE-10's second step, the thing the owner is told to do when a
// handset is lost — has no live grant left. Counting live grants alone would say that
// machine has authorized nobody, put it back in its bootstrap window, and hand any
// party that can open a socket the whole of ADR-007 B25 again: self-pair, revoke, and
// the machine's relay identity is destroyed with a ban only the attacker can lift.
//
// So mayActOn's window closes on having authorized OR BANNED anyone, and this is the
// half no other test covers.
func TestB25Fix_ARevokeDoesNotReopenTheBootstrapWindow(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	machine, phone := b25LegitPair(t, srv, clk)

	// The owner loses the handset and revokes it. The machine's only grant goes with it.
	if err := machine.cl.DeviceRevoke(testCtx(t), phone.rid); err != nil {
		t.Fatalf("the owner's machine revokes its own paired device: %v", err)
	}

	stranger := newB25Party(t, srv)
	_ = stranger.cl.AuthorizeDevice(testCtx(t), stranger.pubOf(machine))

	revokeErr := stranger.cl.DeviceRevoke(testCtx(t), machine.rid)
	if !errors.Is(revokeErr, ErrNotAuthorized) {
		_, redialErr := Dial(testCtx(t), srv.URL(), authFor(machine.pub, machine.priv))
		t.Fatalf("a stranger revoked the machine BECAUSE THE MACHINE HAD JUST REVOKED ITS OWN DEVICE.\n"+
			"  stranger's device_revoke against the machine: %v (want ErrNotAuthorized)\n"+
			"  the machine re-dialing its own relay afterwards: %v\n"+
			"store.mayActOn admits a caller when the target has authorized nobody, and revokeAndPurge "+
			"deletes the grant it severs — so counting live grants alone re-opens the bootstrap window "+
			"of every machine that has revoked its only device, which is the recovery step PB-STATE-10 "+
			"tells the owner to perform. hasActedAsAuthority must count the BAN as well as the grant.",
			revokeErr, redialErr)
	}

	// The machine is still reachable, which is the property the ban would have destroyed.
	cl, err := Dial(testCtx(t), srv.URL(), authFor(machine.pub, machine.priv))
	if err != nil {
		t.Fatalf("the machine cannot reach its own relay after revoking its device: %v", err)
	}
	_ = cl.Close()
}

// TestB25Fix_OneSendersBacklogDoesNotRefuseAnothersAppend is the availability defect
// ADR-007 B26 records as independent of the authority hole: the mailbox depth cap was
// enforced per TARGET (server.go, mailboxDepth(req.Target) >= capN), shared across every
// sender, so whoever filled it first held every other sender out of a mailbox that is not
// theirs — for as long as they kept it full, since the cap is on LIVE depth.
//
// Both senders here are legitimately authorized by the target, which is deliberate: the
// authority fix does NOT fix this, and stating the property over two authorized senders
// is the only way to show that. The fence asserts both halves, because a cap that stops
// refusing the wrong sender must still refuse the right one.
func TestB25Fix_OneSendersBacklogDoesNotRefuseAnothersAppend(t *testing.T) {
	const depthCap = 4
	srv, _, _, clk := startTestRelay(t, func(c *Config) {
		c.Quotas.MailboxMaxItems = depthCap
	})

	target := newB25Party(t, srv)
	noisy := newB25Party(t, srv)
	quiet := newB25Party(t, srv)

	// The target authorizes both senders: both may append to it, by the ordinary rule.
	for _, peer := range []b25Party{noisy, quiet} {
		if err := target.cl.AuthorizeDevice(testCtx(t), target.pubOf(peer)); err != nil {
			t.Fatalf("target authorizes a sender: %v", err)
		}
	}

	sp := newSealParty(t, []byte("sender-pub-0000000000000000000x0"), []byte("target-recipient-pub-00000000x00"))

	// The noisy sender fills its whole budget and is then refused — the cap still binds.
	for i := 0; i < depthCap; i++ {
		if _, err := noisy.cl.MailboxAppend(testCtx(t), target.rid, sp.sealMailbox(t, uint64(i+1), []byte("noise"), clk)); err != nil {
			t.Fatalf("the noisy sender's append %d/%d inside its own budget was refused: %v",
				i+1, depthCap, err)
		}
	}
	if _, err := noisy.cl.MailboxAppend(testCtx(t), target.rid, sp.sealMailbox(t, 99, []byte("noise"), clk)); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("the noisy sender appended %d items and its next one returned %v, want ErrQuotaExceeded; "+
			"per-sender accounting must still CAP each sender, or it is not a depth cap at all",
			depthCap, err)
	}

	// THE PROPERTY: the other sender is untouched by that backlog.
	if _, err := quiet.cl.MailboxAppend(testCtx(t), target.rid, sp.sealMailbox(t, 100, []byte("owner keystroke"), clk)); err != nil {
		t.Fatalf("a second authorized sender's append was refused because ANOTHER sender had filled "+
			"the mailbox: %v\n"+
			"The depth cap must be charged per (sender, target). Charged per target it is a shared "+
			"budget with no owner: one sender's backlog locks every other sender out — the owner's own "+
			"handset held out of its own machine — and because the cap is on LIVE depth it lasts exactly "+
			"as long as that sender keeps re-filling it.", err)
	}
}

// TestB25Fix_APreVersionItemRecordIsSkippedRatherThanMisread fences the one hazard the
// per-sender depth cap introduced: the stored item record grew a SENDER field, so a
// relay upgraded in place over an existing store file holds records written under the
// old layout ([8 append time][envelope], no version byte, no sender).
//
// Read as the new layout, such a record hands out its first 32 envelope bytes as a
// routing id and serves the REST as the envelope — a silently truncated frame that no
// key can open and that the phone can never drain, since the drain advances its cursor
// only for frames the core opened. That is a worse failure than losing the item.
//
// The version byte makes the two layouts distinguishable (an old record starts with the
// top byte of a millisecond timestamp, which is 0x00 this millennium), and the store
// fails closed on anything it did not write.
func TestB25Fix_APreVersionItemRecordIsSkippedRatherThanMisread(t *testing.T) {
	st, err := openStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = st.close() })

	const rid = "0123456789abcdef0123456789abcdef"
	// One record in the layout shipped before the sender field: [8 time][envelope].
	legacy := make([]byte, 8+64)
	binary.BigEndian.PutUint64(legacy[:8], uint64(1_700_000_000_000))
	copy(legacy[8:], bytes.Repeat([]byte{0xAB}, 64))
	if err := st.db.Update(func(tx *bolt.Tx) error {
		mb, err := tx.Bucket(bucketItems).CreateBucketIfNotExists([]byte(rid))
		if err != nil {
			return err
		}
		return mb.Put(u64(1), legacy)
	}); err != nil {
		t.Fatalf("write a pre-version record: %v", err)
	}

	items, _, err := st.readItemsPage(rid, 0, 16, 1<<20)
	if err != nil {
		t.Fatalf("readItemsPage: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("a record written under the pre-sender layout was SERVED as an item: envelope %d "+
			"bytes, and the 32 bytes before it were consumed as a routing id.\n"+
			"The item record carries a version byte precisely so the two layouts cannot be "+
			"confused. Serving one truncates a frame no key can open, and the phone's drain never "+
			"advances past a frame it cannot open — so one mis-read record stalls that mailbox for "+
			"the whole retention window.", len(items[0].Envelope))
	}
	if n := st.mailboxDepthFrom(rid, rid); n != 0 {
		t.Fatalf("a pre-version record was charged to a sender's depth budget: got %d, want 0", n)
	}
}
