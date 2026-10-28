package shim

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
)

// Storage already understands this managed schema6 row; this exercises the
// shim's actual reservation fence, without granting native image authority.
func TestClaudeChatNativeReservationExactSchema6(t *testing.T) {
	for _, kind := range []string{"exact", "schema5", "native-control", "foreign-conversation", "missing-conversation", "foreign-runtime", "foreign-cli", "foreign-cwd", "foreign-shim", "fake-agent", "retired", "embargo", "backend", "fixture", "duplicate-conversation", "conversation-alias"} {
		t.Run(kind, func(t *testing.T) {
			cfg, scope, meta := claudeClaimFixture(t)
			cfg.RequiredRuntime = persist.RequiredRuntimeClaudeChat
			cfg.ConversationID = claudeChatTestUUID
			meta.SchemaVersion = persist.ClaudeChatSchemaVersion
			meta.RequiredRuntime = cfg.RequiredRuntime
			meta.ConversationID = cfg.ConversationID
			switch kind {
			case "schema5":
				meta.SchemaVersion = persist.ClaudeSchemaVersion
			case "native-control":
				meta.RequiredRuntime = persist.RequiredRuntimeClaudeNativeControl
			case "foreign-conversation":
				meta.ConversationID = "883dfe99-775a-4aa2-985c-d91593ad987a"
			case "missing-conversation":
				cfg.ConversationID, meta.ConversationID = "", ""
			case "foreign-runtime":
				bad := *meta.LoginRuntime
				bad.Epoch++
				meta.LoginRuntime = &bad
			case "foreign-cli":
				bad := *meta.CLIIdentity
				bad.Fingerprint = strings.Repeat("c", 64)
				meta.CLIIdentity = &bad
			case "foreign-cwd":
				meta.Cwd = "/different"
			case "foreign-shim":
				meta.ShimPID, meta.ShimStartTime = os.Getpid()+1, 1
			case "fake-agent":
				meta.AgentType = "fake"
			case "retired":
				meta.ClaudeRetirement = &logins.ClaudeReservationRetirement{Version: 1}
			case "embargo":
				cfg.InputEmbargo, meta.InputEmbargo = "blocked", "blocked"
			case "backend":
				cfg.Backend = &BackendConfig{}
			case "fixture":
				cfg.ClaudeChatFixture = cfg.Argv[0]
			}
			raw, err := json.Marshal(meta)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "duplicate-conversation" {
				raw = append(raw[:len(raw)-1], []byte(`,"conversation_id":"`+cfg.ConversationID+`"}`)...)
			}
			if kind == "conversation-alias" {
				raw = []byte(strings.Replace(string(raw), `"conversation_id"`, `"Conversation_ID"`, 1))
			}
			if err := os.WriteFile(filepath.Join(cfg.SessionDir, "meta.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			err = scope.claimClaudeReservation(cfg)
			if kind != "exact" {
				if err == nil || scope.ownPreclaimDurable || scope.startAttempted {
					t.Fatal("invalid managed chat row gained custody")
				}
				if _, err := os.Stat(filepath.Join(cfg.SessionDir, NativeProcessFile)); !os.IsNotExist(err) {
					t.Fatal("refused row published a native record")
				}
				return
			}
			if err != nil || !scope.ownPreclaimDurable || scope.startAttempted || scope.info.PID != 0 {
				t.Fatalf("exact managed schema6 custody refused: %v", err)
			}
			if err := scope.validateClaudeStart(cfg); err != nil {
				t.Fatalf("claimed exact schema6 lost final fence: %v", err)
			}
			// A post-claim conversation substitution must not reuse the same preclaim.
			meta.ConversationID = "883dfe99-775a-4aa2-985c-d91593ad987a"
			raw, _ = json.Marshal(meta)
			if err := os.WriteFile(filepath.Join(cfg.SessionDir, "meta.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := scope.validateClaudeStart(cfg); err == nil {
				t.Fatal("conversation changed after custody")
			}
		})
	}
}
