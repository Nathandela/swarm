// Tests for the FILE-BACKED InboundState (PB-GW-1). The PB-GW-1/-3/-4 suites drive the
// bridge through an in-memory fake, so nothing there exercises the durable custody the
// production gateway actually wires (cmd/swarm-remote/config.go). These cover it directly:
// the round trip across a reopen, the monotonic merge that makes a regressed write
// harmless, and the fail-closed refusal of a checkpoint that cannot be trusted.
package remotegw

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

func TestInboundState_RoundTripsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbound-state.json")
	st, err := OpenInboundState(path, "machine-1")
	if err != nil {
		t.Fatalf("OpenInboundState: %v", err)
	}
	s7 := InboundStream{Sender: [8]byte{1, 2, 3}, Epoch: 7}
	s8 := InboundStream{Sender: [8]byte{1, 2, 3}, Epoch: 8}
	if err := st.Save(InboundCheckpoint{Cursor: 42, Highest: map[InboundStream]uint64{s7: 61, s8: 1}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened, err := OpenInboundState(path, "machine-1")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := reopened.Load()
	if got.Cursor != 42 {
		t.Fatalf("reopened Cursor = %d, want 42", got.Cursor)
	}
	if got.Highest[s7] != 61 || got.Highest[s8] != 1 {
		t.Fatalf("reopened Highest = %v, want per-(sender,epoch) 61 and 1", got.Highest)
	}
}

// TestInboundState_NeverLowersAHighWater: custody merges monotonically. A regressed write
// -- a stale in-memory bridge, a reordered save -- must not be able to re-open a seq
// already consumed, which is the entire point of the persisted guard.
func TestInboundState_NeverLowersAHighWater(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbound-state.json")
	st, err := OpenInboundState(path, "machine-1")
	if err != nil {
		t.Fatalf("OpenInboundState: %v", err)
	}
	s := InboundStream{Epoch: 7}
	if err := st.Save(InboundCheckpoint{Cursor: 10, Highest: map[InboundStream]uint64{s: 61}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := st.Save(InboundCheckpoint{Cursor: 3, Highest: map[InboundStream]uint64{s: 5}}); err != nil {
		t.Fatalf("regressed Save: %v", err)
	}
	got := st.Load()
	if got.Cursor != 10 || got.Highest[s] != 61 {
		t.Fatalf("after a regressed Save, checkpoint = %+v, want Cursor 10 / high-water 61", got)
	}
}

// TestInboundState_CorruptFileFailsClosed: an unreadable checkpoint is an error at open,
// never a silent reset. Starting from an empty checkpoint would leave the restarted
// receiver's staleness check skipped on the first frame of every stream, re-opening every
// frame the relay still retains.
func TestInboundState_CorruptFileFailsClosed(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"garbage.json":   "not json at all",
		"unversioned":    `{"cursor":1,"streams":[]}`,
		"badsender.json": `{"schema_version":1,"machine":"machine-1","cursor":1,"streams":[{"sender":"zz","epoch":7,"seq":3}]}`,
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if _, err := OpenInboundState(path, "machine-1"); !errors.Is(err, errCorruptInboundState) {
			t.Fatalf("OpenInboundState(%s) err = %v, want errCorruptInboundState", name, err)
		}
	}
}

// TestInboundState_MissingFileAndEmptyPath: a first run has no file (empty checkpoint, no
// error), and an empty path is the non-durable in-memory default NewCommandBridge falls
// back to, mirroring OpenSeqSource.
func TestInboundState_MissingFileAndEmptyPath(t *testing.T) {
	fresh, err := OpenInboundState(filepath.Join(t.TempDir(), "absent.json"), "machine-1")
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if ck := fresh.Load(); ck.Cursor != 0 || len(ck.Highest) != 0 {
		t.Fatalf("first-run checkpoint = %+v, want empty", ck)
	}

	mem, err := OpenInboundState("", "machine-1")
	if err != nil {
		t.Fatalf("in-memory: %v", err)
	}
	s := InboundStream{Epoch: 7}
	if err := mem.Save(InboundCheckpoint{Cursor: 4, Highest: map[InboundStream]uint64{s: 9}}); err != nil {
		t.Fatalf("in-memory Save: %v", err)
	}
	if ck := mem.Load(); ck.Cursor != 4 || ck.Highest[s] != 9 {
		t.Fatalf("in-memory checkpoint = %+v, want it held in memory", ck)
	}
}

// TestInboundState_StaleIdentityDiscardsHighWater: both checkpoint coordinates are
// meaningless outside the identity that produced them, so a file left by a DIFFERENT
// machine identity must be discarded, never reused.
//
// machineid.Generate starts at EpochID 1 and `swarm remote init` regenerates the identity
// whenever machine.key is absent WITHOUT touching its siblings in <stateDir>/remote. The
// surviving inbound-state.json then carries an epoch-1 high-water of N -- one per keystroke
// the previous phone ever sent -- so the freshly paired phone's first N frames,
// take_control included, are stale-dropped with no error surfaced anywhere. That is a
// permanent, silent, self-inflicted denial of service, and a REGRESSION: the pre-durable
// in-memory guard reset on restart and self-healed.
func TestInboundState_StaleIdentityDiscardsHighWater(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbound-state.json")
	before, err := OpenInboundState(path, "machine-before-init")
	if err != nil {
		t.Fatalf("OpenInboundState(before): %v", err)
	}
	s := InboundStream{Sender: [8]byte{9, 9}, Epoch: 1}
	if err := before.Save(InboundCheckpoint{Cursor: 4200, Highest: map[InboundStream]uint64{s: 4200}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	after, err := OpenInboundState(path, "machine-after-init")
	if err != nil {
		t.Fatalf("OpenInboundState(after): %v", err)
	}
	if ck := after.Load(); len(ck.Highest) != 0 {
		t.Fatalf("checkpoint of a DIFFERENT machine identity = %+v, want an empty high-water map "+
			"(a reused epoch-1 high-water stale-drops the freshly paired phone's first frames forever)", ck)
	}

	// The new identity's own first Save must re-stamp the file, so the stale high-water
	// cannot come back through the monotonic merge on the next restart either.
	if err := after.Save(InboundCheckpoint{Cursor: 1, Highest: map[InboundStream]uint64{s: 1}}); err != nil {
		t.Fatalf("Save(after): %v", err)
	}
	reopened, err := OpenInboundState(path, "machine-after-init")
	if err != nil {
		t.Fatalf("reopen(after): %v", err)
	}
	if got := reopened.Load().Highest[s]; got != 1 {
		t.Fatalf("high-water after the new identity re-stamped the file = %d, want 1", got)
	}
}

// TestInboundState_StaleIdentityDiscardsCursor: the persisted cursor is the ONLY inbound
// progress pointer, so bound to the wrong identity it silently outruns the mailbox it
// indexes. If the relay's store is reset (reprovision, restore-from-backup) or the routing
// id changes, the machine's mailbox restarts at cursor 1; a read past the end returns no
// items AND NO ERROR, so the gateway is deaf forever with nothing an operator can observe.
func TestInboundState_StaleIdentityDiscardsCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbound-state.json")
	before, err := OpenInboundState(path, "routing-id-before")
	if err != nil {
		t.Fatalf("OpenInboundState(before): %v", err)
	}
	if err := before.Save(InboundCheckpoint{Cursor: 900, Highest: map[InboundStream]uint64{}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	after, err := OpenInboundState(path, "routing-id-after")
	if err != nil {
		t.Fatalf("OpenInboundState(after): %v", err)
	}
	if ck := after.Load(); ck.Cursor != 0 {
		t.Fatalf("cursor of a DIFFERENT machine identity = %d, want 0 "+
			"(a cursor past the end of a restarted mailbox reads nothing, forever, with no error)", ck.Cursor)
	}
}

// readFailingMailbox fails every read, the cheapest stand-in for the class of poll failure
// an operator must be able to see (a full or read-only state dir fails the checkpoint
// persist instead, which fail-closed DROPS the frame -- same silence).
type readFailingMailbox struct{}

var errPollFailed = errors.New("relay unreachable")

func (readFailingMailbox) MailboxRead(context.Context, uint64) ([]relay.Item, error) {
	return nil, errPollFailed
}

// MailboxWait fails too: a wait IS a read on this seam, and this fake exists to be
// the cheapest stand-in for "every inbound fetch fails".
func (readFailingMailbox) MailboxWait(context.Context, uint64) ([]relay.Item, bool, error) {
	return nil, false, errPollFailed
}
func (readFailingMailbox) MailboxAppend(context.Context, string, []byte) (uint64, error) {
	return 0, nil
}
func (readFailingMailbox) MailboxAck(context.Context, uint64) error { return nil }

// TestCommandBridge_RunSurfacesPollError: Run discards PollOnce's error entirely, so a
// gateway that drops every inbound frame -- a failed checkpoint persist DROPS live input by
// design (PB-GW-3) -- does it with no log, no metric, no signal at all. Err() stashes the
// first one, the idiom RelaySink already uses for its append failures.
func TestCommandBridge_RunSurfacesPollError(t *testing.T) {
	b := NewCommandBridge(CommandBridgeConfig{Mailbox: readFailingMailbox{}})
	if err := b.Err(); err != nil {
		t.Fatalf("Err() before any poll = %v, want nil", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _ = b.Run(ctx) }()

	waitFor(t, func() bool { return b.Err() != nil }, 2*time.Second,
		"Run swallowed the poll error: a gateway dropping every inbound frame must be observable")
	if err := b.Err(); !errors.Is(err, errPollFailed) {
		t.Fatalf("Err() = %v, want the poll error %v", err, errPollFailed)
	}
	cancel()
	<-done
}
