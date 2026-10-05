package skeleton

import (
	"encoding/json"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/persist"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeClaudeResumeAcknowledgementRequiresOwnedOmission(t *testing.T) {
	for _, tc := range []struct {
		name, model         string
		flag, embargo, want bool
	}{
		{"native omission", "", true, true, true},
		{"present alias", "haiku", true, true, false},
		{"present other model", "claude-haiku-4-5", true, true, false},
		{"fallback not pinned", "", false, true, false},
		{"owner resumed", "", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, source, _, _ := nativeClaudeFailureFixture(t)
			candidate := source
			candidate.ID = "native-successor"
			binding := *source.AccountBinding
			binding.AccountID = strings.Repeat("b", 32)
			candidate.AccountBinding = &binding
			candidate.LaunchOptions = map[string]string{"model": "claude-sonnet-4-6"}
			incident := accounts.NewIncident("native-incident", source.AgentType, "claude-sonnet-4-6", 2)
			if tc.embargo {
				candidate.InputEmbargo = incident.ID
			}
			if tc.flag {
				candidate.Env = append(candidate.Env, "CLAUDE_CODE_NO_MODEL_FALLBACK=true")
				candidate.AccountClaudeFallback = &persist.ClaudeFallbackPolicy{RecoveryPinned: true}
			}
			manifest := accountHistoryManifest{SchemaVersion: 2, NativeContextRef: source.AccountProjectionRef, Provider: source.AgentType, NativeVersion: source.CLIIdentity.Version, ConversationID: source.ConversationID, Cwd: source.Cwd, Source: *source.AccountBinding, Destination: binding, Epoch: 1, Transaction: strings.Repeat("a", 64), Files: []accountHistoryFile{{Path: filepath.Join("projects", claude.New().ProjectDirName(source.Cwd), source.ConversationID+".jsonl"), SHA256: strings.Repeat("c", 64), Bytes: 1}}}
			manifest.SHA256 = accountManifestHash(manifest)
			rec := accountRotationRecord{Incident: incident, OriginalSource: source.ID, SourceID: source.ID, SourceBinding: *source.AccountBinding, Destination: &binding, State: accountLaunched, CandidateID: candidate.ID, ConversationID: source.ConversationID, Manifest: &manifest, FailureClass: "quota"}
			if e := m.persist(rec); e != nil {
				t.Fatal(e)
			}
			m.w.get = func(local string) (persist.Meta, bool) {
				if local == candidate.ID {
					return candidate, true
				}
				return source, local == source.ID
			}
			body, _ := json.Marshal(map[string]string{"session_id": source.ConversationID, "source": "resume", "model": tc.model})
			if e := m.NoteConversation(candidate.ID, source.ConversationID, body, 10); e != nil {
				t.Fatal(e)
			}
			if e := m.drainInbox(); e != nil {
				t.Fatal(e)
			}
			if m.w.state.AccountRotations[source.ID].ConversationProven != tc.want {
				t.Fatal("resume hook did not honor exact owned omission contract")
			}
		})
	}
}

func TestRecoveryFallbackOverrideRestoresOwnerPolicy(t *testing.T) {
	for _, original := range []*string{nil, new(string), func() *string { v := "false"; return &v }(), func() *string { v := "true"; return &v }()} {
		source := persist.Meta{AccountClaudeFallback: &persist.ClaudeFallbackPolicy{OwnerValue: original}}
		out := restoreClaudeOwnerFallback(source, []string{"HOME=/owner", "CLAUDE_CODE_NO_MODEL_FALLBACK=true"})
		var got *string
		for _, item := range out {
			if v, ok := strings.CutPrefix(item, "CLAUDE_CODE_NO_MODEL_FALLBACK="); ok {
				got = &v
			}
		}
		if (got == nil) != (original == nil) || got != nil && *got != *original {
			t.Fatal("recovery flag changed owner fallback policy")
		}
	}
}
