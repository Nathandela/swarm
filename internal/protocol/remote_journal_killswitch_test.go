package protocol

// FAILING-FIRST tests for re-audit FINDINGS B and C (they compose).
//
// FINDING B: the journal kill-switch gate is not remoteTier-scoped. distributeJournal,
// handleJournalRead and handleJournalSubscribe gate on remoteControlDisabled() with NO
// cc.srv.remoteTier check — unlike every other C1/C2a gate. Both the owner Server (d.srv,
// remoteTier=false) and the remote Server (d.remoteSrv, remoteTier=true) run over the SAME
// KillSwitch-implementing coreAPI, so `swarm remote off` wrongly gates the OWNER-tier journal
// too. It fails closed (over-restrictive) but is wrong: the owner tier must NEVER be gated.
//
// FINDING C: `off` leaves remote journal subscriptions silently armed. While off,
// distributeJournal just DROPS records without closing existing REMOTE journal subscribers;
// on re-enable the old subscription resumes mid-stream, silently missing the off-interval
// events with NO sequence gap (no envelope was generated while off). The disable transition
// must TERMINATE remote journal subscriber connections (like the lease/peek severance) so a
// re-subscribe performs a fresh journal_read (full resync). Remote-tier only.

import (
	"sync/atomic"
	"testing"
)

// TestProtocol_OwnerTierJournalNotKillSwitchGated (finding B): an OWNER-tier Server over a
// KillSwitch-implementing backend reporting DISABLED must STILL serve journal_read /
// journal_subscribe and fan out — the remote kill switch gates the remote tier only. Today the
// gate has no remoteTier check, so the owner tier is wrongly blocked with CodeKillSwitch.
func TestProtocol_OwnerTierJournalNotKillSwitchGated(t *testing.T) {
	stub := newOffJournalStub() // JournalBackend + KillSwitch reporting OFF
	sock := tmpSock(t)
	srv, err := Serve(stub, sock) // OWNER tier (Serve, not ServeRemote)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapJournal})

	// journal_read on the owner tier must succeed despite the shared backend's remote kill
	// switch being OFF (the kill switch gates the remote tier only).
	rc.writeControl(Control{Op: OpJournalRead, EndpointID: rep.EndpointID, Cursor: 0})
	if got := rc.readControl(); got.Op != OpJournalRead {
		t.Fatalf("owner-tier journal_read = op %q code %q; want journal_read (owner tier is never kill-switch-gated, finding B)", got.Op, got.ErrorCode)
	}

	// journal_subscribe likewise registers ...
	rc.writeControl(Control{Op: OpJournalSubscribe, EndpointID: rep.EndpointID})
	if got := rc.readControl(); got.Op != OpOK {
		t.Fatalf("owner-tier journal_subscribe = op %q code %q; want ok", got.Op, got.ErrorCode)
	}
	// ... and the owner-tier fan-out still delivers, despite the remote kill switch being off.
	stub.source <- JournalRecord{Cursor: 7, SessionID: "s1", Type: "launched"}
	if ev := rc.readControl(); ev.Op != OpJournalEvent || ev.Cursor != 7 {
		t.Fatalf("owner-tier journal fan-out = op %q cursor %d; want journal_event cursor 7 (fan-out must not be gated by the remote kill switch, finding B)", ev.Op, ev.Cursor)
	}
}

// remoteJournalStub is a remote-tier backend (DaemonAPI + DeviceAuthenticator + OperationClaimer
// via the embedded *stubDaemon) that ALSO implements JournalBackend, with a TOGGLEABLE kill
// switch so a test can flip `off` while a remote journal subscription is live.
type remoteJournalStub struct {
	*stubDaemon
	enabled atomic.Bool
	source  chan JournalRecord
}

func newRemoteJournalStub() *remoteJournalStub {
	s := &remoteJournalStub{stubDaemon: newStubDaemon(), source: make(chan JournalRecord, 16)}
	s.enabled.Store(true)
	return s
}

func (r *remoteJournalStub) RemoteControlEnabled() bool                    { return r.enabled.Load() }
func (r *remoteJournalStub) JournalReadFrom(uint64) (JournalResume, error) { return JournalResume{}, nil }
func (r *remoteJournalStub) JournalSubscribe() (<-chan JournalRecord, func()) {
	return r.source, func() {}
}

var (
	_ DaemonAPI      = (*remoteJournalStub)(nil)
	_ JournalBackend = (*remoteJournalStub)(nil)
	_ KillSwitch     = (*remoteJournalStub)(nil)
)

// TestProtocol_OffSeversRemoteJournalSubscriber (finding C): a live REMOTE journal subscriber,
// after off, has its connection TERMINATED so a re-subscribe on `on` performs a fresh
// journal_read (full resync) rather than silently resuming mid-stream. Today the disable
// transition leaves the subscription armed and the connection open.
func TestProtocol_OffSeversRemoteJournalSubscriber(t *testing.T) {
	stub := newRemoteJournalStub() // switch ON initially
	sock, srv := serveRemoteAPISrv(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapJournal})

	// Subscribe with the switch ON; a record fans out (the subscription is live).
	rc.writeControl(Control{Op: OpJournalSubscribe, EndpointID: rep.EndpointID})
	if got := rc.readControl(); got.Op != OpOK {
		t.Fatalf("journal_subscribe (switch on) = op %q code %q; want ok", got.Op, got.ErrorCode)
	}
	stub.source <- JournalRecord{Cursor: 1, SessionID: "s1", Type: "launched"}
	if ev := rc.readControl(); ev.Op != OpJournalEvent || ev.Cursor != 1 {
		t.Fatalf("live journal fan-out = op %q cursor %d; want journal_event cursor 1", ev.Op, ev.Cursor)
	}

	// OFF: the disable transition severs remote control AND terminates remote journal
	// subscribers, so `on` forces a fresh journal_read instead of a silent mid-stream resume.
	stub.enabled.Store(false)
	srv.SeverAllRemoteControl()

	if !rc.eventuallyClosed(recvTimeout) {
		t.Fatalf("off did not terminate the remote journal subscriber connection; its old subscription would silently resume mid-stream on `on`, missing the off-interval events with no sequence gap (finding C)")
	}
}
