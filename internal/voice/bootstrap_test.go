package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

type bootstrapFixture struct {
	rows         []protocol.SessionView
	runtime      protocol.ManagerRuntimeStatus
	launches     int
	lost         bool
	refusal      error
	outcome      schema.OperationOutcomeView
	beforeLaunch func(string, protocol.LaunchReq)
}

func (f *bootstrapFixture) List() ([]protocol.SessionView, error) {
	return append([]protocol.SessionView(nil), f.rows...), nil
}
func (f *bootstrapFixture) LaunchOperation(op string, req protocol.LaunchReq) (string, string, error) {
	f.launches++
	if f.beforeLaunch != nil {
		f.beforeLaunch(op, req)
	}
	if f.refusal != nil {
		return "", "", f.refusal
	}
	f.rows = []protocol.SessionView{{ID: "endpoint/manager", EndpointID: "endpoint", Role: "manager", ManagerConversationID: req.Manager.ConversationID, Agent: req.Agent, Cwd: req.Cwd, Status: status.Status{Process: status.ProcessRunning}, Capabilities: &protocol.SessionCapabilities{SessionInstance: "instance"}}}
	if f.lost {
		return "", "", errors.New("lost reply")
	}
	return "endpoint/manager", "Swarm manager", nil
}
func (f *bootstrapFixture) LaunchOperationStatus(string) (schema.OperationOutcomeView, error) {
	return f.outcome, nil
}
func (f *bootstrapFixture) ManagerRuntime(string, string) (protocol.ManagerRuntimeStatus, error) {
	return f.runtime, nil
}

func bootstrapNativeFixture(c Config, refs ManagerBridgeRefs) *bootstrapFixture {
	return &bootstrapFixture{runtime: protocol.ManagerRuntimeStatus{Binding: protocol.ManagerRuntimeBinding{Session: "endpoint/manager", SessionInstance: "instance", ConversationID: c.ConversationID, Provider: c.Provider, NativeSessionID: "native-session", NativeGeneration: "native-generation", ObservationGeneration: "observer", LoginEpoch: "account"}, BridgeSocket: refs.Socket, AuthFile: refs.AuthFile, ReadyForInput: true}}
}

func TestBootstrapPersistsExactManagerIntentBeforeLaunch(t *testing.T) {
	dir := privateConfigDir(t)
	cfg := testConfig()
	refs := ManagerBridgeRefs{Socket: "/private/bridge.sock", AuthFile: "/private/bridge.key"}
	f := bootstrapNativeFixture(cfg, refs)
	f.beforeLaunch = func(op string, req protocol.LaunchReq) {
		raw, err := readVoiceFile(dir, managerIntentName(cfg.ConversationID), MaxControlFrameBytes)
		var intent managerLaunchIntent
		if err != nil || json.Unmarshal(raw, &intent) != nil || intent.OperationID != op || intent.Phase != "executing" {
			t.Fatal("launch preceded durable intent", err)
		}
		if req.Env != nil || req.Manager == nil || req.InitialPrompt != "" {
			t.Fatal("manager launch exported environment or started an unadmitted turn")
		}
	}
	selected, err := bootstrapManager(context.Background(), dir, cfg, refs, "endpoint", nil, f)
	if err != nil || selected.LocalID != "manager" || selected.Binding.SessionID != "endpoint/manager" || f.launches != 1 {
		t.Fatalf("bootstrap: %+v launches=%d %v", selected, f.launches, err)
	}
	if _, err := bootstrapManager(context.Background(), dir, cfg, refs, "endpoint", &selected.Binding, f); err != nil || f.launches != 1 {
		t.Fatal("existing manager was relaunched", err)
	}
}

func TestBootstrapLostReplyAdoptsOnlyExactTypedManager(t *testing.T) {
	dir := privateConfigDir(t)
	cfg := testConfig()
	refs := ManagerBridgeRefs{Socket: "/private/bridge.sock", AuthFile: "/private/bridge.key"}
	f := bootstrapNativeFixture(cfg, refs)
	f.lost = true
	if _, err := bootstrapManager(context.Background(), dir, cfg, refs, "endpoint", nil, f); err == nil {
		t.Fatal("lost response reported confirmed")
	}
	f.lost = false
	if _, err := bootstrapManager(context.Background(), dir, cfg, refs, "endpoint", nil, f); !errors.Is(err, ErrControllerUncertain) {
		t.Fatalf("adopted same-conversation row without exact launch receipt: %v", err)
	}
	f.outcome = schema.OperationOutcomeView{State: schema.OutcomeApplied, SessionID: "endpoint/different-manager"}
	if _, err := bootstrapManager(context.Background(), dir, cfg, refs, "endpoint", nil, f); !errors.Is(err, ErrStale) {
		t.Fatalf("adopted row from a different operation: %v", err)
	}
	f.outcome = schema.OperationOutcomeView{State: schema.OutcomeApplied, SessionID: "endpoint/manager"}
	if _, err := bootstrapManager(context.Background(), dir, cfg, refs, "endpoint", nil, f); err != nil || f.launches != 1 {
		t.Fatal("lost reply created a replacement", err)
	}
	f.runtime.AuthFile = "/other/bridge.key"
	if _, err := bootstrapManager(context.Background(), dir, cfg, refs, "endpoint", nil, f); !errors.Is(err, ErrStale) || f.launches != 1 {
		t.Fatal("foreign bridge adopted", err)
	}
}

func TestBootstrapDoesNotReplaceStoppedOrAmbiguousManager(t *testing.T) {
	dir := privateConfigDir(t)
	cfg := testConfig()
	refs := ManagerBridgeRefs{Socket: "/private/bridge.sock", AuthFile: "/private/bridge.key"}
	f := bootstrapNativeFixture(cfg, refs)
	selected, err := bootstrapManager(context.Background(), dir, cfg, refs, "endpoint", nil, f)
	if err != nil {
		t.Fatal(err)
	}
	f.rows = nil
	if _, err := bootstrapManager(context.Background(), dir, cfg, refs, "endpoint", &selected.Binding, f); err == nil || f.launches != 1 {
		t.Fatal("lost manager replaced")
	}
	row := protocol.SessionView{ID: "endpoint/manager", EndpointID: "endpoint", Role: "manager", ManagerConversationID: cfg.ConversationID, Agent: cfg.Provider, Status: status.Status{Process: status.ProcessRunning}, Capabilities: &protocol.SessionCapabilities{SessionInstance: "instance"}}
	f.rows = []protocol.SessionView{row, row}
	if _, err := bootstrapManager(context.Background(), dir, cfg, refs, "endpoint", nil, f); !errors.Is(discussionActivationError(err), ErrDiscussionActivationUnavailable) || !errors.Is(err, errCompetingManagers) || f.launches != 1 {
		t.Fatalf("ambiguous manager selected or missing owner notice: %v", err)
	}
}

func TestBootstrapRetryRequiresCorrelatedPreEffectPrerequisite(t *testing.T) {
	for _, known := range []bool{true, false} {
		name := "typed pre-effect refusal"
		if !known {
			name = "older wire generic refusal"
		}
		t.Run(name, func(t *testing.T) {
			dir := privateConfigDir(t)
			cfg := testConfig()
			refs := ManagerBridgeRefs{Socket: "/private/bridge.sock", AuthFile: "/private/bridge.key"}
			f := bootstrapNativeFixture(cfg, refs)
			f.refusal = errors.New(protocol.ErrManagerLaunchPrerequisite.Error())
			if known {
				f.refusal = protocol.ErrManagerLaunchPrerequisite
			}
			expired := fmt.Sprintf("launch:%d:%s", time.Now().Add(-2*time.Hour).UnixMilli(), strings.Repeat("a", 32))
			file := managerIntentName(cfg.ConversationID)
			intent := managerLaunchIntent{Version: 1, ConversationID: cfg.ConversationID, OperationID: expired,
				Phase: "prepared", Request: managerLaunchRequest(cfg, refs)}
			if err := saveManagerIntent(dir, file, intent); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			_, err := bootstrapManager(ctx, dir, cfg, refs, "endpoint", nil, f)
			if known && !errors.Is(err, protocol.ErrManagerLaunchPrerequisite) || !known && !errors.Is(err, ErrControllerUncertain) {
				t.Fatalf("unexpected refusal: %v", err)
			}
			raw, err := readVoiceFile(dir, file, MaxControlFrameBytes)
			var saved managerLaunchIntent
			if err != nil || json.Unmarshal(raw, &saved) != nil || saved.OperationID == expired {
				t.Fatalf("expired proven-unsent operation did not renew: %+v %v", saved, err)
			}
			if known && saved.Phase != "prepared" || !known && saved.Phase != "executing" {
				t.Fatalf("wrong custody after refusal: %+v", saved)
			}
			f.refusal = nil
			_, err = bootstrapManager(ctx, dir, cfg, refs, "endpoint", nil, f)
			if known && (err != nil || f.launches != 2) || !known && (!errors.Is(err, ErrControllerUncertain) || f.launches != 1) {
				t.Fatalf("wrong retry behavior: launches=%d, %v", f.launches, err)
			}
		})
	}
}

func TestVMUpgradeReconcilesInFlightLegacyManagerWithoutChangingItsPolicy(t *testing.T) {
	dir, cfg := privateConfigDir(t), testConfig()
	cfg.ConversationID = DiscussionConversationPrefix + "fixture"
	refs := ManagerBridgeRefs{Socket: "/private/bridge.sock", AuthFile: "/private/bridge.key"}
	f := bootstrapNativeFixture(cfg, refs)
	f.lost = true
	if _, err := bootstrapManager(t.Context(), dir, cfg, refs, "endpoint", nil, f); !errors.Is(err, ErrControllerUncertain) {
		t.Fatal(err)
	}
	cfg.VMProjectAccess = true
	if _, err := bootstrapManager(t.Context(), dir, cfg, refs, "endpoint", nil, f); !errors.Is(err, ErrControllerUncertain) || f.launches != 1 {
		t.Fatalf("changed unresolved launch: %v", err)
	}
	f.outcome = schema.OperationOutcomeView{State: schema.OutcomeApplied, SessionID: "endpoint/manager"}
	selected, err := bootstrapManager(t.Context(), dir, cfg, refs, "endpoint", nil, f)
	if err != nil || f.launches != 1 || selected.Binding.NativeCLI {
		t.Fatalf("upgrade took over running legacy manager: %+v %v", selected, err)
	}
}

func TestVMUpgradeReplacesProvenUnsentLegacyManagerOperation(t *testing.T) {
	dir, cfg := privateConfigDir(t), testConfig()
	cfg.ConversationID = DiscussionConversationPrefix + "fixture"
	refs := ManagerBridgeRefs{Socket: "/private/bridge.sock", AuthFile: "/private/bridge.key"}
	f := bootstrapNativeFixture(cfg, refs)
	id, err := protocol.NewLaunchOperationID()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveManagerIntent(dir, managerIntentName(cfg.ConversationID), managerLaunchIntent{Version: 1, ConversationID: cfg.ConversationID, Request: managerLaunchRequest(cfg, refs), OperationID: id, Phase: "prepared"}); err != nil {
		t.Fatal(err)
	}
	cfg.VMProjectAccess = true
	f.beforeLaunch = func(op string, req protocol.LaunchReq) {
		if op == id || !req.Manager.NativeCLI {
			t.Fatal("changed payload reused prior operation")
		}
	}
	f.runtime.NativeCLI = true
	selected, err := bootstrapManager(t.Context(), dir, cfg, refs, "endpoint", nil, f)
	if err != nil || !selected.Binding.NativeCLI || f.launches != 1 {
		t.Fatalf("prepared launch %+v: %v", selected, err)
	}
}
