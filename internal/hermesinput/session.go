package hermesinput

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/coder/websocket"
)

type Snapshot struct {
	Info struct {
		Cwd string `json:"cwd"`
	} `json:"info"`
	SessionID           string    `json:"session_id"`
	SubscriptionID      string    `json:"subscription_id"`
	AuthorityEpoch      int64     `json:"authority_epoch"`
	ExecutionState      string    `json:"execution_state"`
	ExecutionGeneration int64     `json:"execution_generation"`
	Pending             []Receipt `json:"pending"`
	Prompts             []struct {
		Kind string `json:"kind"`
		ID   string `json:"prompt_id"`
	} `json:"prompts"`
}

type Activity struct {
	Snapshot   Snapshot
	Background bool
	Unknown    bool
}

func resume(ctx context.Context, c *websocket.Conn, b Binding, id int) (Snapshot, error) {
	var s Snapshot
	err := rpc(ctx, c, id, "session.resume", map[string]string{"session_id": b.SessionID}, &s)
	if err == nil && (s.SessionID != b.SessionID || s.SubscriptionID == "" || b.AuthorityEpoch > 0 && s.AuthorityEpoch != b.AuthorityEpoch) {
		err = errors.New("hermes resumed another native scope")
	}
	return s, err
}

// Inspect reads the provider's session and existing background registries. It
// neither submits a turn nor restarts a gateway after an incarnation change.
func Inspect(ctx context.Context, b Binding) (Activity, error) {
	var a Activity
	c, err := connect(ctx, b)
	if err != nil {
		return a, err
	}
	defer func() { _ = c.CloseNow() }()
	a.Snapshot, err = resume(ctx, c, b, 1)
	if err != nil {
		return a, err
	}
	a.Background, a.Unknown, err = background(ctx, c, b, 2)
	return a, err
}

func background(ctx context.Context, c *websocket.Conn, b Binding, id int) (bool, bool, error) {
	var processes struct {
		Processes *[]map[string]json.RawMessage `json:"processes"`
	}
	if err := rpc(ctx, c, id, "process.list", map[string]string{"session_id": b.SessionID}, &processes); err != nil {
		return false, true, err
	}
	var agents struct {
		Agents *[]struct {
			Status string `json:"status"`
		} `json:"subagents"`
		Delegations *[]json.RawMessage `json:"delegations"`
	}
	if err := rpc(ctx, c, id+1, "subagent.list", map[string]string{"session_id": b.SessionID}, &agents); err != nil {
		return false, true, err
	}
	if processes.Processes == nil || agents.Agents == nil || agents.Delegations == nil {
		return false, true, nil
	}
	active := len(*agents.Delegations) > 0
	for _, p := range *processes.Processes {
		var exited bool
		if raw, ok := p["exited"]; ok && json.Unmarshal(raw, &exited) == nil {
			active = active || !exited
			continue
		}
		var state string
		if json.Unmarshal(p["status"], &state) != nil {
			return active, true, nil
		}
		switch state {
		case "exited", "completed", "killed":
		default:
			active = true
		}
	}
	for _, agent := range *agents.Agents {
		switch agent.Status {
		case "completed", "failed", "interrupted", "cancelled":
		default:
			active = true
		}
	}
	return active, false, nil
}

// Stop requests cancellation only within this exact session. Native receipt
// completion and native background settlement are checked before reporting quiet.
func Stop(ctx context.Context, b Binding) error {
	c, err := connect(ctx, b)
	if err != nil {
		return err
	}
	defer func() { _ = c.CloseNow() }()
	s, err := resume(ctx, c, b, 1)
	if err != nil {
		return err
	}
	id := 2
	for _, pending := range s.Pending {
		if pending.Status == "unknown" {
			return errUncertain
		}
		if pending.Status == "queued" {
			var result Receipt
			if err = rpc(ctx, c, id, "prompt.cancel", map[string]any{"session_id": b.SessionID, "admission_id": pending.AdmissionID}, &result); err != nil {
				return err
			}
			id++
		}
	}
	if s.ExecutionState == "running" || s.ExecutionState == "waiting" {
		var result json.RawMessage
		if err = rpc(ctx, c, id, "session.interrupt", map[string]any{"session_id": b.SessionID, "execution_generation": s.ExecutionGeneration}, &result); err != nil {
			return err
		}
		id++
	}
	var stopped struct {
		Killed      int `json:"killed"`
		Delegations int `json:"delegations"`
	}
	if err = rpc(ctx, c, id, "process.stop", map[string]string{"session_id": b.SessionID}, &stopped); err != nil {
		return err
	}
	id++
	for {
		s, err = resume(ctx, c, b, id)
		id++
		if err != nil {
			return err
		}
		busy, unknown, err := background(ctx, c, b, id)
		id += 2
		if err != nil {
			return err
		}
		if s.ExecutionState == "idle" && len(s.Pending) == 0 && !busy && !unknown {
			return nil
		}
		if s.ExecutionState == "unknown" || unknown {
			return errUncertain
		}
		select {
		case <-ctx.Done():
			return errors.Join(errUncertain, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// CloseViewer ends only the subscription created on this connection. It never
// calls session.close, deletes history, or signals the persistent gateway.
func CloseViewer(ctx context.Context, b Binding) error {
	c, err := connect(ctx, b)
	if err != nil {
		return err
	}
	defer func() { _ = c.CloseNow() }()
	s, err := resume(ctx, c, b, 1)
	if err != nil {
		return err
	}
	var reply struct {
		SessionID      string `json:"session_id"`
		SubscriptionID string `json:"subscription_id"`
		Detached       bool   `json:"detached"`
	}
	if err = rpc(ctx, c, 2, "session.detach", map[string]string{"session_id": b.SessionID, "subscription_id": s.SubscriptionID, "end_reason": "swarm_viewer_close"}, &reply); err != nil {
		return err
	}
	if !reply.Detached || reply.SessionID != b.SessionID || reply.SubscriptionID != s.SubscriptionID {
		return errors.New("hermes exact native viewer detach is unconfirmed")
	}
	return nil
}

func Validate(ctx context.Context, b Binding) error { _, err := identify(ctx, b); return err }
