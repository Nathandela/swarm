package skeleton

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// resumeHomeRig is the codex login fixture plus a real roster holding one ended
// source discussion and an ordinary native home, wired as serve.go wires them.
type resumeHomeRig struct {
	*loginFixture
	api               *coreAPI
	source            persist.Meta
	native            string // ordinary native home (~)
	managedTranscript string
}

func newResumeHomeRig(t *testing.T, managed, active bool) *resumeHomeRig {
	t.Helper()
	f := newLoginFixture(t)
	r := &resumeHomeRig{loginFixture: f, source: f.rows["source-1"]}
	r.source.Name = "auth refactor"
	r.source.Status.Process = status.ProcessExited
	r.source.LoginRuntime = nil
	if managed {
		r.source.LoginRuntime = &f.runtime
	}
	var err error
	if r.native, err = filepath.EvalSymlinks(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	unmanaged := newFilesystemResumeHistoryResolver(r.native, defaultResumeHistoryLimits)
	r.api = continuityAPI(t, r.source)
	r.api.logins = f.m
	r.api.historyResolver = &loginResumeHistoryResolver{fallback: unmanaged, stateDir: f.m.d.stateDir}
	if err = filepath.WalkDir(f.runtime.Home, func(path string, entry os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(entry.Name(), r.source.ConversationID+".jsonl") {
			r.managedTranscript = path
		}
		return err
	}); err != nil || r.managedTranscript == "" {
		t.Fatalf("missing managed transcript fixture: %v", err)
	}
	if !active {
		// Returning to native sign-in leaves the managed history on disk.
		_ = f.m.store.Close()
		state, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		empty, err := logins.Open(state)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = empty.Close() })
		f.m.store = empty
	}
	return r
}

func (r *resumeHomeRig) resumeSpec() daemon.LaunchSpec {
	return daemon.LaunchSpec{AgentType: logins.ProviderCodex, ClientEnv: []string{"HOME=/ordinary", "PATH=/bin"}, Options: map[string]string{protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, r.source.ID)}}
}

// writeNativeTranscript files the source's conversation in the ordinary ~/.codex.
func (r *resumeHomeRig) writeNativeTranscript(t *testing.T) string {
	t.Helper()
	rel, err := filepath.Rel(r.runtime.Home, r.managedTranscript)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(r.managedTranscript)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.native, ".codex", rel)
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// migrate rewrites the source as v0.25's offline migration left it on disk:
// bound to the managed home through its environment, with no login runtime.
// The migration wrote meta.json directly, past Save's environment allowlist.
func (r *resumeHomeRig) migrate(t *testing.T) {
	t.Helper()
	r.source.LoginRuntime = nil
	resolver := r.api.historyResolver
	r.api = continuityAPIWithConfig(t, func(cfg *daemon.Config) {
		path := filepath.Join(cfg.StateDir, r.source.ID, "meta.json")
		var meta map[string]any
		raw, err := os.ReadFile(path)
		if err == nil {
			err = json.Unmarshal(raw, &meta)
		}
		if err == nil {
			meta["env"] = []string{"HOME=/ordinary", "CODEX_HOME=" + r.runtime.Home}
			raw, err = json.Marshal(meta)
		}
		if err == nil {
			err = os.WriteFile(path, raw, 0600)
		}
		if err != nil {
			t.Fatal(err)
		}
	}, r.source)
	r.api.logins, r.api.historyResolver = r.m, resolver
}

// touch sets path's modification time to the given offset from the managed copy's.
func (r *resumeHomeRig) touch(t *testing.T, path string, offset time.Duration) {
	t.Helper()
	info, err := os.Stat(r.managedTranscript)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(path, info.ModTime().Add(offset), info.ModTime().Add(offset)); err != nil {
		t.Fatal(err)
	}
}

// unusedHistoryResolver fails a test that inspects provider history.
type unusedHistoryResolver struct{ t *testing.T }

func (r unusedHistoryResolver) Resolve(persist.Meta) resumeHistoryResult {
	r.t.Error("same-home resume inspected provider history")
	return resumeHistoryResult{Outcome: resumeHistoryUnsafe}
}
func (r unusedHistoryResolver) LocateTranscript(persist.Meta, string) (string, resumeHistoryOutcome) {
	r.t.Error("same-home resume inspected provider history")
	return "", resumeHistoryUnsafe
}

func TestResumeManagedDiscussionWithoutActiveLoginRefused(t *testing.T) {
	r := newResumeHomeRig(t, true, false)
	spec := r.resumeSpec()
	err := r.api.bindLoginLaunch(&spec)
	if !errors.Is(err, errResumeHomeMismatch) {
		t.Fatalf("managed history resumed in the ordinary native home: %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, `"auth refactor"`) || !strings.Contains(msg, "activate a saved login") {
		t.Fatalf("refusal is not actionable: %v", err)
	}
}

func TestResumeNativeDiscussionWhileLoginActiveRefusedWhenTranscriptMissing(t *testing.T) {
	r := newResumeHomeRig(t, false, true)
	r.writeNativeTranscript(t)
	if err := os.Remove(r.managedTranscript); err != nil {
		t.Fatal(err)
	}
	spec := r.resumeSpec()
	err := r.api.bindLoginLaunch(&spec)
	if !errors.Is(err, errResumeHomeMismatch) {
		t.Fatalf("native history resumed in the managed home: %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, `"auth refactor"`) || !strings.Contains(msg, "return to native sign-in") {
		t.Fatalf("refusal is not actionable: %v", err)
	}
}

func TestResumeNativeDiscussionWhileLoginActiveAllowedWhenTranscriptPresent(t *testing.T) {
	// The fixture's managed home already holds the conversation, as after import.
	r := newResumeHomeRig(t, false, true)
	spec := r.resumeSpec()
	if err := r.api.bindLoginLaunch(&spec); err != nil {
		t.Fatalf("resume refused although the managed home holds its history: %v", err)
	}
	if spec.LoginRuntime == nil || spec.LoginRuntime.Home != r.runtime.Home {
		t.Fatalf("resume not bound to the managed home: %+v", spec.LoginRuntime)
	}
}

// The one-time import copies history and leaves the native original behind, so a
// native copy of a managed discussion is a stale snapshot, never its history.
func TestResumeManagedDiscussionWithoutActiveLoginRefusedDespitePreImportNativeCopy(t *testing.T) {
	r := newResumeHomeRig(t, true, false)
	r.writeNativeTranscript(t)
	spec := r.resumeSpec()
	err := r.api.bindLoginLaunch(&spec)
	if !errors.Is(err, errResumeHomeMismatch) || !strings.Contains(err.Error(), "activate a saved login") {
		t.Fatalf("managed discussion resumed from its pre-import native copy: %v", err)
	}
}

// A native discussion may continue natively after its history was imported, so a
// managed copy older than the native transcript is stale.
func TestResumeNativeDiscussionWhileLoginActiveRequiresCurrentManagedCopy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		offset time.Duration
		grow   bool
		allow  bool
	}{
		{"native newer", time.Hour, false, false},
		{"same time, native larger", 0, true, false},
		{"same time and size", 0, false, true},
		{"native older", -time.Hour, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newResumeHomeRig(t, false, true)
			native := r.writeNativeTranscript(t)
			if tc.grow {
				f, err := os.OpenFile(native, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.WriteString("{}\n")
				if e := f.Close(); err == nil {
					err = e
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			r.touch(t, native, tc.offset)
			spec := r.resumeSpec()
			err := r.api.bindLoginLaunch(&spec)
			if tc.allow && err != nil {
				t.Fatalf("resume refused although the managed copy is current: %v", err)
			}
			if !tc.allow && (!errors.Is(err, errResumeHomeMismatch) || !strings.Contains(err.Error(), "return to native sign-in")) {
				t.Fatalf("resume admitted from a stale managed copy: %v", err)
			}
		})
	}
}

// v0.25 migrated discussions carry the managed home in their environment only.
func TestResumeMigratedDiscussionWithoutActiveLoginRefused(t *testing.T) {
	r := newResumeHomeRig(t, false, false)
	r.migrate(t)
	r.writeNativeTranscript(t)
	spec := r.resumeSpec()
	err := r.api.bindLoginLaunch(&spec)
	if !errors.Is(err, errResumeHomeMismatch) || !strings.Contains(err.Error(), "activate a saved login") {
		t.Fatalf("migrated discussion resumed in the ordinary native home: %v", err)
	}
}

func TestResumeMigratedDiscussionWithActiveLoginIsSameHome(t *testing.T) {
	r := newResumeHomeRig(t, false, true)
	r.migrate(t)
	r.api.historyResolver = unusedHistoryResolver{t}
	spec := r.resumeSpec()
	if err := r.api.bindLoginLaunch(&spec); err != nil {
		t.Fatalf("migrated discussion refused in its managed home: %v", err)
	}
	if spec.LoginRuntime == nil || spec.LoginRuntime.Home != r.runtime.Home {
		t.Fatalf("migrated discussion not bound to the managed home: %+v", spec.LoginRuntime)
	}
}

// Launch recovers an uncaptured conversation id from the source's own home before
// binding, so the guard judges the recovered conversation instead of skipping it.
func TestResumeLaunchChecksRecoveredConversationAgainstTargetHome(t *testing.T) {
	r := newResumeHomeRig(t, false, true)
	r.writeNativeTranscript(t)
	if err := os.Remove(r.managedTranscript); err != nil {
		t.Fatal(err)
	}
	source := r.source
	source.ConversationID = ""
	api := continuityAPI(t, source)
	api.logins, api.historyResolver = r.api.logins, r.api.historyResolver
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	spec := r.resumeSpec()
	spec.ClientEnv = []string{"PATH=" + bin}
	if _, err := api.Launch(spec); !errors.Is(err, errResumeHomeMismatch) {
		t.Fatalf("recovered native conversation resumed in the managed home: %v", err)
	}
	if got, _ := api.core.Get(source.ID); got.ConversationID != r.source.ConversationID {
		t.Fatalf("guard ran before conversation recovery: %q", got.ConversationID)
	}
	if len(api.List()) != 1 {
		t.Fatal("refused resume launched a discussion")
	}
}

func TestResumeSameHomeIsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name    string
		managed bool
	}{{"managed", true}, {"native", false}} {
		t.Run(tc.name, func(t *testing.T) {
			r := newResumeHomeRig(t, tc.managed, tc.managed)
			r.api.historyResolver = unusedHistoryResolver{t}
			spec := r.resumeSpec()
			if err := r.api.bindLoginLaunch(&spec); err != nil {
				t.Fatalf("same-home resume refused: %v", err)
			}
			if (spec.LoginRuntime != nil) != tc.managed || tc.managed && spec.LoginRuntime.Home != r.runtime.Home {
				t.Fatalf("same-home resume rebound: %+v", spec.LoginRuntime)
			}
		})
	}
}

// A full hand-off launch spawns a provider; this drives its composition and the
// login binding of the composed spec, which together decide where it reads and runs.
func TestHandoffFromManagedSourceWithoutActiveLoginLocatesTranscript(t *testing.T) {
	r := newResumeHomeRig(t, true, false)
	spec := daemon.LaunchSpec{AgentType: logins.ProviderCodex, Options: map[string]string{protocol.OptionHandoffFrom: protocol.NamespacedID(testEndpoint, r.source.ID)}}
	composed, err := composeHandsOffLaunch(spec, testEndpoint, r.api.core.Get, r.api.historyResolver)
	if err != nil {
		t.Fatalf("hand-off could not read the managed history: %v", err)
	}
	if !strings.Contains(composed.InitialPrompt, r.managedTranscript) {
		t.Fatalf("hand-off prompt does not point at the managed transcript %q", r.managedTranscript)
	}
	if err = r.api.bindLoginLaunch(&composed); err != nil || composed.LoginRuntime != nil {
		t.Fatalf("hand-off successor not launched with native sign-in: %+v %v", composed.LoginRuntime, err)
	}
}
