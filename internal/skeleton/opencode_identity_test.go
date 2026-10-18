package skeleton

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

const nativeOpenCodeID = "ses_eb0e24fcfffezuXlKs7DaFmVfm"

func TestComposeExternalOpenCodeIdentity(t *testing.T) {
	for _, id := range []string{nativeOpenCodeID, "ses_0123456789abcdef0123456789abcdef"} {
		got, err := composeLaunchSpec(daemon.LaunchSpec{
			AgentType: "opencode", Cwd: "/work",
			Options: map[string]string{protocol.OptionResumeConversationID: id},
		}, testEndpoint, "", srcGetter("", persist.Meta{}), stubLookPath)
		if err != nil {
			t.Fatalf("external native resume: %v", err)
		}
		if got.ConversationID != id || !reflect.DeepEqual(got.Argv, []string{"/abs/opencode", "--session", id}) {
			t.Fatalf("external resume lost native identity: %+v", got)
		}
	}
}

func TestExternalOpenCodeValidatesBeforeDeduplication(t *testing.T) {
	for _, tc := range []struct {
		id   string
		good bool
	}{
		{nativeOpenCodeID, true},
		{migratedConversationID, false},
		{"ses_abcdefghij", false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			r := newResumeAPIRig(t, "opencode", tc.id, nil)
			got, err := r.api.Launch(daemon.LaunchSpec{
				AgentType: "opencode", Cwd: "/work",
				Options: map[string]string{protocol.OptionResumeConversationID: tc.id},
			})
			if tc.good {
				if err != nil || got.ID != r.sourceID {
					t.Fatalf("exact native identity was not reused: %+v %v", got, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "identity is invalid") {
				t.Fatalf("invalid identity bypassed validation through an existing row: %+v %v", got, err)
			}
			if len(r.core.List()) != 1 {
				t.Fatal("identity validation must not reserve a successor")
			}
		})
	}
}

func TestStoredOpenCodeIdentityRefusesBeforeHistoryOrBinaryResolution(t *testing.T) {
	for _, id := range []string{migratedConversationID, "ses_abcdefghij", "--help"} {
		resolver := &fakeResumeHistoryResolver{result: resumeHistoryResult{Outcome: resumeHistoryFound, ConversationID: nativeOpenCodeID}}
		r := newResumeAPIRig(t, "opencode", id, resolver)
		_, err := r.api.Launch(r.launchSpec("opencode"))
		if err == nil || !strings.Contains(err.Error(), "saved conversation identity is invalid") {
			t.Fatalf("corrupt stored identity reached later resume phases: %v", err)
		}
		if resolver.callCount() != 0 || len(r.core.List()) != 1 {
			t.Fatal("corrupt identity triggered recovery or allocation")
		}
	}
}

func TestExternalUUIDProvidersStillRejectOpenCodeIdentity(t *testing.T) {
	for _, provider := range []string{"claude", "codex", "agy", "unknown"} {
		_, err := composeLaunchSpec(daemon.LaunchSpec{
			AgentType: provider, Cwd: "/work",
			Options: map[string]string{protocol.OptionResumeConversationID: nativeOpenCodeID},
		}, testEndpoint, "", srcGetter("", persist.Meta{}), stubLookPath)
		if err == nil || !strings.Contains(err.Error(), "identity is invalid") {
			t.Fatalf("provider %s accepted an OpenCode identity: %v", provider, err)
		}
	}
}
