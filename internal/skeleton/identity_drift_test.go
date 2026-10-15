package skeleton

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/protocol"
)

// identityDriftRig is one running managed session whose bound profile identity
// the test rewrites between checkIdentities ticks.
type identityDriftRig struct {
	m       *accountRotationManager
	store   *accounts.Store
	binding accounts.Binding
	local   string
	auth    string
	held    int
	cleared int
}

func newIdentityDriftRig(t *testing.T) *identityDriftRig {
	t.Helper()
	store, root, bindings := accountTestStore(t, 1)
	source := accountTestSource(t, root, bindings[0])
	m, _ := rotationTestManager(t, store, root, source)
	profile, err := store.HistoryProfilePath(bindings[0])
	if err != nil {
		t.Fatal(err)
	}
	r := &identityDriftRig{m: m, store: store, binding: bindings[0], local: source.ID, auth: filepath.Join(profile, "auth.json")}
	m.w.restoreRecycle = func(string) { r.held++ }
	m.w.clearRecycle = func(local string) {
		if local == r.local {
			r.cleared++
		}
	}
	return r
}

func (r *identityDriftRig) writeIdentity(t *testing.T, id string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"account_id": id, "access_token": "synthetic-access", "refresh_token": "synthetic-refresh"}})
	if err := os.WriteFile(r.auth, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (r *identityDriftRig) drift(t *testing.T) {
	t.Helper()
	r.writeIdentity(t, "different-native-account")
	r.m.checkIdentities()
	if rec := r.m.w.state.AccountRotations[r.local]; !rec.IdentityHeld || r.held == 0 {
		t.Fatalf("setup: a real identity change was not held: %+v held=%d", rec, r.held)
	}
}

func (r *identityDriftRig) accountAuth(t *testing.T) string {
	t.Helper()
	registry, err := r.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return registry.Accounts[r.binding.AccountID].Auth
}

func TestIdentityDrift_ReadErrorIsNotDrift(t *testing.T) {
	r := newIdentityDriftRig(t)
	for _, damage := range []func() error{
		func() error { return os.Remove(r.auth) },
		func() error { return os.WriteFile(r.auth, []byte("{not json"), 0o600) },
	} {
		if err := damage(); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			r.m.checkIdentities()
		}
		if len(r.m.w.state.AccountRotations) != 0 || r.held != 0 {
			t.Fatalf("an unreadable profile identity was reported as drift: rotations=%+v held=%d", r.m.w.state.AccountRotations, r.held)
		}
		if got := r.accountAuth(t); got != accounts.AuthValid {
			t.Fatalf("an unreadable profile identity marked the account %q", got)
		}
	}
}

func TestIdentityDrift_HoldReleasesWhenIdentityMatchesAgain(t *testing.T) {
	r := newIdentityDriftRig(t)
	r.drift(t)
	r.writeIdentity(t, "synthetic-account-0")
	r.m.checkIdentities()
	rec := r.m.w.state.AccountRotations[r.local]
	if rec.State == accountObserved || rec.State == accountBlocked || r.cleared == 0 {
		t.Fatalf("matching identity did not release the hold: %+v cleared=%d", rec, r.cleared)
	}
	if rec.State == accountOwnerCanceled {
		t.Fatalf("released hold was retired as an owner end, which refuses every later recovery: %+v", rec)
	}
	// A later real drift is held again: the release left no tombstone.
	held := r.held
	r.writeIdentity(t, "different-native-account")
	r.m.checkIdentities()
	if rec = r.m.w.state.AccountRotations[r.local]; !rec.IdentityHeld || rec.State != accountObserved || r.held != held+1 {
		t.Fatalf("drift after a release was not held again: %+v held=%d", rec, r.held)
	}
}

func TestIdentityDrift_DriftNeedsLoginIsRestoredWhenIdentityMatches(t *testing.T) {
	r := newIdentityDriftRig(t)
	r.drift(t)
	if got := r.accountAuth(t); got != accounts.AuthNeedsLogin {
		t.Fatalf("setup: drift left auth %q", got)
	}
	r.writeIdentity(t, "synthetic-account-0")
	r.m.checkIdentities()
	if got := r.accountAuth(t); got != accounts.AuthValid {
		t.Fatalf("needs-login set only by drift was not restored once the identity matched: %q", got)
	}
}

func TestIdentityDrift_LoginExpiredNeedsLoginIsNotRestored(t *testing.T) {
	for _, order := range []string{"expired-then-drift", "drift-then-expired"} {
		t.Run(order, func(t *testing.T) {
			r := newIdentityDriftRig(t)
			expire := func() {
				registry, err := r.store.Snapshot()
				if err != nil {
					t.Fatal(err)
				}
				// The login-expired quota fetch's own write.
				if _, err := r.store.SetAuth(registry.Revision, r.binding.AccountID, accounts.AuthNeedsLogin); err != nil {
					t.Fatal(err)
				}
			}
			if order == "expired-then-drift" {
				expire()
				r.drift(t)
			} else {
				r.drift(t)
				expire()
			}
			r.writeIdentity(t, "synthetic-account-0")
			r.m.checkIdentities()
			if got := r.accountAuth(t); got != accounts.AuthNeedsLogin {
				t.Fatalf("a login-expired needs-login was restored by an identity match: %q", got)
			}
		})
	}
}

func TestInputGate_RefusalNamesTheHoldingJob(t *testing.T) {
	r := newIdentityDriftRig(t)
	d := &Daemon{}
	r.m.d = d
	r.m.w.restoreRecycle = d.restoreAuthRecycle
	r.writeIdentity(t, "different-native-account")
	r.m.checkIdentities()
	_, err := d.composerSend("", "op-1", protocol.ComposerSendReq{Session: protocol.NamespacedID("ep-test01", r.local), Text: "hello"})
	if err == nil {
		t.Fatal("an identity-held session accepted a composer send")
	}
	msg := err.Error()
	if strings.Contains(msg, "recycling stale credentials") || !strings.Contains(msg, "identity check") || !strings.Contains(msg, `"test"`) {
		t.Fatalf("refusal does not name the identity check on the account and its remedy: %s", msg)
	}

	// A lock with no recorded job keeps the credential-recycle wording.
	d.restoreAuthRecycle("other-session")
	_, err = d.composerSend("", "op-2", protocol.ComposerSendReq{Session: protocol.NamespacedID("ep-test01", "other-session"), Text: "hello"})
	if err == nil || !strings.Contains(err.Error(), "recycling stale credentials") {
		t.Fatalf("default refusal wording changed: %v", err)
	}
}
