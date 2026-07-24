package protocol

import (
	"errors"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// v0.5 rename — the client sends a rename op with the target session id and the new
// label; the Server RE-VALIDATES the label server-side (reusing sanitizeName:
// control characters stripped, capped to maxNameRunes) and forwards the SANITIZED
// (de-namespaced local id, name) to the DaemonAPI, then replies ok. Modeled
// end-to-end on the existing simple ops (kill/delete).

func renameStub(t *testing.T) (*stubDaemon, *Client) {
	t.Helper()
	stub := newStubDaemon()
	stub.setMetas(persist.Meta{
		ID:        "sess1",
		AgentType: "claude",
		Status:    status.Status{Process: status.ProcessRunning},
	})
	c := dialClient(t, serveStub(t, stub), nil)
	return stub, c
}

// The label is sanitized server-side: embedded control characters (NUL, newline,
// tab) are stripped so a persisted/broadcast name is always a single printable line.
func TestRename_ForwardsSanitizedNameToDaemon(t *testing.T) {
	stub, c := renameStub(t)

	if err := c.Rename(NamespacedID(c.EndpointID(), "sess1"), "back\x00end\n-ref\tactor"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	got := stub.renames()
	if len(got) != 1 {
		t.Fatalf("DaemonAPI.Rename called %d times, want 1", len(got))
	}
	if got[0].id != "sess1" {
		t.Fatalf("Rename local id = %q, want sess1 (de-namespaced)", got[0].id)
	}
	if got[0].name != "backend-refactor" {
		t.Fatalf("Rename name = %q, want the sanitized single-line label", got[0].name)
	}
}

// An over-long label is capped to maxNameRunes server-side (a cosmetic field is
// truncated, never rejected — the rename never fails over a display value).
func TestRename_TruncatesOverlongNameServerSide(t *testing.T) {
	stub, c := renameStub(t)

	if err := c.Rename(NamespacedID(c.EndpointID(), "sess1"), strings.Repeat("x", 200)); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	got := stub.renames()
	if len(got) != 1 {
		t.Fatalf("Rename called %d times, want 1", len(got))
	}
	if n := len([]rune(got[0].name)); n != maxNameRunes {
		t.Fatalf("Rename name = %d runes, want capped to %d", n, maxNameRunes)
	}
}

// A daemon-side rename failure surfaces to the client — the same error path an older
// daemon's "unknown op" refusal takes, which the TUI banners (skew-safe).
func TestRename_DaemonErrorSurfacesToClient(t *testing.T) {
	stub, c := renameStub(t)
	stub.renameErr = errors.New("no such session")

	err := c.Rename(NamespacedID(c.EndpointID(), "sess1"), "new")
	if err == nil {
		t.Fatalf("Rename must surface the daemon error, got nil")
	}
	if !strings.Contains(err.Error(), "no such session") {
		t.Fatalf("Rename error = %q, want it to carry the daemon reason", err.Error())
	}
}

// A rename addressed to a FOREIGN endpoint's namespace is refused BEFORE any
// DaemonAPI call (E6.6/F-1) — the bad id never reaches the daemon.
func TestRename_ForeignEndpointRefusedBeforeDaemon(t *testing.T) {
	stub, c := renameStub(t)

	if err := c.Rename("other-ep/sess1", "new"); err == nil {
		t.Fatalf("Rename with a foreign endpoint id must be refused")
	}
	if got := stub.renames(); len(got) != 0 {
		t.Fatalf("DaemonAPI.Rename must NOT be called for an invalid id; got %v", got)
	}
}
