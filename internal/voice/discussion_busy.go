package voice

import (
	"context"
	"encoding/json"

	"github.com/Nathandela/swarm/internal/protocol"
)

func waitingOn(a, running Admission) bool {
	return running.NativeInputID != "" && a.WaitingOnInput == running.NativeInputID && a.ConversationID == running.ConversationID && a.Origin != nil && a.Sequence > running.Sequence && a.Phase == "" && a.SettledAt.IsZero()
}

// Carry only a scheduling hint on the authenticated tool response. The queued
// instructions keep their own admission and never inherit this turn's authority.
func (c *Controller) addWaitingNotice(running Admission, result json.RawMessage) json.RawMessage {
	st, err := c.store.readView()
	if err != nil {
		return result
	}
	var first Admission
	for _, a := range st.Admissions {
		if waitingOn(a, running) && (first.Sequence == 0 || a.Sequence < first.Sequence) {
			first = a
		}
	}
	if first.Sequence == 0 || first.WaitingNotice {
		return result
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(result, &fields) != nil || fields == nil {
		return result
	}
	fields["dispatcher_notice"] = json.RawMessage(`"A new Discord message is waiting. Finish this turn at the next safe point so it can be delivered."`)
	augmented, err := json.Marshal(fields)
	if err != nil {
		return result
	}
	if _, err := managerToolResponse(augmented); err != nil {
		return result
	}
	err = c.store.update(func(st *State) error {
		if err := checkBinding(st, c.binding); err != nil {
			return err
		}
		k := admissionKey(first.ConversationID, first.Sequence)
		a := st.Admissions[k]
		if !waitingOn(a, running) || a.WaitingNotice {
			return ErrStale
		}
		a.WaitingNotice = true
		putRecord(st, "admissions", &st.Admissions, k, a)
		return nil
	})
	if err != nil {
		return result
	}
	return augmented
}

func (c *Controller) interruptForWaitingMessages(ctx context.Context, running Admission, status protocol.ManagerRuntimeStatus) error {
	rt, ok := c.runtime.(managerInterruptRuntime)
	if !ok || c.discussion == nil || status.Turn.State != "running" || status.Turn.ExternallySteered || status.Turn.NativeInputID != running.NativeInputID || status.Turn.NativeTurnID != running.NativeTurnID {
		return nil
	}
	st, err := c.store.readView()
	if err != nil {
		return err
	}
	conv := st.Conversations[running.ConversationID]
	if conv.SchedulerPaused || conv.CustodyLost || conv.Parked || conv.Retirement != nil {
		return nil
	}
	first := map[string]Admission{}
	for _, a := range st.Admissions {
		if !waitingOn(a, running) || a.QuestionBoundary != conv.LastOutput {
			continue
		}
		sender := a.Origin.ActorID
		prior, exists := first[sender]
		if !exists {
			first[sender] = a
			continue
		}
		if a.Origin.MessageID == prior.Origin.MessageID {
			continue
		}
		if c.discussion.VerifyAudience != nil {
			if err := c.discussion.VerifyAudience(ctx); err != nil {
				return err
			}
		}
		if c.discussion.VerifyOrigin == nil {
			return nil
		}
		for _, ping := range []Admission{prior, a} {
			if err := c.discussion.VerifyOrigin(ctx, *ping.Origin); err != nil {
				return nil
			}
		}
		// The supervisor deduplicates by binding, input and turn, including lost replies.
		_, err := rt.ManagerInterrupt(ctx, protocol.ManagerInterruptReq{Binding: runtimeBinding(c.binding), NativeInputID: running.NativeInputID, NativeTurnID: running.NativeTurnID}, false)
		return err
	}
	return nil
}
