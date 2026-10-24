package skeleton

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/loginworker"
	"github.com/Nathandela/swarm/internal/protocol"
)

func codexEnrollmentCredential(user, account, refresh string) logins.Credential {
	claims, _ := json.Marshal(map[string]any{"exp": time.Now().Add(2 * time.Hour).Unix(), "https://api.openai.com/auth": map[string]string{"chatgpt_user_id": user, "chatgpt_account_id": account}})
	token := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic"
	auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"account_id": account, "id_token": token, "access_token": token, "refresh_token": refresh}})
	return logins.Credential{Auth: auth}
}
func codexOwnedEnrollmentFixture(t *testing.T, parked bool) (*loginFixture, loginworker.Config, logins.Credential) {
	t.Helper()
	f := newLoginFixture(t)
	enableCodexNativeGrantUsage(t, f)
	target, account := f.first, "owned"
	if parked {
		r, e := f.m.store.Snapshot()
		if e != nil {
			t.Fatal(e)
		}
		_, target, e = f.m.store.Add(r.Revision, logins.ProviderCodex, "Parked", usageCodexGrant("parked"))
		if e != nil {
			t.Fatal(e)
		}
		account = "parked"
	}
	c := loginworker.Config{SchemaVersion: 1, StateRoot: f.m.store.StateRoot(), JobID: "0123456789abcdef0123456789abcdef", Generation: 1, Provider: logins.ProviderCodex, Label: target.Label, LoginID: target.ID, Deadline: time.Now().Add(time.Minute)}
	credential := codexEnrollmentCredential("user-"+account, account, "synthetic-enrollment-refresh")
	writeCodexEnrollmentJob(t, c, credential)
	return f, c, credential
}

// Synthetic stopped enrollment evidence exercises the real protocol consumer;
// it does not qualify native login or process containment.
func writeCodexEnrollmentJob(t *testing.T, c loginworker.Config, credential logins.Credential) {
	t.Helper()
	home := loginworker.NativeHome(c.StateRoot, c.JobID)
	if e := os.MkdirAll(home, 0700); e != nil {
		t.Fatal(e)
	}
	for name, value := range map[string]any{loginworker.ConfigFile: c, loginworker.ProgressFile: loginworker.Progress{SchemaVersion: 1, JobID: c.JobID, Generation: c.Generation, Phase: loginworker.PhaseReady, WritersStopped: true, NativeWritersStopped: true}} {
		raw, e := json.Marshal(value)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(filepath.Dir(home), name), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(filepath.Join(home, "auth.json"), credential.Auth, 0600); e != nil {
		t.Fatal(e)
	}
}
func codexEnrollmentLogin(t *testing.T, f *loginFixture, id string) logins.Login {
	t.Helper()
	r, e := f.m.store.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	l, ok := loginByID(r, id)
	if !ok {
		t.Fatal("missing saved login")
	}
	return l
}
func codexCanonicalPath(c loginworker.Config) string {
	return filepath.Join(c.StateRoot, "logins", "saved", c.LoginID, "auth.json")
}
func stageCodexEnrollmentIntent(t *testing.T, c loginworker.Config, old logins.Login, credential logins.Credential, metadataAhead bool) {
	t.Helper()
	owner, e := logins.CodexGrantOwner(credential.Auth)
	if e != nil {
		t.Fatal(e)
	}
	var auth struct {
		Tokens map[string]string `json:"tokens"`
	}
	if e = json.Unmarshal(credential.Auth, &auth); e != nil {
		t.Fatal(e)
	}
	tokens, _ := json.Marshal([3]string{auth.Tokens["access_token"], auth.Tokens["id_token"], auth.Tokens["refresh_token"]})
	generation := sha256.Sum256(tokens)
	target := map[string]any{"revision": old.CredentialRevision + 1, "identity": old.Identity, "credential": map[string]any{"auth": nil}, "auth_owner": map[string]any{"version": 1, "owner": owner}, "login_job_id": c.JobID}
	intent := map[string]any{"version": 1, "login_id": old.ID, "from_revision": old.CredentialRevision, "previous_owner": owner, "generation": hex.EncodeToString(generation[:]), "target": target}
	root := filepath.Dir(codexCanonicalPath(c))
	raw, _ := json.Marshal(intent)
	if e = os.WriteFile(filepath.Join(root, "grant-relogin.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(codexCanonicalPath(c), credential.Auth, 0600); e != nil {
		t.Fatal(e)
	}
	if metadataAhead {
		raw, _ = json.Marshal(target)
		if e = os.WriteFile(filepath.Join(root, "credential.json"), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
}
func assertCodexEnrollmentConsumed(t *testing.T, c loginworker.Config) {
	t.Helper()
	if done, e := loginworker.Completed(c); e != nil || !done {
		t.Fatalf("completion receipt missing: %v", e)
	}
	if cleaned, e := loginworker.Cleaned(c); e != nil || !cleaned {
		t.Fatalf("enrollment grant cleanup missing: %v", e)
	}
	if _, e := os.Stat(filepath.Join(loginworker.NativeHome(c.StateRoot, c.JobID), "auth.json")); !os.IsNotExist(e) {
		t.Fatal("stopped enrollment retained refresh authority")
	}
}
func TestCodexOwnedEnrollmentProtocolCompletesActiveAndParkedReauth(t *testing.T) {
	for _, parked := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "parked"}[parked], func(t *testing.T) {
			f, c, credential := codexOwnedEnrollmentFixture(t, parked)
			before := codexEnrollmentLogin(t, f, c.LoginID)
			reply, e := f.m.d.api.Logins(protocol.LoginsReq{Action: "complete", JobID: c.JobID})
			if e != nil {
				t.Fatal(e)
			}
			after := codexEnrollmentLogin(t, f, c.LoginID)
			if after.LastLoginJobID != c.JobID || after.CredentialRevision != before.CredentialRevision+1 {
				t.Fatal("canonical grant job acknowledgement lost")
			}
			if !parked {
				if _, e = f.m.store.Runtime(logins.ProviderCodex); !errors.Is(e, logins.ErrPending) {
					t.Fatalf("active reauth bypassed pending desired revision: %v", e)
				}
			}
			r, _ := f.m.store.Snapshot()
			if len(r.Switches) != 0 || len(r.Handovers) != 0 || len(f.launched) != 0 {
				t.Fatal("enrollment consumption started legacy switch or discussion restart")
			}
			metadata, e := os.ReadFile(filepath.Join(filepath.Dir(codexCanonicalPath(c)), "credential.json"))
			if e != nil {
				t.Fatal(e)
			}
			if bytes.Contains(metadata, []byte("refresh_token")) || bytes.Contains(metadata, []byte("access_token")) {
				t.Fatal("saved metadata duplicated canonical grant")
			}
			canonical, e := os.ReadFile(codexCanonicalPath(c))
			if e != nil || !bytes.Contains(canonical, []byte("synthetic-enrollment-refresh")) {
				t.Fatal("native-owned canonical grant not installed")
			}
			view, _ := json.Marshal(reply)
			if bytes.Contains(view, []byte("synthetic-enrollment-refresh")) || bytes.Contains(view, credential.Auth) {
				t.Fatal("protocol reply exposed authentication")
			}
			assertCodexEnrollmentConsumed(t, c)
		})
	}
}
func TestCodexOwnedEnrollmentProtocolRetriesMetadataAheadIntent(t *testing.T) {
	f, c, credential := codexOwnedEnrollmentFixture(t, false)
	old := codexEnrollmentLogin(t, f, c.LoginID)
	stageCodexEnrollmentIntent(t, c, old, credential, true)
	if _, e := f.m.d.api.Logins(protocol.LoginsReq{Action: "complete", JobID: c.JobID}); e != nil {
		t.Fatal(e)
	}
	current := codexEnrollmentLogin(t, f, c.LoginID)
	if current.CredentialRevision != old.CredentialRevision+1 || current.LastLoginJobID != c.JobID {
		t.Fatal("intent retry lost exact job/revision")
	}
	if _, e := os.Stat(filepath.Join(filepath.Dir(codexCanonicalPath(c)), "grant-relogin.json")); !os.IsNotExist(e) {
		t.Fatal("completed intent retained")
	}
	assertCodexEnrollmentConsumed(t, c)
}
func TestCodexOwnedEnrollmentProtocolDuplicateNeverReplaysRenewedGrant(t *testing.T) {
	for _, pendingIntent := range []bool{false, true} {
		t.Run(map[bool]string{false: "ack-before-completion", true: "ack-before-intent-retirement"}[pendingIntent], func(t *testing.T) {
			f, c, credential := codexOwnedEnrollmentFixture(t, false)
			old := codexEnrollmentLogin(t, f, c.LoginID)
			r, _ := f.m.store.Snapshot()
			lease, e := f.m.store.AcquireCodexRelogin(c.LoginID, c.JobID)
			if e != nil {
				t.Fatal(e)
			}
			if _, _, e = lease.ReplaceCredential(r.Revision, credential, c.JobID); e != nil {
				t.Fatal(e)
			}
			if e = lease.Close(); e != nil {
				t.Fatal(e)
			}
			if pendingIntent {
				stageCodexEnrollmentIntent(t, c, old, credential, true)
			}
			renewed := codexEnrollmentCredential("user-owned", "owned", "synthetic-native-rotated-refresh")
			if e = os.WriteFile(codexCanonicalPath(c), renewed.Auth, 0600); e != nil {
				t.Fatal(e)
			}
			if _, e = f.m.d.api.Logins(protocol.LoginsReq{Action: "complete", JobID: c.JobID}); e != nil {
				t.Fatal(e)
			}
			actual, e := os.ReadFile(codexCanonicalPath(c))
			if e != nil || !bytes.Equal(actual, renewed.Auth) {
				t.Fatal("completed enrollment replayed stale credentials over native rotation")
			}
			if e = f.m.store.CheckCodexGrantPending(c.LoginID); e != nil {
				t.Fatalf("accepted completed job left grant fenced: %v", e)
			}
			assertCodexEnrollmentConsumed(t, c)
		})
	}
}
func TestCodexOwnedEnrollmentProtocolRejectsWrongIdentityWithoutChangingGrant(t *testing.T) {
	for _, kind := range []string{"workspace", "same-workspace-user"} {
		t.Run(kind, func(t *testing.T) {
			f, c, _ := codexOwnedEnrollmentFixture(t, false)
			before, e := os.ReadFile(codexCanonicalPath(c))
			if e != nil {
				t.Fatal(e)
			}
			user, account := "user-owned", "other"
			if kind == "same-workspace-user" {
				user, account = "other-user", "owned"
			}
			writeCodexEnrollmentJob(t, c, codexEnrollmentCredential(user, account, "synthetic-wrong-refresh"))
			if _, e = f.m.d.api.Logins(protocol.LoginsReq{Action: "complete", JobID: c.JobID}); e != nil {
				t.Fatal(e)
			}
			after, e := os.ReadFile(codexCanonicalPath(c))
			if e != nil || !bytes.Equal(before, after) {
				t.Fatal("wrong account changed canonical authority")
			}
			progress, e := loginworker.ReadProgress(c.StateRoot, c.JobID)
			if e != nil || progress.ErrorCode != "wrong_login" || progress.Phase != loginworker.PhaseFailed {
				t.Fatalf("wrong-account rejection missing: %v", e)
			}
			if cleaned, e := loginworker.Cleaned(c); e != nil || !cleaned {
				t.Fatal("wrong stopped enrollment was not cleaned")
			}
			lease, e := f.m.store.AcquireCodexGrant(c.LoginID)
			if e != nil {
				t.Fatalf("rejected job leaked grant custody: %v", e)
			}
			_ = lease.Close()
		})
	}
}
func TestCodexOwnedEnrollmentProtocolRetainsCandidateWhileGrantLeased(t *testing.T) {
	f, c, _ := codexOwnedEnrollmentFixture(t, false)
	lease, e := f.m.store.AcquireCodexGrant(c.LoginID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.m.d.api.Logins(protocol.LoginsReq{Action: "complete", JobID: c.JobID}); !errors.Is(e, logins.ErrInUse) {
		t.Fatalf("leased authority consumed enrollment: %v", e)
	}
	if done, e := loginworker.Completed(c); e != nil || done {
		t.Fatal("busy authority marked completed")
	}
	if _, e = os.Stat(filepath.Join(loginworker.NativeHome(c.StateRoot, c.JobID), "auth.json")); e != nil {
		t.Fatal("busy authority destroyed enrollment candidate")
	}
	_ = lease.Close()
	if _, e = f.m.d.api.Logins(protocol.LoginsReq{Action: "complete", JobID: c.JobID}); e != nil {
		t.Fatal(e)
	}
}
func TestCodexOwnedEnrollmentProtocolImportsNewAccountCanonically(t *testing.T) {
	f, c, _ := codexOwnedEnrollmentFixture(t, false)
	c.LoginID = ""
	c.Label = "New saved login"
	writeCodexEnrollmentJob(t, c, codexEnrollmentCredential("new-user", "new-workspace", "synthetic-new-refresh"))
	if _, e := f.m.d.api.Logins(protocol.LoginsReq{Action: "complete", JobID: c.JobID}); e != nil {
		t.Fatal(e)
	}
	r, e := f.m.store.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	if r.Providers[logins.ProviderCodex].ActiveID != f.first.ID {
		t.Fatal("import unexpectedly selected new login")
	}
	c.LoginID = c.JobID
	if _, e = os.Stat(codexCanonicalPath(c)); e != nil {
		t.Fatal("import did not create canonical native authority")
	}
	metadata, e := os.ReadFile(filepath.Join(filepath.Dir(codexCanonicalPath(c)), "credential.json"))
	if e != nil || bytes.Contains(metadata, []byte("refresh_token")) {
		t.Fatal("import copied grant into metadata")
	}
	assertCodexEnrollmentConsumed(t, c)
}
