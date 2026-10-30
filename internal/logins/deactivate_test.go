package logins

import (
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// stopDeactivation records every row and then the journal stopped, as the
// daemon does once each discussion is at a safe idle or detached point.
func stopDeactivation(t *testing.T, s *Store, op Switch) Switch {
	t.Helper()
	op.Phase = "stopping"
	if e := s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	for i := range op.Sessions {
		op.Sessions[i].Phase = "stopped"
	}
	op.Phase = "stopped"
	if e := s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	return op
}
func deactivateStopped(t *testing.T, s *Store, provider string, rows []Restart) Switch {
	t.Helper()
	r := snapshot(t, s)
	op, e := s.BeginDeactivate(r.Revision, provider, "test", rows)
	if e != nil {
		t.Fatal(e)
	}
	return stopDeactivation(t, s, op)
}

// finishDeactivation saves the outgoing grant, installs nothing, clears the
// selection, cancels every row (nothing is resumed) and completes.
func finishDeactivation(t *testing.T, s *Store, op Switch) {
	t.Helper()
	if e := s.CaptureOutgoing(op.ID); e != nil {
		t.Fatal(e)
	}
	if e := s.InstallIncoming(op.ID); e != nil {
		t.Fatal(e)
	}
	rt, e := s.CommitSwitch(op.ID)
	if e != nil {
		t.Fatal(e)
	}
	if rt != (Runtime{}) {
		t.Fatalf("deactivation produced a runtime: %+v", rt)
	}
	op = snapshot(t, s).Switches[op.Provider]
	if op.Phase != "committed" {
		t.Fatalf("deactivation not committed: %+v", op)
	}
	for i := range op.Sessions {
		op.Sessions[i].Phase = "cancelled"
	}
	if e = s.SaveSwitch(op); e != nil {
		t.Fatalf("rows not cancellable after commit: %v", e)
	}
	if e = s.CompleteSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
}
func deactivate(t *testing.T, s *Store, provider string, rows []Restart) Switch {
	t.Helper()
	op := deactivateStopped(t, s, provider, rows)
	finishDeactivation(t, s, op)
	return op
}

func TestDeactivateRequiresActiveLogin(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
	r := snapshot(t, s)
	if _, e := s.BeginDeactivate(r.Revision, ProviderCodex, "manual", nil); !errors.Is(e, ErrIneligible) {
		t.Fatalf("deactivated without a selection: %v", e)
	}
	if _, e := s.BeginDeactivate(r.Revision, "other", "manual", nil); !errors.Is(e, ErrIneligible) {
		t.Fatalf("unknown provider admitted: %v", e)
	}
	activate(t, s, a)
	r = snapshot(t, s)
	if _, e := s.BeginDeactivate(r.Revision, ProviderClaude, "manual", nil); !errors.Is(e, ErrIneligible) {
		t.Fatalf("provider without a selection admitted: %v", e)
	}
	if _, e := s.BeginDeactivate(r.Revision-1, ProviderCodex, "manual", nil); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale revision admitted: %v", e)
	}
	r, e := s.mutate(r.Revision, func(r *Registry) error {
		p := r.Providers[ProviderCodex]
		p.Epoch = ^uint64(0)
		r.Providers[ProviderCodex] = p
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.BeginDeactivate(r.Revision, ProviderCodex, "manual", nil); !errors.Is(e, ErrIneligible) {
		t.Fatalf("epoch overflow admitted: %v", e)
	}
	if len(snapshot(t, s).Switches) != 0 {
		t.Fatal("refused deactivation left a journal")
	}
}

func TestDeactivateRefusedWhileSwitchPending(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
	b := add(t, s, ProviderCodex, "B", codexCredential("b", "b"))
	activate(t, s, a)
	r := snapshot(t, s)
	op, e := s.BeginSwitch(r.Revision, ProviderCodex, b.ID, "manual", nil)
	if e != nil {
		t.Fatal(e)
	}
	r = snapshot(t, s)
	if _, e = s.BeginDeactivate(r.Revision, ProviderCodex, "manual", nil); !errors.Is(e, ErrPending) {
		t.Fatalf("deactivation admitted beside a switch: %v", e)
	}
	if e = s.CancelSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	r = snapshot(t, s)
	if _, e = s.BeginDeactivate(r.Revision, ProviderCodex, "manual", nil); e != nil {
		t.Fatal(e)
	}
	r = snapshot(t, s)
	if _, e = s.BeginSwitch(r.Revision, ProviderCodex, b.ID, "manual", nil); !errors.Is(e, ErrPending) {
		t.Fatalf("switch admitted beside a deactivation: %v", e)
	}
	if _, e = s.BeginRelogin(r.Revision, a.ID, codexCredential("a", "fresh"), "relogin", nil); !errors.Is(e, ErrPending) {
		t.Fatalf("relogin admitted beside a deactivation: %v", e)
	}
	if _, e = s.BeginDeactivate(r.Revision, ProviderCodex, "manual", nil); !errors.Is(e, ErrPending) {
		t.Fatalf("second deactivation admitted: %v", e)
	}
}

func TestDeactivateJournalValidation(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
	b := add(t, s, ProviderCodex, "B", codexCredential("b", "b"))
	rt := activate(t, s, a)
	r := snapshot(t, s)
	op, e := s.BeginDeactivate(r.Revision, ProviderCodex, "manual", []Restart{{SourceID: "source", ConversationID: "native"}})
	if e != nil {
		t.Fatal(e)
	}
	if !op.Deactivate || !validID(op.ID) || op.FromID != a.ID || op.ToID != "" || op.FromEpoch != rt.Epoch || op.ToEpoch != rt.Epoch+1 || op.Phase != "waiting" || op.Reason != "manual" || op.Relogin || op.NextID != "" || op.LoginJobID != "" || len(op.AttemptedIDs) != 0 {
		t.Fatalf("bad deactivation journal %+v", op)
	}
	r = snapshot(t, s)
	if !reflect.DeepEqual(r.Switches[ProviderCodex], op) {
		t.Fatalf("journal did not round-trip: %+v", r.Switches[ProviderCodex])
	}
	raw, e := readPrivate(s.root, "registry.json", maxRegistryBytes)
	if e != nil || !strings.Contains(string(raw), `"deactivate":true`) {
		t.Fatalf("marker not persisted: %s %v", raw, e)
	}
	committed := func(op *Switch, p *ProviderState) { op.Phase = "committed"; p.ActiveID = ""; p.Epoch = op.ToEpoch }
	check := func(name string, valid bool, corrupt func(*Switch, *ProviderState)) {
		t.Helper()
		c := r
		c.Providers = maps.Clone(r.Providers)
		c.Switches = maps.Clone(r.Switches)
		op := c.Switches[ProviderCodex]
		op.Sessions = slices.Clone(op.Sessions)
		p := c.Providers[ProviderCodex]
		corrupt(&op, &p)
		c.Switches[ProviderCodex] = op
		c.Providers[ProviderCodex] = p
		if e := validateRegistry(c); (e == nil) != valid {
			t.Errorf("%s: want valid=%v, got %v", name, valid, e)
		}
	}
	check("committed", true, committed)
	check("fresh-row", true, func(op *Switch, _ *ProviderState) { op.Sessions[0].ConversationID = "" })
	check("destination", false, func(op *Switch, _ *ProviderState) { op.ToID = b.ID })
	noOutgoing := func(op *Switch, p *ProviderState) { op.FromID = ""; p.ActiveID = "" }
	check("no-outgoing", true, noOutgoing)
	check("no-outgoing-committed", true, func(op *Switch, p *ProviderState) { noOutgoing(op, p); committed(op, p) })
	check("no-outgoing-without-marker", false, func(op *Switch, p *ProviderState) { noOutgoing(op, p); op.Deactivate = false })
	check("no-outgoing-relogin", false, func(op *Switch, p *ProviderState) { noOutgoing(op, p); op.Relogin = true })
	check("relogin", false, func(op *Switch, _ *ProviderState) { op.Relogin = true })
	check("next", false, func(op *Switch, _ *ProviderState) { op.NextID = b.ID })
	check("login-job", false, func(op *Switch, _ *ProviderState) { op.LoginJobID = strings.Repeat("c", 32) })
	check("attempted", false, func(op *Switch, _ *ProviderState) { op.AttemptedIDs = []string{b.ID} })
	check("resuming", false, func(op *Switch, p *ProviderState) { committed(op, p); op.Phase = "resuming" })
	check("retry-stopping", false, func(op *Switch, p *ProviderState) { committed(op, p); op.Phase = "retry-stopping"; op.NextID = b.ID })
	check("normal-without-destination", false, func(op *Switch, _ *ProviderState) { op.Deactivate = false })
	check("normal", true, func(op *Switch, _ *ProviderState) {
		op.Deactivate = false
		op.ToID = b.ID
		op.AttemptedIDs = []string{b.ID}
	})
	check("normal-fresh-row", false, func(op *Switch, _ *ProviderState) {
		op.Deactivate = false
		op.ToID = b.ID
		op.Sessions[0].ConversationID = ""
	})
	forged := op
	forged.Deactivate = false
	if e = s.SaveSwitch(forged); !errors.Is(e, ErrConflict) {
		t.Fatalf("SaveSwitch dropped the deactivation marker: %v", e)
	}
}

func TestDeactivateCodexCapturesLatestRefreshAndScrubsNativeGrant(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "old"))
	rt := activate(t, s, a)
	// A native writer refreshed the active grant beside unrelated auth state.
	native := `{"auth_mode":"chatgpt","tokens":{"account_id":"a","access_token":"latest","refresh_token":"refresh-latest"},"last_refresh":"2026-10-30T00:00:00Z","OPENAI_API_KEY":null,"unrelated":"keep"}`
	if _, e := durableReplace(s.root, "provider/codex/native/auth.json", []byte(native), writeOps{}); e != nil {
		t.Fatal(e)
	}
	deactivate(t, s, ProviderCodex, nil)
	r := snapshot(t, s)
	if p := r.Providers[ProviderCodex]; p.ActiveID != "" || p.Epoch != rt.Epoch+1 || len(r.Switches) != 0 {
		t.Fatalf("selection not cleared: %+v %+v", p, r.Switches)
	}
	l := find(&r, a.ID)
	if l.AuthState != AuthReady || l.CredentialRevision != a.CredentialRevision+1 {
		t.Fatalf("outgoing login not captured: %+v", l)
	}
	saved, e := s.readSaved(*l)
	if e != nil || !strings.Contains(string(saved.Auth), "refresh-latest") {
		t.Fatalf("lost active refresh: %s %v", saved.Auth, e)
	}
	raw, e := os.ReadFile(filepath.Join(rt.Home, "auth.json"))
	var doc map[string]json.RawMessage
	if e != nil || json.Unmarshal(raw, &doc) != nil {
		t.Fatalf("native auth unreadable: %s %v", raw, e)
	}
	for _, key := range []string{"auth_mode", "tokens", "last_refresh", "OPENAI_API_KEY"} {
		if _, ok := doc[key]; ok {
			t.Fatalf("native grant field %s survived: %s", key, raw)
		}
	}
	if string(doc["unrelated"]) != `"keep"` || strings.Contains(string(raw), "latest") {
		t.Fatalf("scrub changed unrelated state or kept a token: %s", raw)
	}
	if _, e = s.Runtime(ProviderCodex); !errors.Is(e, ErrIneligible) {
		t.Fatalf("managed runtime survived deactivation: %v", e)
	}
	again := activate(t, s, a)
	if again.Epoch != rt.Epoch+2 || again.Home != rt.Home || snapshot(t, s).Providers[ProviderCodex].Epoch != again.Epoch {
		t.Fatalf("home/epoch: before=%+v after=%+v", rt, again)
	}
	if e = s.ValidateRuntime(again); e != nil {
		t.Fatal(e)
	}
	raw, e = os.ReadFile(filepath.Join(again.Home, "auth.json"))
	if e != nil || !strings.Contains(string(raw), "refresh-latest") || !strings.Contains(string(raw), "keep") {
		t.Fatalf("reactivation lost latest grant: %s %v", raw, e)
	}
}

func TestDeactivateClaudeCapturesSecureStorage(t *testing.T) {
	s := testStore(t)
	kc := s.keychain.(*fakeKeychain)
	a := add(t, s, ProviderClaude, "A", claudeCredential("a", "old"))
	rt := activate(t, s, a)
	// Claude refreshes the selected login's own secure storage in place.
	if e := s.writeClaudeAuth(a.ID, []byte(`{"claudeAiOauth":{"accessToken":"latest","refreshToken":"refresh-latest","expiresAt":5678}}`)); e != nil {
		t.Fatal(e)
	}
	files := map[string]string{".claude.json": `{"oauthAccount":{"accountUuid":"a","organizationUuid":"org"},"userID":"a","projects":{"project":{"trust":true}}}`, ".credentials.json": `{"mcpOAuth":{"private":"keep"}}`}
	for name, body := range files {
		if _, e := durableReplace(s.root, "provider/claude/native/"+name, []byte(body), writeOps{}); e != nil {
			t.Fatal(e)
		}
	}
	deactivate(t, s, ProviderClaude, nil)
	r := snapshot(t, s)
	if p := r.Providers[ProviderClaude]; p.ActiveID != "" || p.Epoch != rt.Epoch+1 || len(r.Switches) != 0 {
		t.Fatalf("selection not cleared: %+v %+v", p, r.Switches)
	}
	l := find(&r, a.ID)
	if l.AuthState != AuthReady || l.CredentialRevision != a.CredentialRevision+1 || !l.ExpiresAt.Equal(time.UnixMilli(5678)) {
		t.Fatalf("outgoing secure storage not captured: %+v", l)
	}
	saved, e := s.readSaved(*l)
	if e != nil || !strings.Contains(string(saved.Auth), "refresh-latest") {
		t.Fatalf("lost active refresh: %s %v", saved.Auth, e)
	}
	// Only the account identity leaves the common Claude home.
	want := map[string]string{".claude.json": `{"projects":{"project":{"trust":true}}}`, ".credentials.json": files[".credentials.json"]}
	for name, body := range want {
		raw, e := os.ReadFile(filepath.Join(rt.Home, name))
		if e != nil || string(raw) != body {
			t.Fatalf("deactivation left wrong Claude native %s: %s %v", name, raw, e)
		}
	}
	again := activate(t, s, a)
	if again.Epoch != rt.Epoch+2 || again.Home != rt.Home {
		t.Fatalf("home/epoch: before=%+v after=%+v", rt, again)
	}
	if e = s.ValidateRuntime(again); e != nil {
		t.Fatal(e)
	}
	auth, e := kc.Read(s.savedNativeHome(a.ID))
	if e != nil || !strings.Contains(string(auth), "refresh-latest") {
		t.Fatalf("reactivation lost latest grant: %s %v", auth, e)
	}
}

func TestDeactivateCaptureIdentityMismatchMarksNeedsLogin(t *testing.T) {
	for _, kind := range []string{"codex-foreign", "claude-foreign", "claude-malformed"} {
		t.Run(kind, func(t *testing.T) {
			s := testStore(t)
			p, c, name, native := ProviderCodex, codexCredential("a", "a"), "auth.json", string(codexCredential("foreign", "foreign").Auth)
			switch kind {
			case "claude-foreign":
				p, c, name, native = ProviderClaude, claudeCredential("a", "a"), ".claude.json", string(claudeCredential("foreign", "foreign").Identity)
			case "claude-malformed":
				p, c, name, native = ProviderClaude, claudeCredential("a", "a"), ".claude.json", `{"oauthAccount":{"accountUuid":"a"}}`
			}
			a := add(t, s, p, "A", c)
			rt := activate(t, s, a)
			saved := func() string {
				t.Helper()
				meta, e := readPrivate(s.root, s.savedPath(a), maxCredentialBytes*2)
				if e != nil {
					t.Fatal(e)
				}
				if p == ProviderClaude {
					auth, e := s.claudeAuth(a.ID)
					if e != nil {
						t.Fatal(e)
					}
					meta = append(meta, auth...)
				}
				return string(meta)
			}
			before := saved()
			// Another account was signed in inside the managed native home.
			if _, e := durableReplace(s.root, filepath.Join("provider", p, "native", name), []byte(native), writeOps{}); e != nil {
				t.Fatal(e)
			}
			op := deactivateStopped(t, s, p, []Restart{{SourceID: "source", ConversationID: "native"}})
			if e := s.CaptureOutgoing(op.ID); e != nil {
				t.Fatalf("credential content blocked deactivation: %v", e)
			}
			r := snapshot(t, s)
			if l := find(&r, a.ID); l.AuthState != AuthNeedsLogin || l.CredentialRevision != a.CredentialRevision || r.Switches[p].Phase != "captured" {
				t.Fatalf("content failure not classified: %+v %+v", l, r.Switches[p])
			}
			finishDeactivation(t, s, op)
			if after := saved(); after != before {
				t.Fatalf("saved copy overwritten: %s", after)
			}
			r = snapshot(t, s)
			if r.Providers[p].ActiveID != "" || r.Providers[p].Epoch != rt.Epoch+1 || len(r.Switches) != 0 {
				t.Fatalf("deactivation did not complete: %+v", r)
			}
			if p == ProviderCodex {
				raw, e := os.ReadFile(filepath.Join(rt.Home, "auth.json"))
				if e != nil || strings.Contains(string(raw), "foreign") {
					t.Fatalf("foreign grant left in managed home: %s %v", raw, e)
				}
			}
		})
	}
}

func TestDeactivateCaptureStorageFaultStillRetries(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
	activate(t, s, a)
	name := "provider/codex/native/auth.json"
	if e := s.root.Remove(name); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(filepath.Join(s.path, s.savedPath(a)), filepath.Join(s.path, name)); e != nil {
		t.Fatal(e)
	}
	op := deactivateStopped(t, s, ProviderCodex, nil)
	if e := s.CaptureOutgoing(op.ID); !errors.Is(e, ErrUnsafePath) {
		t.Fatalf("unsafe native store treated as credential content: %v", e)
	}
	r := snapshot(t, s)
	if r.Switches[ProviderCodex].Phase != "stopped" || find(&r, a.ID).AuthState != AuthReady {
		t.Fatalf("storage fault advanced deactivation: %+v", r)
	}
	// Once storage is repaired the same journal completes.
	if e := s.root.Remove(name); e != nil {
		t.Fatal(e)
	}
	if _, e := durableReplace(s.root, name, codexCredential("a", "a").Auth, writeOps{}); e != nil {
		t.Fatal(e)
	}
	finishDeactivation(t, s, op)
}

func TestDeactivateRefusesRetryReloginAndRuntime(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
	b := add(t, s, ProviderCodex, "B", codexCredential("b", "b"))
	rt := activate(t, s, a)
	op := deactivateStopped(t, s, ProviderCodex, []Restart{{SourceID: "source", ConversationID: "native"}})
	if e := s.CaptureOutgoing(op.ID); e != nil {
		t.Fatal(e)
	}
	if e := s.InstallIncoming(op.ID); e != nil {
		t.Fatal(e)
	}
	if e := s.VerifyInstalled(op.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.CommitSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	if e := s.VerifyInstalled(op.ID); e != nil {
		t.Fatal(e)
	}
	before := snapshot(t, s)
	fresh, job := codexCredential("a", "fresh"), strings.Repeat("c", 32)
	for name, call := range map[string]func() error{
		"retry":               func() error { return s.BeginSwitchRetry(op.ID, b.ID, "retry") },
		"retry-at-revision":   func() error { return s.BeginSwitchRetryAtRevision(before.Revision, op.ID, b.ID, "retry") },
		"relogin":             func() error { return s.BeginSwitchRelogin(op.ID, fresh, "relogin") },
		"relogin-at-revision": func() error { return s.BeginSwitchReloginAtRevision(before.Revision, op.ID, fresh, "relogin") },
		"relogin-job":         func() error { return s.BeginSwitchReloginJobAtRevision(before.Revision, op.ID, fresh, "relogin", job) },
		"retarget":            func() error { return s.RetargetSwitch(op.ID) },
	} {
		if e := call(); !errors.Is(e, ErrPending) {
			t.Errorf("%s admitted on a deactivation: %v", name, e)
		}
	}
	if _, e := s.RuntimeForSwitch(op.ID, "source"); !errors.Is(e, ErrPending) {
		t.Fatalf("deactivation produced a restart runtime: %v", e)
	}
	rt.Epoch = op.ToEpoch
	if e := s.ValidateSwitchRestart(op.ID, "source", rt); !errors.Is(e, ErrPending) {
		t.Fatalf("deactivation admitted a restart: %v", e)
	}
	resume := before.Switches[ProviderCodex]
	resume.Phase = "resuming"
	if e := s.SaveSwitch(resume); e == nil {
		t.Fatal("deactivation entered resume")
	}
	if !reflect.DeepEqual(snapshot(t, s), before) {
		t.Fatal("refused call changed the journal")
	}
	if e := s.CompleteSwitch(op.ID); !errors.Is(e, ErrInUse) {
		t.Fatalf("completed with a live row: %v", e)
	}
	done := before.Switches[ProviderCodex]
	done.Sessions[0].Phase = "cancelled"
	if e := s.SaveSwitch(done); e != nil {
		t.Fatal(e)
	}
	if e := s.CompleteSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
}

func TestDeactivateReplayIsIdempotent(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "old"))
	rt := activate(t, s, a)
	if _, e := durableReplace(s.root, "provider/codex/native/auth.json", codexCredential("a", "latest").Auth, writeOps{}); e != nil {
		t.Fatal(e)
	}
	op := deactivateStopped(t, s, ProviderCodex, []Restart{{SourceID: "source", ConversationID: "native"}})
	state := func() (Registry, string) {
		t.Helper()
		r := snapshot(t, s)
		r.Revision = 0
		raw, e := os.ReadFile(filepath.Join(rt.Home, "auth.json"))
		if e != nil {
			t.Fatal(e)
		}
		return r, string(raw)
	}
	replay := func(name string, step func() error) {
		t.Helper()
		if e := step(); e != nil {
			t.Fatalf("%s: %v", name, e)
		}
		r, raw := state()
		if e := step(); e != nil {
			t.Fatalf("%s replay: %v", name, e)
		}
		if again, rawAgain := state(); !reflect.DeepEqual(r, again) || raw != rawAgain {
			t.Fatalf("%s replay changed state:\n%+v %s\n%+v %s", name, r, raw, again, rawAgain)
		}
	}
	replay("capture", func() error { return s.CaptureOutgoing(op.ID) })
	// Crash after the native scrub, before the installed phase is recorded.
	writes := 0
	s.writeOps.rename = func(old, new string) error {
		if new == "registry.json" {
			if writes++; writes == 2 {
				return errors.New("synthetic crash before installed")
			}
		}
		return s.root.Rename(old, new)
	}
	if e := s.InstallIncoming(op.ID); e == nil {
		t.Fatal("injected crash not reached")
	}
	s.writeOps = writeOps{}
	if got := snapshot(t, s).Switches[ProviderCodex].Phase; got != "installing" {
		t.Fatalf("lost install intent: %s", got)
	}
	replay("install", func() error { return s.InstallIncoming(op.ID) })
	replay("commit", func() error {
		rt, e := s.CommitSwitch(op.ID)
		if rt != (Runtime{}) {
			return errors.New("deactivation produced a runtime")
		}
		return e
	})
	r, raw := state()
	saved, e := s.readSaved(*find(&r, a.ID))
	if e != nil || !strings.Contains(string(saved.Auth), "latest") || strings.Contains(raw, "latest") {
		t.Fatalf("replayed deactivation lost capture or kept native grant: %s %s %v", saved.Auth, raw, e)
	}
	if r.Providers[ProviderCodex].ActiveID != "" || r.Providers[ProviderCodex].Epoch != rt.Epoch+1 {
		t.Fatalf("replayed commit moved epoch: %+v", r.Providers[ProviderCodex])
	}
}

func TestCancelDeactivateOnlyWhileUntouched(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
	rt := activate(t, s, a)
	rows := func() []Restart { return []Restart{{SourceID: "source", ConversationID: "native"}} }
	r := snapshot(t, s)
	op, e := s.BeginDeactivate(r.Revision, ProviderCodex, "manual", rows())
	if e != nil {
		t.Fatal(e)
	}
	if e = s.CancelSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	r = snapshot(t, s)
	if p := r.Providers[ProviderCodex]; len(r.Switches) != 0 || p.ActiveID != a.ID || p.Epoch != rt.Epoch {
		t.Fatalf("untouched cancel changed selection: %+v", r)
	}
	if e = s.ValidateRuntime(rt); e != nil {
		t.Fatalf("untouched cancel invalidated runtime: %v", e)
	}
	op, e = s.BeginDeactivate(r.Revision, ProviderCodex, "manual", rows())
	if e != nil {
		t.Fatal(e)
	}
	op.Phase = "stopping"
	op.Sessions[0].Phase = "claimed"
	if e = s.SaveSwitch(op); e != nil {
		t.Fatal(e)
	}
	if e = s.CancelSwitch(op.ID); e != nil {
		t.Fatal(e)
	}
	op = snapshot(t, s).Switches[ProviderCodex]
	if !op.CancelRequested || op.Phase != "stopping" {
		t.Fatalf("cancel abandoned a claimed source: %+v", op)
	}
	finishDeactivation(t, s, stopDeactivation(t, s, op))
	r = snapshot(t, s)
	if len(r.Switches) != 0 || r.Providers[ProviderCodex].ActiveID != "" {
		t.Fatalf("claimed deactivation did not complete: %+v", r)
	}
}

func TestRemoveAfterDeactivateLeavesNoCodexGrant(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "secret-old"))
	activate(t, s, a)
	if _, e := durableReplace(s.root, "provider/codex/native/auth.json", codexCredential("a", "secret-latest").Auth, writeOps{}); e != nil {
		t.Fatal(e)
	}
	op := deactivateStopped(t, s, ProviderCodex, nil)
	r := snapshot(t, s)
	if _, e := s.Remove(r.Revision, a.ID); !errors.Is(e, ErrInUse) {
		t.Fatalf("removed the outgoing login during deactivation: %v", e)
	}
	finishDeactivation(t, s, op)
	r = snapshot(t, s)
	if _, e := s.Remove(r.Revision, a.ID); e != nil {
		t.Fatal(e)
	}
	e := filepath.WalkDir(s.path, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(raw), "secret-") {
			t.Errorf("removed login's token survives in %s", path)
		}
		return err
	})
	if e != nil {
		t.Fatal(e)
	}
}

func TestDeactivateRowsMayOmitConversationID(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
	b := add(t, s, ProviderCodex, "B", codexCredential("b", "b"))
	activate(t, s, a)
	r := snapshot(t, s)
	if _, e := s.BeginSwitch(r.Revision, ProviderCodex, b.ID, "manual", []Restart{{SourceID: "source"}}); !errors.Is(e, ErrIneligible) {
		t.Fatalf("switch admitted a row without an exact conversation: %v", e)
	}
	for name, rows := range map[string][]Restart{"no-source": {{ConversationID: "native"}}, "duplicate": {{SourceID: "source"}, {SourceID: "source", ConversationID: "native"}}} {
		if _, e := s.BeginDeactivate(r.Revision, ProviderCodex, "manual", rows); !errors.Is(e, ErrIneligible) {
			t.Fatalf("%s row admitted: %v", name, e)
		}
	}
	rows := []Restart{{SourceID: "fresh", Phase: "ready", ChildID: "child", ChildStopClaimed: true, LastError: "old"}, {SourceID: "resumable", ConversationID: "native"}}
	op, e := s.BeginDeactivate(r.Revision, ProviderCodex, "manual", rows)
	if e != nil {
		t.Fatal(e)
	}
	want := []Restart{{SourceID: "fresh", Phase: "waiting"}, {SourceID: "resumable", ConversationID: "native", Phase: "waiting"}}
	if got := snapshot(t, s).Switches[ProviderCodex].Sessions; !reflect.DeepEqual(got, want) || !reflect.DeepEqual(op.Sessions, want) {
		t.Fatalf("rows not normalised: %+v %+v", got, op.Sessions)
	}
	finishDeactivation(t, s, stopDeactivation(t, s, op))
	if r = snapshot(t, s); len(r.Switches) != 0 || r.Providers[ProviderCodex].ActiveID != "" {
		t.Fatalf("deactivation did not complete: %+v", r)
	}
}

func TestDeactivateCodexReplacesUnparseableNativeAuth(t *testing.T) {
	for name, body := range map[string]string{
		"truncated": `{"auth_mode":"chatgpt","tokens":{"account_id":"a","access_token":"live-token","refresh_token":"live-refresh"`,
		"duplicate": `{"tokens":{"account_id":"a","access_token":"live-token","refresh_token":"live-refresh"},"tokens":{}}`,
		"array":     `[{"tokens":{"account_id":"a","access_token":"live-token","refresh_token":"live-refresh"}}]`,
		"empty":     ``,
	} {
		t.Run(name, func(t *testing.T) {
			s := testStore(t)
			a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
			rt := activate(t, s, a)
			if _, e := durableReplace(s.root, "provider/codex/native/auth.json", []byte(body), writeOps{}); e != nil {
				t.Fatal(e)
			}
			deactivate(t, s, ProviderCodex, nil)
			r := snapshot(t, s)
			if r.Providers[ProviderCodex].ActiveID != "" || len(r.Switches) != 0 || find(&r, a.ID).AuthState != AuthNeedsLogin {
				t.Fatalf("deactivation did not complete: %+v", r)
			}
			raw, e := os.ReadFile(filepath.Join(rt.Home, "auth.json"))
			if e != nil || string(raw) != "{}" {
				t.Fatalf("unparseable native auth kept: %s %v", raw, e)
			}
		})
	}
}

func TestCodexLegacyTopLevelTokensRemoved(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
	rt := activate(t, s, a)
	plant := func() {
		t.Helper()
		doc := map[string]json.RawMessage{"unrelated": json.RawMessage(`"keep"`)}
		if e := json.Unmarshal(codexCredential("a", "a").Auth, &doc); e != nil {
			t.Fatal(e)
		}
		for _, key := range []string{"api_key", "access_token", "refresh_token", "id_token"} {
			doc[key] = json.RawMessage(`"legacy-secret"`)
		}
		b, _ := json.Marshal(doc)
		if _, e := durableReplace(s.root, "provider/codex/native/auth.json", b, writeOps{}); e != nil {
			t.Fatal(e)
		}
	}
	check := func(step string) {
		t.Helper()
		raw, e := os.ReadFile(filepath.Join(rt.Home, "auth.json"))
		if e != nil || strings.Contains(string(raw), "legacy-secret") || !strings.Contains(string(raw), "keep") {
			t.Fatalf("%s: legacy token keys survived or unrelated state lost: %s %v", step, raw, e)
		}
	}
	plant()
	deactivate(t, s, ProviderCodex, nil)
	check("deactivation")
	plant()
	activate(t, s, a)
	check("install")
}

func TestDeactivateClaudeUnreadableIdentityChecksSavedGrant(t *testing.T) {
	for _, kind := range []string{"missing", "empty", "unparseable", "saved-invalid"} {
		t.Run(kind, func(t *testing.T) {
			s := testStore(t)
			kc := s.keychain.(*fakeKeychain)
			a := add(t, s, ProviderClaude, "A", claudeCredential("a", "a"))
			rt := activate(t, s, a)
			name := "provider/claude/native/.claude.json"
			if kind == "missing" {
				if e := s.root.Remove(name); e != nil {
					t.Fatal(e)
				}
			} else if kind == "empty" {
				if _, e := durableReplace(s.root, name, nil, writeOps{}); e != nil {
					t.Fatal(e)
				}
			} else if _, e := durableReplace(s.root, name, []byte(`{"oauthAccount":{"accountUuid":"a"`), writeOps{}); e != nil {
				t.Fatal(e)
			}
			if kind == "saved-invalid" {
				if e := kc.Write(s.savedNativeHome(a.ID), []byte(`{"claudeAiOauth":{}}`)); e != nil {
					t.Fatal(e)
				}
			}
			meta, e := readPrivate(s.root, s.savedPath(a), maxCredentialBytes*2)
			if e != nil {
				t.Fatal(e)
			}
			op := deactivateStopped(t, s, ProviderClaude, nil)
			if e = s.CaptureOutgoing(op.ID); e != nil {
				t.Fatalf("unreadable identity blocked deactivation: %v", e)
			}
			r := snapshot(t, s)
			want := AuthReady
			if kind == "saved-invalid" {
				want = AuthNeedsLogin
			}
			if l := find(&r, a.ID); l.AuthState != want || l.CredentialRevision != a.CredentialRevision || r.Switches[ProviderClaude].Phase != "captured" {
				t.Fatalf("want %s: %+v %+v", want, l, r.Switches[ProviderClaude])
			}
			finishDeactivation(t, s, op)
			if after, e := readPrivate(s.root, s.savedPath(a), maxCredentialBytes*2); e != nil || string(after) != string(meta) {
				t.Fatalf("saved metadata rewritten: %s %v", after, e)
			}
			if kind == "unparseable" {
				if raw, e := os.ReadFile(filepath.Join(rt.Home, ".claude.json")); e != nil || string(raw) != "{}" {
					t.Fatalf("unparseable identity kept: %s %v", raw, e)
				}
			}
			if kind != "saved-invalid" {
				if e := s.ValidateRuntime(activate(t, s, a)); e != nil {
					t.Fatalf("kept login does not reactivate: %v", e)
				}
			}
		})
	}
}

// handClear drops the selection without a journal, as an owner editing the
// registry did before deactivation existed. The managed grant copy remains.
func handClear(t *testing.T, s *Store, provider string) ProviderState {
	t.Helper()
	r, e := s.mutate(snapshot(t, s).Revision, func(r *Registry) error {
		p := r.Providers[provider]
		p.ActiveID = ""
		r.Providers[provider] = p
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	return r.Providers[provider]
}

func TestDeactivateStopOnlyWithoutSelection(t *testing.T) {
	for _, p := range []string{ProviderCodex, ProviderClaude} {
		t.Run(p, func(t *testing.T) {
			s := testStore(t)
			c, name, identity := codexCredential("a", "a"), "auth.json", "account_id"
			if p == ProviderClaude {
				c, name, identity = claudeCredential("a", "a"), ".claude.json", "oauthAccount"
			}
			a := add(t, s, p, "A", c)
			rt := activate(t, s, a)
			state := handClear(t, s, p)
			r := snapshot(t, s)
			login := *find(&r, a.ID)
			meta, e := readPrivate(s.root, s.savedPath(a), maxCredentialBytes*2)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.BeginDeactivate(r.Revision, p, "manual", nil); !errors.Is(e, ErrIneligible) {
				t.Fatalf("stop-only admitted without discussions: %v", e)
			}
			op, e := s.BeginDeactivate(r.Revision, p, "manual", []Restart{{SourceID: "source"}})
			if e != nil {
				t.Fatal(e)
			}
			if !op.Deactivate || op.FromID != "" || op.ToID != "" || op.FromEpoch != state.Epoch || op.ToEpoch != state.Epoch+1 {
				t.Fatalf("bad stop-only journal %+v", op)
			}
			finishDeactivation(t, s, stopDeactivation(t, s, op))
			r = snapshot(t, s)
			if st := r.Providers[p]; st.ActiveID != "" || st.Epoch != state.Epoch+1 || len(r.Switches) != 0 {
				t.Fatalf("stop-only did not commit: %+v %+v", st, r.Switches)
			}
			if l := find(&r, a.ID); !reflect.DeepEqual(*l, login) {
				t.Fatalf("saved login modified: %+v", l)
			}
			if after, e := readPrivate(s.root, s.savedPath(a), maxCredentialBytes*2); e != nil || string(after) != string(meta) {
				t.Fatalf("saved metadata rewritten: %s %v", after, e)
			}
			raw, e := os.ReadFile(filepath.Join(rt.Home, name))
			if e != nil || strings.Contains(string(raw), identity) || strings.Contains(string(raw), `"a"`) {
				t.Fatalf("hand-cleared grant copy left: %s %v", raw, e)
			}
		})
	}
}
