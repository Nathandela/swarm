package protocol

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

// Integration coverage for the audit-006 fixes that need a REAL daemon + shim: the
// supersede re-snapshot over the real shim (F1) and no-silent-loss status delivery
// through FromDaemon's roster poller (F4).

// TestFix_SupersedeReSnapshotRealShim drives F1 end-to-end: the superseding
// controller's snapshot is re-fetched from the live shim (a valid, non-empty grid),
// proving the real re-snapshot path (fromdaemon shimStream.ReSnapshot) works and
// never deadlocks or returns an empty snapshot.
func TestFix_SupersedeReSnapshotRealShim(t *testing.T) {
	d := realDaemon(t)
	launchRealSession(t, d)

	sock := tmpSock(t)
	srv, err := Serve(FromDaemon(d), sock)
	if err != nil {
		t.Fatalf("Serve(FromDaemon): %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	c := dialClient(t, sock, []string{"attach"})
	var id string
	deadline := time.Now().Add(launchTimeout)
	for time.Now().Before(deadline) {
		views, err := c.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(views) == 1 {
			id = views[0].ID
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if id == "" {
		t.Fatalf("session never appeared within %s", launchTimeout)
	}

	a, err := c.Attach(id)
	if err != nil {
		t.Fatalf("first Attach: %v", err)
	}
	if len(a.Snapshot()) == 0 {
		t.Fatalf("first attach snapshot empty")
	}

	// Supersede: the new controller's snapshot is RE-fetched from the live shim.
	c2 := dialClient(t, sock, []string{"attach"})
	b, err := c2.Attach(onlyViewID(t, c2))
	if err != nil {
		t.Fatalf("superseding Attach (real re-snapshot, F1): %v", err)
	}
	if b.Generation() <= a.Generation() {
		t.Errorf("supersede generation: B=%d not > A=%d", b.Generation(), a.Generation())
	}
	if len(b.Snapshot()) == 0 {
		t.Fatalf("superseding controller got an EMPTY snapshot from the real shim — re-snapshot failed (F1)")
	}
}

// TestFix_StatusChangeDeliveredThroughFromDaemon drives F4 end-to-end: a real
// status change (running -> terminated) reaches a subscriber via FromDaemon's
// roster poller, proving the watch() loop emits changes without silently losing
// them.
func TestFix_StatusChangeDeliveredThroughFromDaemon(t *testing.T) {
	d := realDaemon(t)
	launchRealSession(t, d)

	sock := tmpSock(t)
	srv, err := Serve(FromDaemon(d), sock)
	if err != nil {
		t.Fatalf("Serve(FromDaemon): %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	c := dialClient(t, sock, []string{"subscribe"})
	var id string
	deadline := time.Now().Add(launchTimeout)
	for time.Now().Before(deadline) {
		views, err := c.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(views) == 1 {
			id = views[0].ID
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if id == "" {
		t.Fatalf("session never appeared within %s", launchTimeout)
	}

	ch, err := c.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Terminate the session; the roster poller must deliver the status change.
	if err := c.Kill(id); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	deadline = time.Now().Add(launchTimeout)
	for time.Now().Before(deadline) {
		ev, ok := recvEvent(t, ch, 2*time.Second)
		if !ok {
			continue
		}
		if ev.Session.Status.Process != status.ProcessRunning {
			return // observed the running -> terminal transition
		}
	}
	t.Fatalf("status change never delivered through FromDaemon after Kill (F4)")
}
