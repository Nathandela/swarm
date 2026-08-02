// FAILING-FIRST (RED) tests for slice "Daemon DHI-1": the ASSEMBLED daemon must
// actually SERVE the remote journal ops. These exercise the REAL production path —
// the coreAPI adapter (skeleton/api.go) over a real *daemon.Daemon, served on a
// dedicated REMOTE-tier socket via protocol.ServeRemote — NOT a journalStub and NOT
// a direct d.JournalReadFrom call. That is exactly the path DHI-1
// (docs/verification/remote-phase1-daemon-review.md) says is broken and invisible to
// every existing test.
//
// WHY THEY FAIL TODAY (DHI-1):
//   - protocol.JournalBackend (remote.go:50-53) requires BOTH JournalReadFrom AND
//     JournalSubscribe on the DaemonAPI; the server type-asserts it at server.go:1056
//     and, when it fails, answers "journal not supported by this daemon"
//     (server.go:1058).
//   - *daemon.Daemon implements JournalReadFrom (daemon/journal.go:15) but has NO
//     JournalSubscribe, and the internal/journal Journal has Append + ReadFrom but no
//     subscribe/fan-out.
//   - coreAPI (skeleton/api.go:39-66) forwards NEITHER journal method, so the
//     assembled DaemonAPI does not satisfy protocol.JournalBackend at all — every
//     journal_read / journal_subscribe on the real binary is refused.
//
// So both tests below reach the "journal not supported by this daemon" refusal
// (behavioral RED) rather than real journal data / a live journal_event.
//
// CONTRACT THE IMPLEMENTER MUST ADD to turn these GREEN (do NOT change these tests):
//
//	// internal/journal: a live subscribe/fan-out alongside Append + ReadFrom.
//	// Append must deliver each newly-appended Record to every live subscriber.
//	func (j *Journal) Subscribe() (<-chan journal.Record, func()) // single source per subscriber; cancel via the returned func
//
//	// internal/daemon: expose the fan-out on the daemon, converting to the wire type.
//	func (d *Daemon) JournalSubscribe() (<-chan protocol.JournalRecord, func())
//	// (or return a daemon/journal record type and let coreAPI convert — see below.)
//
//	// internal/skeleton coreAPI: forward BOTH journal methods so the assembled
//	// DaemonAPI satisfies protocol.JournalBackend (remote.go:50-53):
//	func (a *coreAPI) JournalReadFrom(from uint64) (protocol.JournalResume, error)
//	func (a *coreAPI) JournalSubscribe() (<-chan protocol.JournalRecord, func())
//	// JournalReadFrom forwards to a.core.JournalReadFrom(from) and converts
//	// journal.Resume -> protocol.JournalResume (Events []journal.Record ->
//	// []protocol.JournalRecord{Cursor, SessionID, Type, Group}). JournalSubscribe
//	// forwards to the daemon fan-out, converting journal.Record -> protocol.JournalRecord.
//	// var _ protocol.JournalBackend = (*coreAPI)(nil)  // must hold once wired.
//
// Wire/types the contract rides on (already frozen, protocol/remote.go +
// protocol/types.go): protocol.JournalRecord{Cursor,SessionID,Type,Group},
// protocol.JournalResume{Cursor,Events,FullResync}, Control.Journal / Control.Cursor
// / Control.FullResync carriers, ops OpJournalRead/OpJournalSubscribe/OpJournalEvent,
// cap CapJournal.

package skeleton

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/wire"
)

// serveRemoteAssembled stands up a REMOTE-tier Server over the ASSEMBLED coreAPI
// (the real production DaemonAPI adapter wrapping a real *daemon.Daemon), on its own
// dedicated socket — the amendment D.0-A1 remote.sock the gateway dials. This is the
// end-to-end path DHI-1 says cannot serve journal ops.
func serveRemoteAssembled(t *testing.T, sk *Daemon) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "swsk-rem") // short path keeps sun_path under the 104-byte limit
	if err != nil {
		t.Fatalf("remote sock dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "r.sock")
	srv, err := protocol.ServeRemote(sk.api, sock) // sk.api is the real *coreAPI
	if err != nil {
		t.Fatalf("ServeRemote over the assembled coreAPI: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// rawRemote is a minimal remote-tier client built directly on the frozen wire +
// control types (protocol.Client exposes no journal ops), so these tests drive the
// journal_read / journal_subscribe / journal_event surface over the assembled remote
// socket.
type rawRemote struct {
	t          *testing.T
	conn       net.Conn
	endpointID string
}

func dialRemote(t *testing.T, sock string, caps ...string) *rawRemote {
	t.Helper()
	conn, err := net.DialTimeout("unix", sock, 5*time.Second)
	if err != nil {
		t.Fatalf("dial remote sock: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	r := &rawRemote{t: t, conn: conn}
	r.write(protocol.Control{Op: protocol.OpHello, ProtocolVersion: protocol.Version, Capabilities: caps})
	rep := r.read(5 * time.Second)
	if rep.Op != protocol.OpHello {
		t.Fatalf("remote hello reply op = %q; want %q (reply %+v)", rep.Op, protocol.OpHello, rep)
	}
	r.endpointID = rep.EndpointID
	return r
}

func (r *rawRemote) write(c protocol.Control) {
	r.t.Helper()
	body, err := protocol.EncodeControl(c)
	if err != nil {
		r.t.Fatalf("encode control: %v", err)
	}
	if err := wire.WriteFrame(r.conn, wire.TControl, body); err != nil {
		r.t.Fatalf("write frame: %v", err)
	}
}

// read blocks for the next control frame within `within`, fataling on any error.
func (r *rawRemote) read(within time.Duration) protocol.Control {
	r.t.Helper()
	c, err := r.readTry(within)
	if err != nil {
		r.t.Fatalf("read control frame: %v", err)
	}
	return c
}

// readTry returns the next control frame or an error (e.g. a read deadline), without
// fataling — for polling loops that scan a stream for a particular op.
func (r *rawRemote) readTry(within time.Duration) (protocol.Control, error) {
	_ = r.conn.SetReadDeadline(time.Now().Add(within))
	typ, payload, err := wire.ReadFrame(r.conn)
	if err != nil {
		return protocol.Control{}, err
	}
	if typ != wire.TControl {
		return protocol.Control{}, fmt.Errorf("frame type = %d; want a control frame", typ)
	}
	return protocol.DecodeControl(payload)
}

// TestSkeleton_RemoteJournalReadReturnsRealJournalData (DHI-1): a journal_read from a
// remote-tier client over the ASSEMBLED path returns real journal data recorded by
// the daemon (a launched session is a journalworthy transition, journalRecordFor ->
// launched), NOT the "journal not supported by this daemon" refusal.
//
// RED today: coreAPI forwards no JournalReadFrom, so the assembled DaemonAPI does not
// satisfy protocol.JournalBackend and the server refuses with OpError.
func TestSkeleton_RemoteJournalReadReturnsRealJournalData(t *testing.T) {
	sk := assemble(t)
	// C2a precondition: journal is now kill-switch-gated (refused CodeKillSwitch when
	// RemoteControlEnabled()==false), so a device must be paired to turn remote control ON before
	// journal serves — the same switch-on precondition the peek's E2E establishes (phonesim). This
	// only sets the precondition the new security contract requires; the journal-plumbing assertion
	// below is unchanged (a paired phone is also the realistic state in which a phone reads journal).
	registerPhone(t, sk, device.CapFull)
	// A journalworthy transition: launching a session appends a `launched` record to
	// the daemon's durable journal (daemon/journal.go journalRecordFor).
	launchFake(t, sk, "print HELLO\nidle 60s\n")

	sock := serveRemoteAssembled(t, sk)
	rc := dialRemote(t, sock, protocol.CapRemoteGateway, protocol.CapJournal)

	deadline := time.Now().Add(10 * time.Second)
	for {
		rc.write(protocol.Control{Op: protocol.OpJournalRead, EndpointID: rc.endpointID, Cursor: 0})
		got := rc.read(10 * time.Second)
		if got.Op == protocol.OpError {
			t.Fatalf("journal_read over the assembled remote path was REFUSED: %q / code=%q\n"+
				"DHI-1: coreAPI (skeleton/api.go) does not forward JournalReadFrom, so the assembled "+
				"DaemonAPI does not satisfy protocol.JournalBackend and the server answers "+
				"\"journal not supported by this daemon\". Fix: add coreAPI.JournalReadFrom + "+
				"coreAPI.JournalSubscribe forwarders.", got.Error, got.ErrorCode)
		}
		if got.Op != protocol.OpJournalRead {
			t.Fatalf("journal_read reply op = %q; want %q", got.Op, protocol.OpJournalRead)
		}
		if len(got.Journal) > 0 {
			// Real journal data flowed end-to-end from the daemon's durable journal.
			rec := got.Journal[0]
			if rec.Type == "" {
				t.Fatalf("journal_read record carried an empty Type; want a real transition kind")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("journal_read returned no records after a journalworthy launch; " +
				"want at least the `launched` transition from the daemon journal")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestSkeleton_RemoteJournalSubscribeStreamsLiveEvent (DHI-1): a journal_subscribe
// from a remote-tier client over the ASSEMBLED path is acked (not refused) and then
// receives a live journal_event for a SUBSEQUENT journalworthy transition, streamed
// through the daemon journal fan-out — NOT the "journal not supported" refusal.
//
// RED today: coreAPI forwards no JournalSubscribe and *daemon.Daemon has no
// JournalSubscribe (no journal fan-out exists), so the assembled DaemonAPI does not
// satisfy protocol.JournalBackend and journal_subscribe is refused with OpError.
//
// ISOLATION NOTE: run with -run TestSkeleton_RemoteJournal to get a clean signal. The
// unrelated wedged-eviction journal-subscribe test in internal/protocol
// (TestProtocol_JournalSubscribeOrderedAndEvictsWedged) is CPU-scheduling sensitive
// under parallel load; this test does not share that property.
func TestSkeleton_RemoteJournalSubscribeStreamsLiveEvent(t *testing.T) {
	sk := assemble(t)
	// C2a precondition: journal_subscribe is now kill-switch-gated, so pair a device to turn remote
	// control ON before subscribing (mirrors the peek E2E's switch-on precondition; the streaming
	// assertion below is unchanged).
	registerPhone(t, sk, device.CapFull)
	sock := serveRemoteAssembled(t, sk)
	rc := dialRemote(t, sock, protocol.CapRemoteGateway, protocol.CapJournal)

	// Register the subscriber BEFORE the transition, so there is no read/subscribe gap.
	rc.write(protocol.Control{Op: protocol.OpJournalSubscribe, EndpointID: rc.endpointID})
	ack := rc.read(10 * time.Second)
	if ack.Op == protocol.OpError {
		t.Fatalf("journal_subscribe over the assembled remote path was REFUSED: %q / code=%q\n"+
			"DHI-1: coreAPI forwards no JournalSubscribe and *daemon.Daemon has no JournalSubscribe "+
			"(no internal/journal fan-out), so the assembled DaemonAPI does not satisfy "+
			"protocol.JournalBackend. Fix: add journal.Subscribe fan-out + daemon.JournalSubscribe + "+
			"coreAPI.JournalSubscribe forwarder.", ack.Error, ack.ErrorCode)
	}
	if ack.Op != protocol.OpOK {
		t.Fatalf("journal_subscribe ack op = %q; want %q", ack.Op, protocol.OpOK)
	}

	// A SUBSEQUENT journalworthy transition: a fresh launch appends a `launched`
	// record, which must live-stream to the registered subscriber as a journal_event.
	launchFake(t, sk, "print WORLD\nidle 60s\n")

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		ev, err := rc.readTry(2 * time.Second)
		if err != nil {
			continue // no frame yet within this slice; keep waiting up to the deadline
		}
		if ev.Op == protocol.OpJournalEvent {
			if len(ev.Journal) == 0 {
				t.Fatalf("journal_event carried no record; want the live journal record")
			}
			return // a live journal record streamed end-to-end through the daemon fan-out
		}
		// Ignore any other control frame and keep scanning for the journal_event.
	}
	t.Fatal("no journal_event streamed to the remote subscriber after a journalworthy launch " +
		"(DHI-1: the assembled daemon has no journal fan-out to stream live records)")
}
