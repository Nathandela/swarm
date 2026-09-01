package codex

// The ADR-024 auth-probe contract, pinned against the two failure modes that
// motivated it: an identity that moved on every routine token refresh would
// recycle the fleet daily (codex rewrites auth.json each refresh), and an
// identity that missed a re-login would leave stranded sessions stranded.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
)

// probe returns the adapter through the extension assertion, proving the codex
// adapter actually advertises it (absence is the signal, ADR-010 section 5).
func probe(t *testing.T) adapter.AuthProbe {
	t.Helper()
	p, ok := adapter.AsAuthProbe(New())
	if !ok {
		t.Fatal("codex adapter does not implement adapter.AuthProbe")
	}
	return p
}

func TestAuthCredentialsFileIsHomeRelative(t *testing.T) {
	got := probe(t).AuthCredentialsFile()
	if got != ".codex/auth.json" {
		t.Fatalf("AuthCredentialsFile = %q; want .codex/auth.json", got)
	}
}

func TestIdentitySurvivesARoutineTokenRefresh(t *testing.T) {
	p := probe(t)
	before := `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,
		"tokens":{"id_token":"old-id","access_token":"old-at","refresh_token":"old-rt","account_id":"acct-1"},
		"last_refresh":"2026-08-31T07:00:00Z"}`
	after := `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,
		"tokens":{"id_token":"new-id","access_token":"new-at","refresh_token":"new-rt","account_id":"acct-1"},
		"last_refresh":"2026-09-01T07:28:21Z"}`
	idBefore, ok := p.AuthIdentity([]byte(before))
	if !ok || idBefore == "" {
		t.Fatalf("before refresh: identity not derived (ok=%v)", ok)
	}
	idAfter, ok := p.AuthIdentity([]byte(after))
	if !ok {
		t.Fatal("after refresh: identity not derived")
	}
	if idBefore != idAfter {
		t.Fatalf("a routine refresh changed the identity (%q -> %q); the watcher would recycle daily", idBefore, idAfter)
	}
}

func TestIdentityChangesWhenTheAccountDoes(t *testing.T) {
	p := probe(t)
	a := `{"auth_mode":"chatgpt","tokens":{"account_id":"acct-1"}}`
	b := `{"auth_mode":"chatgpt","tokens":{"account_id":"acct-2"}}`
	idA, _ := p.AuthIdentity([]byte(a))
	idB, _ := p.AuthIdentity([]byte(b))
	if idA == idB {
		t.Fatal("two different accounts derived the same identity")
	}
}

func TestIdentityNeverEchoesTheSecret(t *testing.T) {
	p := probe(t)
	id, ok := p.AuthIdentity([]byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-super-secret"}`))
	if !ok {
		t.Fatal("apikey mode: identity not derived")
	}
	if id == "sk-super-secret" || len(id) != 64 {
		t.Fatalf("identity %q is not a digest", id)
	}
}

func TestAPIKeyModeDistinguishesKeys(t *testing.T) {
	p := probe(t)
	a, _ := p.AuthIdentity([]byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-one"}`))
	b, _ := p.AuthIdentity([]byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-two"}`))
	if a == b {
		t.Fatal("two different API keys derived the same identity")
	}
}

func TestGarbageAndEmptyFailClosed(t *testing.T) {
	p := probe(t)
	for name, raw := range map[string]string{
		"garbage":       "not json at all",
		"empty object":  "{}",
		"empty bytes":   "",
		"no account":    `{"auth_mode":"chatgpt","tokens":{}}`,
		"truncated mid": `{"auth_mode":"chatgpt","tokens":{"account_id":"acc`,
	} {
		if id, ok := p.AuthIdentity([]byte(raw)); ok {
			t.Errorf("%s: derived identity %q from unparseable credentials; must hold", name, id)
		}
	}
}
