package daemon

import "testing"

// TestComposerIdempotency_ClaimBindsExactRequestHash covers the production store seam behind
// the protocol's no-false-success rule. A terminal result may replay only for the exact body
// that created it; another body reusing the key is a collision, not cached success.
func TestComposerIdempotency_ClaimBindsExactRequestHash(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	const (
		op       = "devA:01JCOMPOSERBODYBIND000"
		action   = "composer_send"
		session  = "session-one"
		instance = "instance-one"
	)
	phase, _, err := d.ClaimComposerOperation(op, action, session, instance, "hash-a")
	if err != nil || phase != "prepared" {
		t.Fatalf("initial claim = phase %q err %v, want prepared", phase, err)
	}
	if err := d.BeginComposerOperation(op); err != nil {
		t.Fatalf("BeginComposerOperation: %v", err)
	}
	if err := d.CommitComposerOperation(op, []byte(`{"ok":true}`), true); err != nil {
		t.Fatalf("CommitComposerOperation: %v", err)
	}
	phase, _, err = d.ClaimComposerOperation(op, action, session, instance, "hash-a")
	if err != nil || phase != "completed" {
		t.Fatalf("exact replay = phase %q err %v, want completed", phase, err)
	}
	if _, _, err := d.ClaimComposerOperation(op, action, session, instance, "hash-b"); err == nil {
		t.Fatal("same operation_id accepted a different request hash and could replay false success")
	}
}
