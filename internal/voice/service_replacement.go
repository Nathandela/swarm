package voice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

var errManagerReplacementAdopted = errors.New("voice: fresh manager adopted")

// ManagerReplacement is the private control receipt for an exact owner launch.
type ManagerReplacement struct {
	Previous    ManagerBinding `json:"previous"`
	OperationID string         `json:"operation_id"`
	Phase       string         `json:"phase"`
	SessionID   string         `json:"session_id,omitempty"`
	Attention   string         `json:"attention,omitempty"`
}

type replacementIntent struct {
	Version int                `json:"version"`
	Receipt ManagerReplacement `json:"receipt"`
	Request protocol.LaunchReq `json:"request"`
}

type managerReplacement struct {
	dir, endpoint string
	config        Config
	refs          ManagerBridgeRefs
	runtime       managerServiceRuntime
	wake          chan struct{}

	mu        sync.Mutex
	attention string
}

func (r *managerReplacement) file() string {
	return "replace-" + hashBytes([]byte(r.config.ConversationID)) + ".json"
}

func (r *managerReplacement) read() (replacementIntent, error) {
	var intent replacementIntent
	raw, err := readVoiceFile(r.dir, r.file(), MaxControlFrameBytes)
	if errors.Is(err, os.ErrNotExist) {
		if _, markerErr := readVoiceFile(r.dir, r.file()+".initialized", 32); !errors.Is(markerErr, os.ErrNotExist) {
			return intent, ErrCorrupt
		}
		return intent, ErrNotFound
	}
	if err != nil {
		return intent, err
	}
	if validateObjectMembers(raw, "version", "receipt", "request") != nil || json.Unmarshal(raw, &intent) != nil || intent.Version != 1 {
		return intent, ErrCorrupt
	}
	receipt := intent.Receipt
	previousEndpoint, _, valid := protocol.ParseID(receipt.Previous.SessionID)
	if !valid || previousEndpoint != r.endpoint || receipt.Previous.Generation == 0 || receipt.Previous.ConversationID != r.config.ConversationID ||
		receipt.Previous.Provider != r.config.Provider || !configText(receipt.OperationID, 128) || receipt.Attention != "" {
		return intent, ErrCorrupt
	}
	switch receipt.Phase {
	case "prepared", "executing":
		if receipt.SessionID != "" {
			return intent, ErrCorrupt
		}
	case "applied":
		nextEndpoint, _, valid := protocol.ParseID(receipt.SessionID)
		if !valid || nextEndpoint != r.endpoint || receipt.SessionID == receipt.Previous.SessionID {
			return intent, ErrCorrupt
		}
	default:
		return intent, ErrCorrupt
	}
	return intent, nil
}

func (r *managerReplacement) write(intent replacementIntent) error {
	payload, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	if len(payload) > MaxControlFrameBytes {
		return ErrCapacity
	}
	return writeVoiceFile(r.dir, r.file(), payload)
}

func (r *managerReplacement) begin(intent replacementIntent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, err := r.read()
	if err != nil {
		return err
	}
	if current.Receipt != intent.Receipt {
		return ErrStale
	}
	if !managerLaunchReplayable(intent.Receipt.OperationID) {
		return ErrExpired
	}
	if err = writeVoiceFile(r.dir, r.file()+".initialized", []byte("replacement-v1\n")); err != nil {
		return err
	}
	intent.Receipt.Phase = "executing"
	return r.write(intent)
}

func (r *managerReplacement) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *managerReplacement) checkRoster(previous ManagerBinding, candidate string) error {
	rows, err := r.runtime.List()
	if err != nil {
		return err
	}
	oldExited := false
	for _, row := range rows {
		if row.ID == previous.SessionID {
			if row.Role != "manager" || row.ManagerConversationID != previous.ConversationID || row.Agent != previous.Provider ||
				row.Capabilities == nil || row.Capabilities.SessionInstance != previous.SessionInstance {
				return ErrStale
			}
			if row.Status.Process != status.ProcessExited {
				return ErrControllerUncertain
			}
			oldExited = true
		} else if row.Role == "manager" && row.ManagerConversationID == previous.ConversationID &&
			!discussionManagerEnded(row) && row.ID != candidate {
			return ErrConflict
		}
	}
	if !oldExited {
		return ErrNotFound
	}
	return nil
}

func (c *managerControl) replacementStatus() (ManagerReplacement, error) {
	r := c.replacement
	if r == nil {
		return ManagerReplacement{}, ErrNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	intent, err := r.read()
	if err != nil {
		return ManagerReplacement{}, err
	}
	receipt := intent.Receipt
	if receipt.Previous.Generation > c.binding.Generation {
		return ManagerReplacement{}, ErrStale
	}
	if receipt.SessionID == c.binding.SessionID && receipt.Phase == "applied" {
		receipt.Phase = "adopted"
	} else if identity(receipt.Previous) != identity(c.binding) {
		return ManagerReplacement{}, ErrStale
	}
	receipt.Attention = r.attention
	return receipt, nil
}

func (c *managerControl) queueReplacement(ctx context.Context, expected ManagerBinding) (ManagerReplacement, error) {
	var none ManagerReplacement
	r := c.replacement
	if r == nil || expected != c.binding {
		return none, ErrStale
	}
	if err := c.store.checkControllerBinding(expected); !errors.Is(err, managerproof.ErrQuarantined) {
		if err == nil {
			return none, ErrConflict
		}
		return none, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	prior, err := r.read()
	if err == nil {
		if prior.Receipt.Previous.Generation > expected.Generation {
			return none, ErrStale
		}
		if identity(prior.Receipt.Previous) == identity(expected) {
			receipt := prior.Receipt
			if receipt.Phase != "prepared" || managerLaunchReplayable(receipt.OperationID) {
				receipt.Attention = r.attention
				return receipt, nil
			}
		} else if prior.Receipt.Phase != "applied" || prior.Receipt.SessionID != expected.SessionID {
			return none, ErrStale
		}
	} else if !errors.Is(err, ErrNotFound) {
		return none, err
	}
	if err = r.checkRoster(expected, ""); err != nil {
		return none, err
	}
	if err = ctx.Err(); err != nil {
		return none, err
	}
	op, err := protocol.NewLaunchOperationID()
	if err != nil {
		return none, err
	}
	receipt := ManagerReplacement{Previous: expected, OperationID: op, Phase: "prepared"}
	intent := replacementIntent{Version: 1, Receipt: receipt, Request: managerLaunchRequest(r.config, r.refs)}
	if err = r.write(intent); err != nil {
		return none, err
	}
	r.attention = ""
	return receipt, nil
}

func (c *managerControl) progressReplacement(ctx context.Context) (bool, error) {
	r := c.replacement
	if err := c.store.checkControllerBinding(c.binding); !errors.Is(err, managerproof.ErrQuarantined) {
		return false, err
	}
	r.mu.Lock()
	intent, err := r.read()
	r.mu.Unlock()
	if err != nil {
		return false, err
	}
	if identity(intent.Receipt.Previous) != identity(c.binding) || intent.Receipt.Previous.Generation > c.binding.Generation {
		return false, ErrStale
	}
	current, _ := json.Marshal(managerLaunchRequest(r.config, r.refs))
	recorded, _ := json.Marshal(intent.Request)
	if string(current) != string(recorded) {
		return false, ErrConflict
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	receipt := &intent.Receipt
	if receipt.Phase == "prepared" {
		if !managerLaunchReplayable(receipt.OperationID) {
			return false, ErrExpired
		}
		if err = r.checkRoster(c.binding, ""); err != nil {
			return false, err
		}
		if _, err = managerBridgeCredential(r.dir, filepath.Base(r.refs.AuthFile), true); err != nil {
			return false, err
		}
		if err = r.begin(intent); err != nil {
			return false, err
		}
		receipt.Phase = "executing"
		id, _, launchErr := r.runtime.LaunchOperation(receipt.OperationID, intent.Request)
		if knownUnsentManagerLaunch(launchErr) {
			receipt.Phase = "prepared"
			return false, errors.Join(launchErr, r.write(intent))
		}
		if launchErr == nil && id != "" {
			receipt.Phase, receipt.SessionID = "applied", id
		}
	}
	if receipt.Phase == "executing" {
		outcome, err := r.runtime.LaunchOperationStatus(receipt.OperationID)
		if err != nil {
			return false, err
		}
		target, applied := protocol.AppliedOwnerLaunchSession(r.endpoint, outcome)
		if !applied {
			return false, ErrControllerUncertain
		}
		receipt.Phase, receipt.SessionID = "applied", target
	}
	nextEndpoint, _, valid := protocol.ParseID(receipt.SessionID)
	if !valid || nextEndpoint != r.endpoint || receipt.SessionID == c.binding.SessionID {
		return false, ErrStale
	}
	if err = r.write(intent); err != nil {
		return false, err
	}
	if err = r.checkRoster(c.binding, receipt.SessionID); err != nil {
		return false, err
	}
	next, err := findBootstrapManager(ctx, r.config, r.refs, r.endpoint, receipt.SessionID, nil, r.runtime)
	if err != nil {
		return false, err
	}
	controller := c.controller
	if controller == nil {
		controller, err = NewController(c.store, r.runtime, c.binding, r.refs)
		if err != nil {
			return false, err
		}
	}
	_, err = controller.ReplaceManager(ctx, next.Binding, r.refs)
	return err == nil, err
}
