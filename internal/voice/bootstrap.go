package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

type managerBootstrapClient interface {
	List() ([]protocol.SessionView, error)
	LaunchOperation(string, protocol.LaunchReq) (string, string, error)
	LaunchOperationStatus(string) (schema.OperationOutcomeView, error)
	ManagerRuntime(string, string) (protocol.ManagerRuntimeStatus, error)
}

type managerLaunchIntent struct {
	Version        int                `json:"version"`
	ConversationID string             `json:"conversation_id"`
	OperationID    string             `json:"operation_id"`
	Request        protocol.LaunchReq `json:"request"`
	Phase          string             `json:"phase"`
	SessionID      string             `json:"session_id,omitempty"`
}

type managerSelection struct {
	LocalID string
	Binding ManagerBinding
	Runtime protocol.ManagerRuntimeStatus
}

func managerIntentName(conversation string) string {
	return "manager-" + hashBytes([]byte(conversation)) + ".json"
}

// bootstrapManager never creates a replacement for a previously bound manager.
// A pending launch reconciles its exact durable operation without dispatching it again.
func bootstrapManager(ctx context.Context, dir string, cfg Config, refs ManagerBridgeRefs, endpoint string, existing *ManagerBinding, client managerBootstrapClient) (managerSelection, error) {
	var none managerSelection
	if err := ctx.Err(); err != nil {
		return none, err
	}
	if cfg.Validate() != nil || client == nil || endpoint == "" || refs.Socket == "" || refs.AuthFile == "" {
		return none, ErrInvalid
	}
	if existing != nil && (existing.ConversationID != cfg.ConversationID || existing.Provider != cfg.Provider) {
		return none, ErrConflict
	}
	rows, err := client.List()
	if err != nil {
		return none, err
	}
	var live []protocol.SessionView
	hadManager := false
	for _, row := range rows {
		if row.Role != "manager" || row.ManagerConversationID != cfg.ConversationID {
			continue
		}
		hadManager = true
		if row.Status.Process == status.ProcessRunning {
			live = append(live, row)
		}
	}
	if len(live) > 1 {
		return none, errors.New("voice: more than one manager claims this conversation; reconcile before continuing")
	}
	if existing != nil {
		if len(live) != 1 {
			return none, errors.New("voice: manager is stopped or unavailable; explicit resume is required")
		}
		return findBootstrapManager(ctx, cfg, refs, endpoint, live[0].ID, existing, client)
	}
	req := managerLaunchRequest(cfg, refs)
	file := managerIntentName(cfg.ConversationID)
	var intent managerLaunchIntent
	raw, err := readVoiceFile(dir, file, MaxControlFrameBytes)
	if err == nil {
		if validateObjectMembers(raw, "version", "conversation_id", "operation_id", "request", "phase", "session_id") != nil || json.Unmarshal(raw, &intent) != nil || intent.Version != 1 || intent.ConversationID != cfg.ConversationID || intent.OperationID == "" {
			return none, ErrCorrupt
		}
		original, _ := json.Marshal(intent.Request)
		current, _ := json.Marshal(req)
		if string(original) != string(current) {
			return none, ErrConflict
		}
		if intent.Phase != "prepared" && intent.Phase != "executing" && intent.Phase != "applied" {
			return none, ErrCorrupt
		}
		if intent.Phase == "prepared" && (intent.SessionID != "" || hadManager) {
			return none, ErrConflict
		}
		if intent.Phase == "executing" {
			outcome, err := client.LaunchOperationStatus(intent.OperationID)
			if err != nil {
				return none, err
			}
			target, applied := protocol.AppliedOwnerLaunchSession(endpoint, outcome)
			if !applied {
				return none, ErrControllerUncertain
			}
			if (intent.SessionID != "" && intent.SessionID != target) || (len(live) == 1 && live[0].ID != target) {
				return none, ErrStale
			}
			intent.SessionID, intent.Phase = target, "applied"
			if err := saveManagerIntent(dir, file, intent); err != nil {
				return none, err
			}
		}
		if intent.Phase == "applied" {
			if intent.SessionID == "" || (len(live) == 1 && live[0].ID != intent.SessionID) {
				return none, ErrStale
			}
			return findBootstrapManager(ctx, cfg, refs, endpoint, intent.SessionID, nil, client)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if hadManager {
			return none, ErrControllerUncertain
		}
		op, err := protocol.NewLaunchOperationID()
		if err != nil {
			return none, err
		}
		intent = managerLaunchIntent{Version: 1, ConversationID: cfg.ConversationID, OperationID: op, Request: req, Phase: "prepared"}
		if err := saveManagerIntent(dir, file, intent); err != nil {
			return none, err
		}
	} else {
		return none, err
	}
	if err := ctx.Err(); err != nil {
		return none, err
	}
	intent.Phase = "executing"
	if err := saveManagerIntent(dir, file, intent); err != nil {
		return none, err
	}
	id, _, err := client.LaunchOperation(intent.OperationID, intent.Request)
	if errors.Is(err, ErrControllerUnsupported) || errors.Is(err, ErrStale) {
		// The protocol client's capability and endpoint checks run before dispatch.
		intent.Phase = "prepared"
		if saveErr := saveManagerIntent(dir, file, intent); saveErr != nil {
			return none, errors.Join(err, saveErr)
		}
		return none, err
	}
	if err != nil || id == "" {
		return none, fmt.Errorf("%w (launch %s)", ErrControllerUncertain, intent.OperationID)
	}
	intent.Phase = "applied"
	intent.SessionID = id
	if err := saveManagerIntent(dir, file, intent); err != nil {
		return none, err
	}
	return findBootstrapManager(ctx, cfg, refs, endpoint, id, nil, client)
}

func managerLaunchRequest(cfg Config, refs ManagerBridgeRefs) protocol.LaunchReq {
	options := map[string]string{}
	if cfg.Model != "" {
		options["model"] = cfg.Model
	}
	return protocol.LaunchReq{Agent: cfg.Provider, Name: "Swarm manager", Cwd: cfg.WorkDir, Options: options, Cols: 120, Rows: 32, Manager: &protocol.ManagerLaunchReq{ConversationID: cfg.ConversationID, BridgeSocket: refs.Socket, AuthFile: refs.AuthFile}}
}

func saveManagerIntent(dir, file string, intent managerLaunchIntent) error {
	raw, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	if len(raw) > MaxControlFrameBytes {
		return ErrCapacity
	}
	return writeVoiceFile(dir, file, raw)
}

func findBootstrapManager(ctx context.Context, cfg Config, refs ManagerBridgeRefs, endpoint, id string, existing *ManagerBinding, client managerBootstrapClient) (managerSelection, error) {
	observeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	observedExact := false
	for {
		rows, err := client.List()
		if err != nil {
			return managerSelection{}, err
		}
		for _, row := range rows {
			if row.ID == id {
				observedExact = true
				selected, err := selectBootstrapManager(ctx, cfg, refs, endpoint, existing, row, client)
				if !errors.Is(err, managerproof.ErrUnavailable) {
					return selected, err
				}
			}
		}
		select {
		case <-observeCtx.Done():
			if err := ctx.Err(); err != nil {
				return managerSelection{}, err
			}
			if observedExact {
				return managerSelection{}, ErrControllerNativeUnavailable
			}
			return managerSelection{}, ErrControllerUncertain
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func selectBootstrapManager(ctx context.Context, cfg Config, refs ManagerBridgeRefs, endpoint string, existing *ManagerBinding, row protocol.SessionView, client managerBootstrapClient) (managerSelection, error) {
	var result managerSelection
	prefix, local, ok := strings.Cut(row.ID, "/")
	if !ok || prefix != endpoint || local == "" || strings.Contains(local, "/") || row.EndpointID != endpoint || row.Role != "manager" || row.ManagerConversationID != cfg.ConversationID || row.Agent != cfg.Provider || row.Cwd != cfg.WorkDir || row.Status.Process != status.ProcessRunning {
		return result, ErrStale
	}
	if row.Capabilities == nil || row.Capabilities.SessionInstance == "" {
		return result, managerproof.ErrUnavailable
	}
	if existing != nil && (existing.SessionID != row.ID || existing.SessionInstance != row.Capabilities.SessionInstance) {
		return result, ErrStale
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var native protocol.ManagerRuntimeStatus
	for {
		var err error
		native, err = client.ManagerRuntime(row.ID, row.Capabilities.SessionInstance)
		if !errors.Is(err, managerproof.ErrUnavailable) {
			if err != nil {
				return result, err
			}
			break
		}
		select {
		case <-probeCtx.Done():
			return result, probeCtx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	b := native.Binding
	if b.Validate() != nil || b.Session != row.ID || b.SessionInstance != row.Capabilities.SessionInstance || b.ConversationID != cfg.ConversationID || b.Provider != cfg.Provider || native.BridgeSocket != refs.Socket || native.AuthFile != refs.AuthFile {
		return result, ErrStale
	}
	result = managerSelection{LocalID: local, Runtime: native, Binding: ManagerBinding{ConversationID: b.ConversationID, Provider: b.Provider, SessionID: b.Session, SessionInstance: b.SessionInstance, NativeSessionID: b.NativeSessionID, NativeGeneration: b.NativeGeneration, ObservationGeneration: b.ObservationGeneration, LoginEpoch: b.LoginEpoch}}
	if existing != nil && identity(*existing) != identity(result.Binding) {
		return result, ErrStale
	}
	return result, nil
}
