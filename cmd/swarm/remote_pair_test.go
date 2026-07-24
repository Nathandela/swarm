package main

// FAILING-FIRST tests for slice A4-cli: the `swarm remote pair` CLI subcommand, built
// on the async pairing client API pinned in internal/protocol/client_pairing_test.go
// (Client.StartPairing + the PairingSession handle: QR/RendezvousID/ExpiresAt,
// Pending(), Confirm(allow), Result()).
//
// INTENDED PRODUCTION (RED — runRemotePair does not exist yet; GREEN implements it):
//
//	// runRemote gains a routed "pair" verb -> runRemotePair (dispatch passes os.Stdin).
//	//
//	// runRemotePair runs the owner side of pairing: dial the owner socket (dialClient,
//	// CapPairing), StartPairing, print the QR + rendezvous for the phone, block on
//	// Pending() to show the SAS (six emoji) + device name, read the operator's y/n from
//	// the INJECTED stdin reader (never os.Stdin directly — testable without a TTY),
//	// Confirm(allow), then block on Result() and print the terminal outcome. A declined
//	// or dropped/failed pairing is nonzero exit, fail closed.
//	func runRemotePair(args []string, stdin io.Reader, stdout, stderr io.Writer) int
//
// HARNESS: `swarm remote pair` must dial through dialClient -> daemon.EnsureDaemon,
// whose first act is the daemon version PROBE (a leading daemon.VersionProbeTag).
// skeleton.Serve's real daemon HOSTS pairing (coreAPI.BeginPairing) but only over a
// provisioned pairingConfig driving a REAL Noise handshake with a phone over a
// rendezvous transport — unscriptable from this package. So the least-effort harness
// that still exercises the REAL StartPairing round trip is a scripted fake owner
// daemon: a listener that DEMUXES the version probe exactly as internal/skeleton does,
// then feeds framed client connections to a real protocol.NewServer backed by a
// scriptedPairingHost. That drives the genuine pair_start-reply -> pair_pending-push ->
// pair_confirm -> pair_result-push wire contract without a phone or relay.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

// scriptedPairingHost is a minimal owner-tier daemon backend (protocol.DaemonAPI) that
// ALSO implements protocol.PairingHost with a SCRIPTED handshake: BeginPairing returns a
// canned rendezvous view, then from a background goroutine drives the SAS gate (a fixed
// six-emoji SAS + device name) through the passed-in confirm and reports a terminal
// result — success on an approving confirm, a failure (fail closed) otherwise. It records
// the capability it was asked to grant and the allow value confirm observed, so a test
// can prove the flag and the pair_confirm reached it over the wire.
type scriptedPairingHost struct {
	view       protocol.PairView
	sas        []string
	deviceName string
	pairedID   string
	pairedName string

	events    chan persist.Meta
	capSeen   chan string // req.Capability BeginPairing was called with (buffered cap 1)
	confirmed chan bool   // the allow value confirm observed (buffered cap 1)
}

func newScriptedPairingHost() *scriptedPairingHost {
	exp := time.Now().Add(2 * time.Minute)
	return &scriptedPairingHost{
		view:       protocol.PairView{QR: "otpauth://swarm-pair/CLI-abc123", RendezvousID: "rvz-cli-7", ExpiresAt: &exp},
		sas:        []string{"🍎", "🚗", "🐘", "🎸", "🌙", "🔑"},
		deviceName: "Nathan's iPhone",
		pairedID:   "devCLI",
		pairedName: "Nathan's iPhone",
		events:     make(chan persist.Meta),
		capSeen:    make(chan string, 1),
		confirmed:  make(chan bool, 1),
	}
}

// BeginPairing returns the canned view synchronously, then scripts the handshake in a
// goroutine: it calls confirm with the fixed SAS + device name, records the allow it
// saw, and reports exactly one result — success on (true, nil), failure otherwise.
func (h *scriptedPairingHost) BeginPairing(_ context.Context, req protocol.PairStartReq,
	confirm func(sas []string, deviceName string) (bool, error),
	result func(protocol.PairResult)) (protocol.PairView, error) {

	select {
	case h.capSeen <- req.Capability:
	default:
	}
	go func() {
		ok, err := confirm(h.sas, h.deviceName)
		select {
		case h.confirmed <- ok:
		default:
		}
		if ok && err == nil {
			result(protocol.PairResult{DeviceID: h.pairedID, Name: h.pairedName, Capability: req.Capability})
			return
		}
		reason := err
		if reason == nil {
			reason = errors.New("pairing declined")
		}
		result(protocol.PairResult{Err: reason})
	}()
	return h.view, nil
}

// DaemonAPI — minimal stubs; the pair test never drives session ops.
func (h *scriptedPairingHost) List() []persist.Meta { return nil }
func (h *scriptedPairingHost) Launch(daemon.LaunchSpec) (persist.Meta, error) {
	return persist.Meta{}, errors.New("scriptedPairingHost: launch not implemented")
}
func (h *scriptedPairingHost) Kill(string) error   { return errors.New("scriptedPairingHost: kill not implemented") }
func (h *scriptedPairingHost) Delete(string) error { return errors.New("scriptedPairingHost: delete not implemented") }
func (h *scriptedPairingHost) Attach(string) (protocol.SessionStream, error) {
	return nil, errors.New("scriptedPairingHost: attach not implemented")
}
func (h *scriptedPairingHost) Events() <-chan persist.Meta { return h.events }

// replayConn is a net.Conn whose first Read replays the demux prefix byte already
// consumed from the underlying connection, so the protocol Server reads the wire frame
// from its true first byte (mirrors internal/skeleton's prefixedConn).
type replayConn struct {
	net.Conn
	r io.Reader
}

func withPrefix(conn net.Conn, prefix ...byte) net.Conn {
	return &replayConn{Conn: conn, r: io.MultiReader(bytes.NewReader(prefix), conn)}
}

func (p *replayConn) Read(b []byte) (int, error) { return p.r.Read(b) }

// startFakePairingDaemon stands up the scripted fake owner daemon on
// <stateDir>/daemon.sock and points the SWARM_DAEMON_* environment dialClient reads at
// it. Its accept loop DEMUXES the first byte exactly as the assembled daemon does: a
// leading daemon.VersionProbeTag is EnsureDaemon's liveness probe (answered with the
// daemon protocol version so it never spawns a real daemon); a leading 0x00 is a framed
// protocol client, fed to a real protocol.Server backed by the host.
func startFakePairingDaemon(t *testing.T, stateDir string, host *scriptedPairingHost) {
	t.Helper()
	sock := filepath.Join(stateDir, "daemon.sock")
	t.Setenv(daemon.EnvStateDir, stateDir)
	t.Setenv(daemon.EnvSocket, sock)
	t.Setenv(daemon.EnvLock, filepath.Join(stateDir, "daemon.lock"))
	t.Setenv(daemon.EnvLog, filepath.Join(stateDir, "daemon.log"))

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen fake pairing daemon: %v", err)
	}
	srv := protocol.NewServer(host, "ep-fake-pair")

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed
			}
			go serveFakePairingConn(srv, conn)
		}
	}()

	t.Cleanup(func() {
		_ = ln.Close()
		_ = srv.Close()
	})
}

func serveFakePairingConn(srv *protocol.Server, conn net.Conn) {
	var tag [1]byte
	if _, err := io.ReadFull(conn, tag[:]); err != nil {
		conn.Close()
		return
	}
	switch tag[0] {
	case daemon.VersionProbeTag:
		defer conn.Close()
		var payload [4]byte
		if _, err := io.ReadFull(conn, payload[:]); err != nil {
			return
		}
		var out [4]byte
		binary.BigEndian.PutUint32(out[:], uint32(daemon.ProtocolVersion))
		_, _ = conn.Write(out[:])
	case 0x00:
		srv.ServeConn(withPrefix(conn, 0x00))
	default:
		conn.Close()
	}
}

// TestRemotePair_ShowsSASAndConfirms drives `swarm remote pair` against the scripted
// fake owner daemon with the operator CONFIRMING (feeding 'y' on the injected stdin). It
// asserts: the QR + rendezvous are shown (for the phone to scan); the six-emoji SAS +
// the device name are shown at the confirm gate; the approving pair_confirm reached the
// host over the wire; and the terminal "paired <device>" result prints (exit 0).
func TestRemotePair_ShowsSASAndConfirms(t *testing.T) {
	dir := shortStateDir(t)
	host := newScriptedPairingHost()
	startFakePairingDaemon(t, dir, host)

	var stdout, stderr bytes.Buffer
	exit := runRemotePair(nil, strings.NewReader("y\n"), &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("runRemotePair(confirm) exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	out := stdout.String()

	// The rendezvous view (QR + id) bootstraps the phone.
	if !strings.Contains(out, host.view.QR) {
		t.Errorf("pair output missing QR %q; got:\n%s", host.view.QR, out)
	}
	if !strings.Contains(out, host.view.RendezvousID) {
		t.Errorf("pair output missing rendezvous id %q; got:\n%s", host.view.RendezvousID, out)
	}

	// The SAS gate: the six emoji + the requesting device name.
	for _, e := range host.sas {
		if !strings.Contains(out, e) {
			t.Errorf("pair output missing SAS emoji %q; got:\n%s", e, out)
		}
	}
	if !strings.Contains(out, host.deviceName) {
		t.Errorf("pair output missing device name %q; got:\n%s", host.deviceName, out)
	}

	// sonnet#4: the confirm surface echoes the capability tier being granted, so the
	// operator sees the authority they are about to grant (default "full") BEFORE allowing,
	// not just the SAS/device name.
	if !strings.Contains(out, "Capability to grant: full") {
		t.Errorf("pair confirm output missing the granted-capability echo; got:\n%s", out)
	}

	// The confirm was SENT and approving: the host observed allow=true over the wire.
	select {
	case allow := <-host.confirmed:
		if !allow {
			t.Errorf("host confirm saw allow=false; want true (approving pair_confirm not sent?)")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("host never received a confirm (pair_confirm not sent over the wire?)")
	}

	// Flag parsing + wiring: the default capability tier reached the host.
	select {
	case cap := <-host.capSeen:
		if cap != "full" {
			t.Errorf("host BeginPairing capability = %q; want the default %q", cap, "full")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("host never received a pair_start (StartPairing not driven?)")
	}

	// The terminal paired result names the device.
	if !strings.Contains(out, "paired") || !strings.Contains(out, host.pairedName) {
		t.Errorf("pair output missing terminal paired result for %q; got:\n%s", host.pairedName, out)
	}
}

// TestRemotePair_DenyReportsDeclined drives `swarm remote pair` with the operator
// DENYING (feeding 'n'): Confirm(false) reaches the host, nothing is paired, and the CLI
// reports the pairing declined with a nonzero exit (fail closed).
func TestRemotePair_DenyReportsDeclined(t *testing.T) {
	dir := shortStateDir(t)
	host := newScriptedPairingHost()
	startFakePairingDaemon(t, dir, host)

	var stdout, stderr bytes.Buffer
	exit := runRemotePair(nil, strings.NewReader("n\n"), &stdout, &stderr)
	if exit == 0 {
		t.Fatalf("runRemotePair(deny) exit = 0; want nonzero (nothing paired, fail closed)")
	}
	combined := strings.ToLower(stdout.String() + stderr.String())
	if !strings.Contains(combined, "declined") {
		t.Errorf("declined pairing output = %q; want a 'declined' report", combined)
	}

	// The declining pair_confirm reached the host (allow=false).
	select {
	case allow := <-host.confirmed:
		if allow {
			t.Errorf("host confirm saw allow=true; want false (deny not sent over the wire?)")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("host never received a confirm on the deny path")
	}
}
