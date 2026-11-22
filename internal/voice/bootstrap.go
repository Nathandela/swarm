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

var errCompetingManagers = errors.New("voice: more than one manager claims this conversation; reconcile before continuing")

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
	return bootstrapManagerRequest(ctx, dir, cfg, refs, endpoint, existing, client, managerLaunchRequest(cfg, refs), managerIntentName(cfg.ConversationID), "")
}

func bootstrapManagerRequest(ctx context.Context, dir string, cfg Config, refs ManagerBridgeRefs, endpoint string, existing *ManagerBinding, client managerBootstrapClient, req protocol.LaunchReq, file, retiredSource string) (managerSelection, error) {
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
		if retiredSource == "" || row.Status.Process != status.ProcessExited {
			hadManager = true
		}
		if row.Status.Process == status.ProcessRunning {
			live = append(live, row)
		}
	}
	if len(live) > 1 {
		return none, errCompetingManagers
	}
	if existing != nil {
		if len(live) != 1 {
			return none, errors.New("voice: manager is stopped or unavailable; explicit resume is required")
		}
		return findBootstrapManager(ctx, cfg, refs, endpoint, live[0].ID, existing, client)
	}
	var intent managerLaunchIntent
	raw, err := readVoiceFile(dir, file, MaxControlFrameBytes)
	if err == nil {
		if validateObjectMembers(raw, "version", "conversation_id", "operation_id", "request", "phase", "session_id") != nil || json.Unmarshal(raw, &intent) != nil || intent.Version != 1 || intent.ConversationID != cfg.ConversationID || intent.OperationID == "" {
			return none, ErrCorrupt
		}
		// Reconcile a pre-upgrade receipt using its immutable request. A prepared
		// intent never reached dispatch and may adopt the new launch configuration.
		if matchesLegacyManagerRequest(intent.Request, req) {
			if intent.Phase == "prepared" {
				intent.OperationID, err = protocol.NewLaunchOperationID()
				if err != nil {
					return none, err
				}
				intent.Request = req
			}
		} else if !sameManagerRequest(intent.Request, req) {
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
			return observeLaunchedManager(ctx, cfg, refs, endpoint, intent, client)
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
	if !managerLaunchReplayable(intent.OperationID) {
		// Only a prepared intent reaches dispatch. Executing intents reconcile above.
		intent.OperationID, err = protocol.NewLaunchOperationID()
		if err != nil {
			return none, err
		}
		if err := saveManagerIntent(dir, file, intent); err != nil {
			return none, err
		}
	}
	intent.Phase = "executing"
	if err := saveManagerIntent(dir, file, intent); err != nil {
		return none, err
	}
	id, _, err := client.LaunchOperation(intent.OperationID, intent.Request)
	if knownUnsentManagerLaunch(err) {
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
	return observeLaunchedManager(ctx, cfg, refs, endpoint, intent, client)
}

// An exited roster row diagnoses startup failure. Only the independently verified
// owner launch receipt can additionally prove that this attempt accepted no input.
type bootstrapExit struct {
	row protocol.SessionView
}

func (e *bootstrapExit) Error() string { return managerStartupExit(e.row).Error() }
func (e *bootstrapExit) Unwrap() error { return ErrDiscussionActivationUnavailable }

type failedResume struct {
	Attempt    int    `json:"attempt"`
	Session    string `json:"session"`
	Instance   string `json:"instance"`
	Diagnostic string `json:"diagnostic"`
}

func (e *failedResume) Error() string { return e.Diagnostic }
func (e *failedResume) Unwrap() error { return ErrDiscussionActivationUnavailable }

func observeLaunchedManager(ctx context.Context, cfg Config, refs ManagerBridgeRefs, endpoint string, intent managerLaunchIntent, client managerBootstrapClient) (managerSelection, error) {
	selected, err := findBootstrapManager(ctx, cfg, refs, endpoint, intent.SessionID, nil, client)
	if err == nil && intent.Request.Manager != nil && selected.Binding.NativeCLI != intent.Request.Manager.NativeCLI {
		return managerSelection{}, ErrStale
	}
	var exited *bootstrapExit
	if !errors.As(err, &exited) || intent.Request.Manager == nil || intent.Request.Manager.RetireOperationID == "" && !strings.HasPrefix(cfg.ConversationID, DiscussionConversationPrefix) {
		return selected, err
	}
	outcome, readErr := client.LaunchOperationStatus(intent.OperationID)
	target, applied := protocol.AppliedOwnerLaunchSession(endpoint, outcome)
	if readErr != nil || !applied || target != intent.SessionID || outcome.FailedManagerInstance == "" ||
		(exited.row.Capabilities != nil && exited.row.Capabilities.SessionInstance != outcome.FailedManagerInstance) {
		return selected, errors.Join(err, ErrControllerUncertain, readErr)
	}
	return selected, &failedResume{Session: target, Instance: outcome.FailedManagerInstance, Diagnostic: err.Error()}
}

func knownUnsentManagerLaunch(err error) bool {
	return errors.Is(err, protocol.ErrManagerLaunchPrerequisite) || errors.Is(err, ErrControllerUnsupported) || errors.Is(err, ErrStale)
}

func managerLaunchReplayable(id string) bool {
	// Leave time for private dispatch and daemon-side validation.
	return protocol.LaunchOperationReplayable(id, time.Now().Add(time.Minute))
}

func managerLaunchRequest(cfg Config, refs ManagerBridgeRefs) protocol.LaunchReq {
	options := map[string]string{}
	if cfg.Model != "" {
		options["model"] = cfg.Model
	}
	name := "Swarm manager"
	if strings.HasPrefix(cfg.ConversationID, DiscussionConversationPrefix) {
		name = cfg.Name
	}
	return protocol.LaunchReq{Agent: cfg.Provider, Name: name, Cwd: cfg.WorkDir, Options: options, Cols: 120, Rows: 32, Manager: &protocol.ManagerLaunchReq{NativeCLI: cfg.VMProjectAccess, ConversationID: cfg.ConversationID, BridgeSocket: refs.Socket, AuthFile: refs.AuthFile}}
}

func sameManagerRequest(a, b protocol.LaunchReq) bool {
	first, _ := json.Marshal(a)
	second, _ := json.Marshal(b)
	return string(first) == string(second)
}

func matchesLegacyManagerRequest(previous, current protocol.LaunchReq) bool {
	if previous.Manager == nil || previous.Manager.NativeCLI || current.Manager == nil || !current.Manager.NativeCLI {
		return false
	}
	legacy := *current.Manager
	legacy.NativeCLI = false
	current.Manager = &legacy
	return sameManagerRequest(previous, current)
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

// A successful launch already has an immutable session ID. Continue observing
// it on the next sync without applying the cooldown for new launch attempts.
var errManagerStillStarting = fmt.Errorf("%w: dispatcher session is still starting", ErrControllerNativeUnavailable)

func managerObservationWindow(cfg Config) time.Duration {
	if strings.HasPrefix(cfg.ConversationID, DiscussionConversationPrefix) {
		return 45 * time.Second
	}
	return 5 * time.Second
}

func findBootstrapManager(ctx context.Context, cfg Config, refs ManagerBridgeRefs, endpoint, id string, existing *ManagerBinding, client managerBootstrapClient) (managerSelection, error) {
	observeCtx, cancel := context.WithTimeout(ctx, managerObservationWindow(cfg))
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
				selected, err := selectBootstrapManager(observeCtx, cfg, refs, endpoint, existing, row, client)
				discussionDeadline := strings.HasPrefix(cfg.ConversationID, DiscussionConversationPrefix) &&
					errors.Is(err, context.DeadlineExceeded) && observeCtx.Err() != nil
				if !errors.Is(err, managerproof.ErrUnavailable) && !discussionDeadline {
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
				if strings.HasPrefix(cfg.ConversationID, DiscussionConversationPrefix) {
					return managerSelection{}, errManagerStillStarting
				}
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
	if !ok || prefix != endpoint || local == "" || strings.Contains(local, "/") || row.EndpointID != endpoint || row.Role != "manager" || row.ManagerConversationID != cfg.ConversationID || row.Agent != cfg.Provider || row.Cwd != cfg.WorkDir {
		return result, ErrStale
	}
	if row.Status.Process == status.ProcessExited {
		return result, &bootstrapExit{row}
	}
	if row.Status.Process != status.ProcessRunning {
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
	window := 20 * time.Second
	if strings.HasPrefix(cfg.ConversationID, DiscussionConversationPrefix) {
		window = managerObservationWindow(cfg)
	}
	probeCtx, cancel := context.WithTimeout(ctx, window)
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
	result = managerSelection{LocalID: local, Runtime: native, Binding: ManagerBinding{NativeCLI: native.NativeCLI, ConversationID: b.ConversationID, Provider: b.Provider, SessionID: b.Session, SessionInstance: b.SessionInstance, NativeSessionID: b.NativeSessionID, NativeGeneration: b.NativeGeneration, ObservationGeneration: b.ObservationGeneration, LoginEpoch: b.LoginEpoch}}
	if existing != nil && identity(*existing) != identity(result.Binding) {
		return result, ErrStale
	}
	return result, nil
}
