package skeleton

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/transcript"
)

func TestIdentityTailCapturesNewGenerationAtUnchangedSizeAndRetiresOnExit(t *testing.T) {
	const id = "ring-native"
	a := continuityAPI(t, persist.Meta{ID: id, AgentType: "reference", Cwd: t.TempDir(), Status: status.Status{Process: status.ProcessExited}})
	d := &Daemon{core: a.core, stateDir: a.stateDir}
	path := filepath.Join(a.stateDir, id, shim.IdentityTailFile)
	w, err := transcript.NewTail(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if _, err := w.Write(bytes.Repeat([]byte("x"), transcript.TailBytes)); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	d.captureConversationIDFinal(id)
	if m, _ := a.core.Get(id); m.ConversationID != "" {
		t.Fatal("invented identity before its marker")
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("\nconv-id=late-ring-id\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil || before.Size() != after.Size() {
		t.Fatalf("fixture did not keep a constant tail size: %v", err)
	}
	d.endSession(id)
	if m, _ := a.core.Get(id); m.ConversationID != "late-ring-id" {
		t.Fatalf("exit marker in new generation was lost: %q", m.ConversationID)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("known ended identity retained output evidence: %v", err)
	}
}

func TestIdentityTailRetriesQuietEvidenceAfterMetadataWriteFailure(t *testing.T) {
	const id = "ring-retry"
	a := continuityAPI(t, persist.Meta{ID: id, AgentType: "reference", Cwd: t.TempDir(), Status: status.Status{Process: status.ProcessExited}})
	d := &Daemon{core: a.core, stateDir: a.stateDir}
	dir := filepath.Join(a.stateDir, id)
	w, err := transcript.NewTail(filepath.Join(dir, shim.IdentityTailFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("conv-id=quiet-ring-id\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	meta := filepath.Join(dir, "meta.json")
	saved := filepath.Join(dir, "saved-meta")
	if err := os.Rename(meta, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(meta, 0700); err != nil {
		t.Fatal(err)
	}
	d.captureConversationIDFinal(id)
	if m, _ := a.core.Get(id); m.ConversationID != "" {
		t.Fatal("fixture did not cause an identity persistence failure")
	}
	if err := os.Remove(meta); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(saved, meta); err != nil {
		t.Fatal(err)
	}
	d.captureConversationIDFinal(id)
	if m, _ := a.core.Get(id); m.ConversationID != "quiet-ring-id" {
		t.Fatalf("unchanged evidence was not retried after disk recovery: %q", m.ConversationID)
	}
}
