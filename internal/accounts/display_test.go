package accounts

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCachedDisplayNeverEstablishesIdentityAndSanitizesControls(t *testing.T) {
	s, _ := testStore(t)
	candidate, a := admitNative(t, s, "display-account")
	binding, err := s.CurrentBinding(a.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ email, want string }{{"person@example.test", "person@example.test"}, {"person@example.test\nAuthorization: secret", ""}, {"person\u202e@example.test", ""}} {
		claims, _ := json.Marshal(map[string]string{"email": test.email, "chatgpt_plan_type": "invented"})
		raw, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"account_id": "display-account", "access_token": "synthetic-bearer", "refresh_token": "synthetic-refresh", "id_token": "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"}})
		if err := os.WriteFile(filepath.Join(candidate.ProfilePath, "auth.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		email, plan, err := s.DisplayIdentity(binding)
		if err != nil || email != test.want || plan != "" {
			t.Fatalf("unsafe optional presentation fields: email=%q plan=%q error=%v", email, plan, err)
		}
		if identity, err := s.NativeIdentity(binding); err != nil || identity != binding.Identity {
			t.Fatal("cached display changed immutable identity")
		}
	}
}

func TestAmbiguousNativeIdentityAndRegistryJSONFailClosed(t *testing.T) {
	if _, err := CodexIdentity([]byte(`{"tokens":{"account_id":"first","account_id":"second"}}`)); err == nil {
		t.Fatal("duplicate immutable native identity accepted")
	}
	if _, err := ClaudeIdentity([]byte(`{"oauthAccount":{"accountUuid":"a","organizationUuid":"one","organizationUuid":"two"}}`)); err == nil {
		t.Fatal("duplicate subscription identity accepted")
	}
	s, root := testStore(t)
	if err := os.WriteFile(filepath.Join(root, "accounts", "registry.json"), []byte(`{"schema_version":1,"revision":1,"revision":2,"enabled":{},"accounts":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot(); err == nil {
		t.Fatal("duplicate registry revision accepted")
	}
}
