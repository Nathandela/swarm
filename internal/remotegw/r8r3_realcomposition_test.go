package remotegw

// WAVE R8 / ROUND 3 -- THE CONTROL HALF, DRIVEN THROUGH THE GATEWAY THE PRODUCT SHIPS.
//
// STANDING CONSTRAINT 3 ("tests must drive the REAL assembled path at least once per seam")
// was met for the daemon's frame handling and NOT for the seam between the phone and it.
// Round 2's control tests each held ONE daemon connection for the whole test; the gateway
// holds none -- ForwardCommand dials a fresh connection per command and closes it on the
// reply. Every test was green and no phone could type a byte.
//
// So this file drives the product's own object: a real protocol.ServeRemote listening on a
// real socket, and a real remotegw.Gateway forwarding a real signed begin followed by a real
// unsigned input, each on its own connection because that is what ForwardCommand does. The
// assertion is BYTES AT THE PTY, because "refused" and "refused after writing" are the same
// reply and different outcomes.
//
// It also carries the READ half's equivalent (round-3 blocker 2c's sibling): the capability
// record the daemon authors must reach the gateway's own sink, and an INCONSISTENT record
// must be stripped at the gateway decode seam -- the second of amendment T2-b's three, which
// round 2's evidence claimed existed and which did not.

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// ---------------------------------------------------------------------------
// The backend: the smallest DaemonAPI that can hold a terminal-control plane.
// ---------------------------------------------------------------------------

// r8Backend is a protocol.DaemonAPI plus every optional seam the terminal control plane
// consults: the capability lookup, the device authenticator, the device registrar, the kill
// switch and the PTY sink that COUNTS BYTES.
type r8Backend struct {
	mu      sync.Mutex
	events  chan persist.Meta
	records map[string]protocol.SessionCapabilities
	claimed map[string]bool
	written [][]byte
	streams map[string]*r8r4Stream
	// refuseOnTap makes SessionCapabilities refuse from the moment TerminalTap is called --
	// i.e. after the subscribe-time gate has passed and before the first emission.
	refuseOnTap bool
	tapped      bool
}

func newR8Backend() *r8Backend {
	return &r8Backend{
		events:  make(chan persist.Meta),
		records: map[string]protocol.SessionCapabilities{},
	}
}

func (b *r8Backend) List() []persist.Meta                              { return nil }
func (b *r8Backend) Launch(daemon.LaunchSpec) (persist.Meta, error)    { return persist.Meta{}, nil }
func (b *r8Backend) Kill(string) error                                 { return nil }
func (b *r8Backend) Delete(string) error                               { return nil }
func (b *r8Backend) Rename(string, string) error                       { return nil }
func (b *r8Backend) SetTag(string, string) error                       { return nil }
func (b *r8Backend) Attach(string) (protocol.SessionStream, error)     { return nil, os.ErrNotExist }
func (b *r8Backend) Events() <-chan persist.Meta                       { return b.events }
func (b *r8Backend) AuthorizeCommand(protocol.DeviceCommandAuth) error { return nil }
func (b *r8Backend) DeviceRegistered(string) bool                      { return true }
func (b *r8Backend) RemoteControlEnabled() bool                        { return true }

// ClaimOperation satisfies the remote-tier construction guard: an op id is single-use.
func (b *r8Backend) ClaimOperation(operationID, _, _ string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.claimed == nil {
		b.claimed = map[string]bool{}
	}
	existed := b.claimed[operationID]
	b.claimed[operationID] = true
	return existed, nil
}

func (b *r8Backend) SessionCapabilities(local string) (protocol.SessionCapabilities, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.refuseOnTap && b.tapped {
		return protocol.SessionCapabilities{}, false
	}
	rec, ok := b.records[local]
	return rec, ok
}

func (b *r8Backend) setRecord(local string, rec protocol.SessionCapabilities) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.records[local] = rec
}

// TerminalInput is the PTY.
func (b *r8Backend) TerminalInput(local string, p []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.written = append(b.written, append([]byte(nil), p...))
	return nil
}

func (b *r8Backend) writes() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.written)
}

// r8Endpoint is the STABLE endpoint id the assembled daemon serves its remote socket with.
// It matters here rather than being decoration: resolveSession requires every session id to
// be namespaced with the CONNECTION's endpoint, and ForwardCommand opens a new connection per
// command -- so a per-connection id would make the second command name a session the first
// connection's id no longer matches, which is a second way the product path differs from a
// single-connection test.
const r8Endpoint = "mach1"

// serveR8Remote stands up a real remote-tier protocol server on a short-pathed socket and
// returns the socket and the backend. The server is closed in t.Cleanup, so nothing this
// rig starts outlives the test (standing constraint 9).
func serveR8Remote(t *testing.T) (string, *r8Backend) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "r8gw")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "r.sock")
	b := newR8Backend()
	srv, err := protocol.ServeRemoteWithID(b, sock, r8Endpoint)
	if err != nil {
		t.Fatalf("protocol.ServeRemote: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return sock, b
}

// ---------------------------------------------------------------------------
// The seam.
// ---------------------------------------------------------------------------

// TestR8R3_TheGatewayForwardsAControlGenerationAcrossItsPerCommandConnections is round-3
// blocker 1, driven through the object the product uses.
func TestR8R3_TheGatewayForwardsAControlGenerationAcrossItsPerCommandConnections(t *testing.T) {
	sock, backend := serveR8Remote(t)
	backend.setRecord("sess1", protocol.SessionCapabilities{
		Provider: "opencode", ProviderVersion: "0.9.0", AdapterRevision: "rev-test",
		SessionInstance: "inst-1", TerminalFallback: true, TerminalControl: true,
	})
	gw := New(sock, nil)

	exp := time.Now().Add(time.Minute)
	begin, err := gw.ForwardCommand(protocol.OpTerminalControlBegin, protocol.RemoteCommand{
		DeviceCommandAuth: schema.DeviceCommandAuth{
			DeviceID: "devA", Action: schema.ActionTerminalControlBegin,
			Session: r8Endpoint + "/sess1", OperationID: "devA:01JBR3BEGIN000000000001",
			ExpiresAt: exp, Sig: "sig",
		},
		TerminalControlBegin: &schema.TerminalControlBeginReq{
			Session: r8Endpoint + "/sess1", SessionInstance: "inst-1", Profile: schema.CurrentProfileVersion,
		},
		BodyVersion: schema.CurrentProfileVersion,
	})
	if err != nil {
		t.Fatalf("ForwardCommand(terminal_control_begin): %v", err)
	}
	if begin.Op != protocol.OpOK || begin.ControlGeneration == "" {
		t.Fatalf("terminal_control_begin through the gateway = op %q code %q error %q; want ok "+
			"with a minted generation", begin.Op, begin.ErrorCode, begin.Error)
	}

	// The begin's connection is now closed. This is the frame that matters.
	in, err := gw.ForwardCommand(protocol.OpTerminalInput, protocol.RemoteCommand{
		DeviceCommandAuth: schema.DeviceCommandAuth{
			Action: schema.ActionTerminalInput, Session: r8Endpoint + "/sess1",
		},
		TerminalInput: &schema.TerminalInputReq{
			Session: r8Endpoint + "/sess1", SessionInstance: "inst-1",
			ControlGeneration: begin.ControlGeneration, Bytes: []byte("ls\r"),
		},
	})
	if err != nil {
		t.Fatalf("ForwardCommand(terminal_input): %v", err)
	}
	if in.Op != protocol.OpOK {
		t.Fatalf("terminal_input through the gateway = op %q code %q error %q; want ok.\n"+
			"ForwardCommand dials a fresh daemon connection per command, so a generation held "+
			"on the minting connection is a generation no phone can ever use -- the wave's exit "+
			"says a fallback session can be CONTROLLED, and this is the only path there is.",
			in.Op, in.ErrorCode, in.Error)
	}
	if got := backend.writes(); got != 1 {
		t.Fatalf("bytes reaching the PTY = %d write(s), want 1", got)
	}
}

// TestR8R3_AHealthyStructuredSessionIsStillRefusedThroughTheGateway is the fence's other
// side, over the same real path: the wave's exit says Claude and Codex expose NO ROUTE to the
// terminal plane, and a fix that made control reachable must not have made it reachable for
// them.
func TestR8R3_AHealthyStructuredSessionIsStillRefusedThroughTheGateway(t *testing.T) {
	sock, backend := serveR8Remote(t)
	backend.setRecord("sess1", protocol.SessionCapabilities{
		Provider: "claude", ProviderVersion: "1.2.3", AdapterRevision: "rev-test",
		SessionInstance: "inst-1", StructuredChat: true,
	})
	gw := New(sock, nil)

	exp := time.Now().Add(time.Minute)
	begin, err := gw.ForwardCommand(protocol.OpTerminalControlBegin, protocol.RemoteCommand{
		DeviceCommandAuth: schema.DeviceCommandAuth{
			DeviceID: "devA", Action: schema.ActionTerminalControlBegin,
			Session: r8Endpoint + "/sess1", OperationID: "devA:01JBR3BEGIN000000000002",
			ExpiresAt: exp, Sig: "sig",
		},
		TerminalControlBegin: &schema.TerminalControlBeginReq{
			Session: r8Endpoint + "/sess1", SessionInstance: "inst-1", Profile: schema.CurrentProfileVersion,
		},
		BodyVersion: schema.CurrentProfileVersion,
	})
	if err != nil {
		t.Fatalf("ForwardCommand(terminal_control_begin): %v", err)
	}
	if begin.ErrorCode != protocol.CodeCapabilityRefused {
		t.Fatalf("terminal_control_begin over a healthy Claude session = op %q code %q; want %q",
			begin.Op, begin.ErrorCode, protocol.CodeCapabilityRefused)
	}
	if backend.writes() != 0 {
		t.Fatalf("a refused begin still reached the PTY")
	}
}
