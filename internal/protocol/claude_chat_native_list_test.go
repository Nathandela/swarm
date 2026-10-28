package protocol

import (
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
	"strings"
	"testing"
)

func TestClaudeChatNativeActualClientListCarriesExactV2ReadCapability(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocked-readable", true: "owner-input-ready"}[ready], func(t *testing.T) {
			backend := newCapabilityTapStub()
			backend.metas = []persist.Meta{{SchemaVersion: 6, ID: "native-chat", AgentType: "claude", RequiredRuntime: "claude-chat", Status: status.Status{Process: status.ProcessRunning}, Cwd: "/work", ConversationID: "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a", ShimPID: 42, ShimStartTime: 99, LoginRuntime: &logins.Runtime{Provider: "claude", Epoch: 1, Identity: strings.Repeat("a", 64), Home: "/fixture/state/logins/provider/claude/profiles/" + strings.Repeat("a", 64)}, CLIIdentity: &persist.CLIIdentity{Path: "/fixture/claude", Version: "2.1.310", Fingerprint: strings.Repeat("b", 64)}}}
			expected := SessionCapabilities{Provider: "claude", ProviderVersion: "2.1.310", SessionInstance: "native-current-instance", NativeInteractionVersion: 2, NativeComposer: ready}
			backend.setRecord("native-chat", expected)
			client := dialClient(t, serveOwnerAPI(t, backend), nil)
			rows, err := client.List()
			if err != nil || len(rows) != 1 {
				t.Fatal("native roster unavailable", err)
			}
			row := rows[0]
			if row.RequiredRuntime != "claude-chat" || row.Capabilities == nil || *row.Capabilities != expected || row.Capabilities.Validate() != nil || row.Capabilities.AllowsTerminalWatch() || row.Capabilities.AllowsTerminalControl() || row.Capabilities.Interrupt {
				t.Fatalf("actual owner wire lost exact native capability: %+v", row)
			}
		})
	}
}

func TestClaudeChatNativeActualClientListNeverPublishesForeignReadProof(t *testing.T) {
	for _, kind := range []string{"wrong-provider", "wrong-version", "wrong-read-version", "no-instance", "terminal", "generic-chat", "ordinary-runtime"} {
		t.Run(kind, func(t *testing.T) {
			backend := newCapabilityTapStub()
			meta := persist.Meta{SchemaVersion: 6, ID: "native-chat", AgentType: "claude", RequiredRuntime: "claude-chat", Status: status.Status{Process: status.ProcessRunning}, Cwd: "/work", ConversationID: "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a", ShimPID: 42, ShimStartTime: 99, LoginRuntime: &logins.Runtime{Provider: "claude", Epoch: 1, Identity: strings.Repeat("a", 64), Home: "/fixture/state/logins/provider/claude/profiles/" + strings.Repeat("a", 64)}, CLIIdentity: &persist.CLIIdentity{Path: "/fixture/claude", Version: "2.1.310", Fingerprint: strings.Repeat("b", 64)}}
			proof := SessionCapabilities{Provider: "claude", ProviderVersion: "2.1.310", SessionInstance: "native-current-instance", NativeInteractionVersion: 2}
			switch kind {
			case "wrong-provider":
				proof.Provider = "fake"
			case "wrong-version":
				proof.ProviderVersion = "2.1.309"
			case "wrong-read-version":
				proof.NativeInteractionVersion = 1
			case "no-instance":
				proof.SessionInstance = ""
			case "terminal":
				proof.TerminalFallback = true
			case "generic-chat":
				proof.StructuredChat = true
			case "ordinary-runtime":
				meta.RequiredRuntime = ""
			}
			backend.metas = []persist.Meta{meta}
			backend.setRecord(meta.ID, proof)
			rows, err := dialClient(t, serveOwnerAPI(t, backend), nil).List()
			if err != nil || len(rows) != 1 {
				t.Fatal("roster row lost instead of refusing foreign proof", err)
			}
			if rows[0].Capabilities != nil {
				t.Fatal("foreign/inconsistent native read proof published")
			}
		})
	}
}
