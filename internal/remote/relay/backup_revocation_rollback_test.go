package relay

// CAVEAT, pinned as a test rather than left an accident (reviewer follow-up on the R2 "backup"
// work package): Restore is a POINT-IN-TIME ROLLBACK of the whole store, and the revocation state
// is part of that store. `swarm remote revoke` is not append-only from the relay's point of view --
// revokeAndPurge (store.go) deletes the bucketPairs edge and writes a bucketRetired tombstone for
// the ceremony that granted it (ADR-007 B47: "a retired ceremony id is refused forever"). A backup
// taken BEFORE a revoke therefore does not know about it: restoring that backup brings the deleted
// edge back AND drops the tombstone, so a grantee that kept its old consent bytes -- exactly the
// artifact B47 exists to defeat a replay of -- is accepted again, and the phone the revoke was
// supposed to keep out can dial again, all without ever being asked.
//
// THIS IS NOT A BUG IN Backup/Restore. It is inherent to any point-in-time restore of a store that
// stores its authorization state IN the store (which is the only honest place to put it: the relay
// cannot hold a durable revocation anywhere the backup wouldn't also capture without contradicting
// its own "the relay stays an untrusted ciphertext mailbox" invariant). The operator remedy is
// simple and is what relay-runbook.md Sec.11 now documents: re-run `swarm remote revoke` for
// anything that was revoked after the backup being restored was taken -- restoring a backup is not
// a substitute for redoing recent revocations.
import (
	"crypto/ed25519"
	"path/filepath"
	"testing"
)

// TestRestore_RollsBackARevocationPerformedAfterTheBackup pins the caveat above end to end: a
// consent replayed after Restore succeeds again (the retirement tombstone is gone), and the
// revoked phone can dial the machine again (the ban and the pairs edge deletion are both gone).
func TestRestore_RollsBackARevocationPerformedAfterTheBackup(t *testing.T) {
	srv, cfg, apns, clk := startTestRelay(t, nil)
	ctx := testCtx(t)

	machinePub, machinePriv := newRelayAuthKey(t)
	phonePub, phonePriv := newRelayAuthKey(t)
	phoneRID := RoutingID(phonePub)

	machine := dialAuthed(t, srv.URL(), authFor(machinePub, machinePriv))
	consent := consentToCeremony(phonePriv, "the-original-pairing", machine.RoutingID())
	if err := machine.AuthorizeDevice(ctx, ed25519.PublicKey(phonePub), consent); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	if c, err := Dial(ctx, srv.URL(), authForPeer(phonePub, phonePriv, machine.RoutingID())); err != nil {
		t.Fatalf("precondition: phone cannot dial right after pairing: %v", err)
	} else {
		_ = c.Close()
	}

	// Back up NOW, before the revoke below -- this is the backup an operator restores from later.
	if err := srv.Close(); err != nil {
		t.Fatalf("Close (pre-revoke): %v", err)
	}
	backupPath := filepath.Join(t.TempDir(), "pre-revoke.db")
	if err := Backup(cfg.DBPath, backupPath); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	// The owner loses the phone and revokes it, on a relay restarted against the same live store.
	srv2, err := New(cfg, WithClock(clk), WithPushSink(apns))
	if err != nil {
		t.Fatalf("New (post-backup): %v", err)
	}
	if err := srv2.Start(ctx); err != nil {
		t.Fatalf("Start (post-backup): %v", err)
	}
	machine2 := dialAuthed(t, srv2.URL(), authFor(machinePub, machinePriv))
	if err := machine2.DeviceRevoke(ctx, phoneRID); err != nil {
		t.Fatalf("DeviceRevoke: %v", err)
	}
	if _, err := Dial(ctx, srv2.URL(), authForPeer(phonePub, phonePriv, machine2.RoutingID())); err == nil {
		t.Fatal("precondition: the revoke did not take effect, so this test proves nothing")
	}
	if err := srv2.Close(); err != nil {
		t.Fatalf("Close (post-revoke): %v", err)
	}

	// Restore the PRE-revoke backup over the live store -- e.g. an operator recovering from an
	// unrelated disaster, unaware a revoke happened after the backup was taken.
	if err := Restore(cfg.DBPath, backupPath); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	srv3, err := New(cfg, WithClock(clk), WithPushSink(apns))
	if err != nil {
		t.Fatalf("New (post-restore): %v", err)
	}
	if err := srv3.Start(ctx); err != nil {
		t.Fatalf("Start (post-restore): %v", err)
	}
	t.Cleanup(func() { _ = srv3.Close() })

	// The revoked phone can dial the machine again, with NO replay and NO new pairing -- the pairs
	// edge and the ban are both exactly as they were before the revoke, because Restore replaced
	// the whole store, edge and ban included.
	machineRID := RoutingID(machinePub)
	c, err := Dial(ctx, srv3.URL(), authForPeer(phonePub, phonePriv, machineRID))
	if err != nil {
		t.Fatalf("revoked phone cannot dial after restore = %v, want nil (the restore undid the "+
			"revoke without anyone replaying anything).", err)
	}
	_ = c.Close()

	// Separately: the retirement tombstone is gone too. Replaying the SAME consent bytes the
	// phone signed during the original pairing -- nothing new signed, the phone never asked again
	// -- succeeds, where ADR-007 B47 says it must not for a pairing that is still actually revoked.
	machine3 := dialAuthed(t, srv3.URL(), authFor(machinePub, machinePriv))
	if err := machine3.AuthorizeDevice(ctx, ed25519.PublicKey(phonePub), consent); err != nil {
		t.Fatalf("replayed consent after restore = %v, want nil.\n"+
			"  This is the other half of the caveat: Restore rolled back the revocation along with "+
			"everything else in the store, so the retired-ceremony tombstone that would normally "+
			"refuse this replay is gone too.", err)
	}
}
