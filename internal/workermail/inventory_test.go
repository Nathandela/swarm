package workermail

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectPendingRequiresOriginalMailbox(t *testing.T) {
	dir := t.TempDir()
	b := Binding{Discussion: "discussion", Parent: "manager", Session: "worker", Instance: "instance", Attempt: "attempt"}
	if _, err := Submit(dir, b, "note", "complete", "fixture", true); err != nil {
		t.Fatal(err)
	}
	pending, err := InspectPending(context.Background(), dir)
	if err != nil || len(pending) != 1 || pending[0].ID != "note" || pending[0].Binding != b {
		t.Fatalf("original mailbox: %+v, %v", pending, err)
	}
	other := filepath.Join(t.TempDir(), "dispatcher-mail.db")
	if err := os.Symlink(filepath.Join(dir, "dispatcher-mail.db"), other); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectPending(context.Background(), filepath.Dir(other)); err == nil {
		t.Fatal("redirected receipts attributed to the wrong supervisor")
	}
}
