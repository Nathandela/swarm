package skeleton

import (
	"context"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/managerproof"
)

func TestManagerFacadeDerivesBridgeAndBindingFromNativeCustody(t *testing.T) {
	d, b, feed := newCodexManagerSendRig(t)
	status, err := d.managerRuntime(b.Session, b.SessionInstance)
	if err != nil {
		t.Fatal(err)
	}
	if status.Binding != b || !status.ReadyForInput || status.BridgeSocket == "" || status.AuthFile == "" {
		t.Fatalf("native owner status lost custody: %+v", status)
	}
	changed := b
	changed.LoginEpoch = "caller-identity"
	if _, err := d.checkedManagerBinding(b.Session, changed); err == nil {
		t.Fatal("caller supplied login identity became authority")
	}
	changed = b
	changed.NativeGeneration = "replacement-process"
	if _, err := d.checkedManagerBinding(b.Session, changed); err == nil {
		t.Fatal("replacement native generation borrowed binding")
	}
	nonce := strings.Repeat("a", 64)
	registry, err := d.codexManagerRegistry(b.Session, b)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Prepare(nonce, "operation", strings.Repeat("b", 64), 0); err != nil {
		t.Fatal(err)
	}
	if err := registry.StartTurn("owned", ""); err != nil {
		t.Fatal(err)
	}
	if err := registry.BindInput(nonce, "owned", "message"); err != nil {
		t.Fatal(err)
	}
	registry.CloseTurn("owned", "answer")
	if err := registry.StartTurn("manual", ""); err != nil {
		t.Fatal(err)
	}
	outcome, err := d.managerInputStatus(context.Background(), b.Session, managerproof.Admission{Binding: b, Nonce: nonce})
	if err != nil || outcome.NativeTurnID != "owned" || outcome.State != "completed" {
		t.Fatalf("historical facade borrowed current turn: %+v %v", outcome, err)
	}
	feed.retired.Store(true)
	if _, err := d.managerRuntime(b.Session, b.SessionInstance); err == nil {
		t.Fatal("lost observer retained runtime authority")
	}
}
