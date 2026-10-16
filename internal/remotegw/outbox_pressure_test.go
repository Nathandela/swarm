package remotegw

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestOutboxUncertainEnvelopeSurvivesUnrelatedCommitPressureAndRestart(t *testing.T) {
	dir := t.TempDir()
	seqPath := filepath.Join(dir, "outbound.seq")
	outboxPath := filepath.Join(dir, "outbound.outbox")
	lost := &commitThenFailAppender{}
	sink, outbox := outboxTestSink(t, lost, seqPath, outboxPath)
	if err := sink.Event(protocol.JournalRecord{Cursor: 1, SessionID: "machine/session", Type: "launched"}); err == nil {
		t.Fatal("fixture did not retain an uncertain delivery")
	}
	sealed := lost.all()[0]
	for cursor := uint64(2); cursor <= 32; cursor++ {
		if err := outbox.Reserve(cursor, []byte("unrelated envelope")); err != nil {
			t.Fatal(err)
		}
		if err := outbox.Commit(cursor); err != nil {
			t.Fatal(err)
		}
	}
	appender := &countingAppender{}
	restarted, recovered := outboxTestSink(t, appender, seqPath, outboxPath)
	pending, err := recovered.Pending()
	if err != nil || len(pending) != 1 || pending[0].Cursor != 1 || !bytes.Equal(pending[0].Envelope, sealed) {
		t.Fatalf("uncertain delivery lost to unrelated commits: %+v err=%v", pending, err)
	}
	if err := restarted.Replay(); err != nil {
		t.Fatal(err)
	}
	if sent := appender.all(); len(sent) != 1 || !bytes.Equal(sent[0], sealed) {
		t.Fatal("restart recreated the uncertain envelope instead of replaying its exact bytes")
	}
	if recovered.Cursor() != 32 {
		t.Fatal("replaying an older pending entry regressed the committed cursor")
	}
	if err := restarted.Replay(); err != nil || appender.count() != 1 {
		t.Fatalf("settled delivery was replayed again: count=%d err=%v", appender.count(), err)
	}
}
