package protocol

// Item 1.3 (agents-tracker-445), R1.3.7 — the Server exposes IsControlled so the
// daemon's grid tap can skip a session with a live controller lease, rather than
// stealing its stream every poll under concurrent shim serving.

import (
	"testing"
	"time"
)

func TestIsControlled_ReflectsControllerLease(t *testing.T) {
	stub := newStubDaemon()
	sock := tmpSock(t)
	srv, err := Serve(stub, sock)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	if srv.IsControlled("sess1") {
		t.Fatal("IsControlled(sess1) = true with no attach; want false")
	}

	c := dialClient(t, sock, []string{"attach"})
	a, err := c.Attach(NamespacedID(c.EndpointID(), "sess1"))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if !srv.IsControlled("sess1") {
		t.Fatal("IsControlled(sess1) = false while a controller is attached; want true (R1.3.7)")
	}
	// A different session is not controlled.
	if srv.IsControlled("other") {
		t.Fatal("IsControlled(other) = true; want false")
	}

	if err := a.Detach(); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	// Detach releases the lease asynchronously server-side; it must clear promptly.
	deadline := time.Now().Add(2 * time.Second)
	for srv.IsControlled("sess1") {
		if time.Now().After(deadline) {
			t.Fatal("IsControlled(sess1) still true 2s after detach; want false (heuristic resumes)")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
