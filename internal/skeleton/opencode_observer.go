package skeleton

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Nathandela/swarm/internal/adapter/opencode"
	"github.com/Nathandela/swarm/internal/opencodebridge"
)

type openCodeReader interface {
	Message(context.Context, string) (json.RawMessage, error)
	Snapshot(context.Context, int, string) (opencodebridge.Snapshot, error)
	Events(context.Context, func(opencodebridge.Event) error) error
}

// observeOpenCode owns the reducer. The first result is a subscription/snapshot barrier.
func observeOpenCode(ctx context.Context, reader openCodeReader, identity opencodebridge.Identity, state *OpenCodeState, publish func(OpenCodeView), ready func(error), projection *openCodeTranscript) {
	for {
		_ = observeOpenCodeEpoch(ctx, reader, identity, state, publish, ready, projection)
		ready = nil
		if ctx.Err() != nil {
			return
		}
		if projection != nil {
			projection.ready.Store(false)
		}
		state.Degrade("native event stream was interrupted")
		state.MarkRootStatusUnknown("native observation requires reconciliation")
		state.nativeUnknown = "native current activity requires reconciliation"
		if projection != nil {
			if err := projection.view(state.View()); err != nil {
				projection.ready.Store(false)
			}
		}
		publish(state.View())
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func observeOpenCodeEpoch(ctx context.Context, reader openCodeReader, identity opencodebridge.Identity, state *OpenCodeState, publish func(OpenCodeView), ready func(error), projection *openCodeTranscript) (result error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	notify := func(err error) {
		if ready != nil {
			ready(err)
			ready = nil
		}
	}
	defer func() { notify(result) }()
	epoch := state.BeginEpoch()
	if projection != nil {
		projection.ready.Store(false)
	}
	if state.scope == nil {
		state.scope = map[string]OpenCodeDescendant{}
	}
	state.nativeUnknown = "native current activity has not been reconciled"
	state.MarkRootStatusUnknown("native snapshot has not been reconciled")
	if projection != nil {
		if err := projection.view(state.View()); err != nil {
			projection.ready.Store(false)
		}
	}
	publish(state.View())
	events := make(chan []byte, 128)
	finished := make(chan error, 1)
	done := make(chan struct{})
	var buffered atomic.Int64
	go func() {
		defer close(done)
		finished <- reader.Events(ctx, func(event opencodebridge.Event) error {
			raw, err := json.Marshal(event)
			if err != nil || len(raw) > opencode.MaxNativeEventBytes {
				return errors.New("native event exceeds bound")
			}
			if buffered.Add(int64(len(raw))) > 2<<20 {
				buffered.Add(-int64(len(raw)))
				return errors.New("native event buffer exceeds bound")
			}
			select {
			case events <- raw:
				return nil
			default:
				buffered.Add(-int64(len(raw)))
				return errors.New("native event buffer is full")
			}
		})
	}()
	// The event producer must release all references before this epoch can be replaced.
	defer func() { cancel(); <-done }()
	preludeDeadline := time.NewTimer(5 * time.Second)
	defer preludeDeadline.Stop()
	var first []byte
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-preludeDeadline.C:
		return errors.New("native event subscription did not identify itself")
	case <-finished:
		return errors.New("native event subscription failed")
	case first = <-events:
		buffered.Add(-int64(len(first)))
	}
	var connected opencodebridge.Event
	var connectedIdentity opencodebridge.Identity
	if json.Unmarshal(first, &connected) != nil || connected.Type != "swarm.bridge.connected" || json.Unmarshal(connected.Properties, &connectedIdentity) != nil || connectedIdentity != identity {
		return errors.New("native event subscription has another binding")
	}

	apply := func(raw []byte, snapshot bool) (bool, error) {
		var envelope opencodebridge.Event
		if json.Unmarshal(raw, &envelope) != nil || envelope.Type == "swarm.bridge.gap" || envelope.Type == "swarm.bridge.connected" {
			return false, errors.New("native stream gap")
		}
		event, err := opencode.DecodeEvent(raw)
		if err != nil {
			return false, err
		}
		currentChange := event.SessionStatus != nil || event.SessionIdle != nil || strings.HasPrefix(event.Kind, "permission.") || strings.HasPrefix(event.Kind, "question.") || event.Kind == "session.created" || event.Kind == "session.updated" || event.Kind == "session.deleted"
		if snapshot && currentChange {
			return true, nil
		}
		switch state.Apply(raw, epoch).Disposition {
		case OCNeedsSnapshot:
			if event.MessageUpdated != nil {
				id := event.MessageUpdated.Info.ID
				row, err := reader.Message(ctx, id)
				if err != nil {
					return false, err
				}
				if err := state.reconcileMessage(id, row, epoch); err != nil {
					return false, err
				}
				if projection != nil {
					return false, projection.row(row, "live")
				}
				return false, nil
			}
			return true, nil
		case OCRefusedCap, OCRefusedParse, OCRefusedDegraded:
			return false, errors.New("native event could not be retained")
		}
		if projection != nil {
			row, err := projection.event(ctx, reader, event)
			if err != nil {
				return false, err
			}
			if row != nil {
				info, _, err := opencode.DecodeMessageRow(row)
				if err != nil {
					return false, err
				}
				if info.SessionID == identity.RootID && info.Role == "user" {
					if err := state.ImportHistory([]json.RawMessage{row}, epoch); err != nil {
						return false, err
					}
				}
				if err := projection.row(row, "live"); err != nil {
					return false, err
				}
			}
		}
		return false, nil
	}
	checkHealth := func() bool {
		if projection == nil || projection.qualifySink(ctx, reader) == nil {
			return true
		}
		projection.ready.Store(false)
		state.MarkRootStatusUnknown("native bridge health is unconfirmed")
		state.nativeUnknown = "native current activity requires health reconciliation"
		return false
	}
	refresh := func() error {
		for attempt := 0; attempt < 3; attempt++ {
			snapshot, err := reader.Snapshot(ctx, 50, "")
			if err != nil {
				return err
			}
			if err := validateOpenCodeSnapshot(snapshot, identity); err != nil {
				return err
			}
			if err := state.ImportHistory(snapshot.Messages, epoch); err != nil {
				return err
			}
			if projection != nil {
				if err := projection.recoverOwnedInputs(ctx, reader, state, epoch); err != nil {
					return err
				}
				if err := projection.history(snapshot.Messages, "imported"); err != nil {
					return err
				}
			}
			dirty := false
		Drain:
			for {
				select {
				case event := <-events:
					buffered.Add(-int64(len(event)))
					changed, err := apply(event, true)
					if err != nil {
						return err
					}
					dirty = dirty || changed
				default:
					break Drain
				}
			}
			if !dirty {
				if err := state.reconcileNative(snapshot, epoch); err != nil {
					return err
				}
				if projection != nil {
					projection.ready.Store(checkHealth())
				}
				return nil
			}
			state.MarkRootStatusUnknown("native status changed during reconciliation")
			state.nativeUnknown = "native current activity changed during reconciliation"
		}
		return nil
	}
	if err := refresh(); err != nil {
		return err
	}
	if projection != nil {
		if err := projection.view(state.View()); err != nil {
			projection.ready.Store(false)
		}
	}
	publish(state.View())
	notify(nil)
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-finished:
			return errors.New("native event stream ended")
		case raw := <-events:
			buffered.Add(-int64(len(raw)))
			needsSnapshot, err := apply(raw, false)
			if err != nil {
				return err
			}
			if needsSnapshot {
				if err := refresh(); err != nil {
					return err
				}
			}
			if projection != nil {
				if err := projection.view(state.View()); err != nil {
					projection.ready.Store(false)
				}
			}
			publish(state.View())
		case <-timer.C:
			if checkHealth() && (!state.View().BusyKnown || projection != nil && !projection.ready.Load()) {
				if err := refresh(); err != nil {
					return err
				}
			}
			if projection != nil {
				if err := projection.view(state.View()); err != nil {
					projection.ready.Store(false)
				}
			}
			publish(state.View())
		}
	}
}

func validateOpenCodeSnapshot(snapshot opencodebridge.Snapshot, identity opencodebridge.Identity) error {
	if snapshot.Identity != identity || snapshot.Statuses == nil || snapshot.Messages == nil || snapshot.Permissions == nil || snapshot.Questions == nil || snapshot.Children == nil {
		return errors.New("native snapshot has an incomplete or different binding")
	}
	var session struct {
		ID        string `json:"id"`
		Directory string `json:"directory"`
		Time      struct {
			Created int64 `json:"created"`
		} `json:"time"`
	}
	if json.Unmarshal(snapshot.Session, &session) != nil || session.ID != identity.RootID || session.Directory != identity.Directory || session.Time.Created != identity.NativeCreatedAtMS {
		return errors.New("native root identity could not be verified")
	}
	return nil
}
