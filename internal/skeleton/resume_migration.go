package skeleton

import (
	"fmt"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

type resumeRecoveryCall struct {
	done chan struct{}
	err  error
}

// ensureResumeConversationID is a bounded per-source singleflight. The map holds
// only in-flight calls and is pruned as each completes; unrelated sources resolve
// concurrently while a burst of retries for one source performs one disk scan.
func (a *coreAPI) ensureResumeConversationID(local string, source persist.Meta) error {
	if err := validateStoredResumeConversationID(source); err != nil {
		return err
	}
	if source.ConversationID != "" || a.historyResolver == nil {
		return nil
	}
	a.recoveryMu.Lock()
	if a.recoveries == nil {
		a.recoveries = make(map[string]*resumeRecoveryCall)
	}
	if active := a.recoveries[local]; active != nil {
		a.recoveryMu.Unlock()
		<-active.done
		return active.err
	}
	call := &resumeRecoveryCall{done: make(chan struct{})}
	a.recoveries[local] = call
	a.recoveryMu.Unlock()

	call.err = a.recoverResumeConversationID(local, source)
	close(call.done)
	a.recoveryMu.Lock()
	delete(a.recoveries, local)
	a.recoveryMu.Unlock()
	return call.err
}

func validateStoredResumeConversationID(source persist.Meta) error {
	if source.ConversationID == "" || source.AgentType != "codex" && source.AgentType != "claude" {
		return nil
	}
	if !adapter.IsCanonicalConversationID(source.ConversationID) {
		return fmt.Errorf("resume: saved conversation identity is invalid")
	}
	return nil
}

func (a *coreAPI) recoverResumeConversationID(local string, source persist.Meta) error {
	// An authoritative hook/backend capture can win before the singleflight leader
	// starts. Refetch before touching provider history.
	current, ok := a.core.Get(local)
	if !ok {
		return fmt.Errorf("resume: source session is no longer available")
	}
	if current.ConversationID != "" {
		if err := validateStoredResumeConversationID(current); err != nil {
			return err
		}
		return nil
	}
	if current.Status.Process == status.ProcessRunning || current.AgentType != source.AgentType {
		return fmt.Errorf("resume: source session is no longer eligible for recovery")
	}
	result := a.historyResolver.Resolve(current)

	// Refetch after every resolver outcome. A capture that won while the scan was
	// running is authoritative even if the scan found nothing or found another id.
	current, ok = a.core.Get(local)
	if !ok {
		return fmt.Errorf("resume: source session is no longer available")
	}
	if current.ConversationID != "" {
		if err := validateStoredResumeConversationID(current); err != nil {
			return err
		}
		return nil
	}
	if current.Status.Process == status.ProcessRunning || current.AgentType != source.AgentType {
		return fmt.Errorf("resume: source session is no longer eligible for recovery")
	}

	provider := source.AgentType
	switch result.Outcome {
	case resumeHistoryUnsupported:
		return nil // preserve the existing no-captured-id refusal in composeLaunchSpec
	case resumeHistoryNoMatch:
		return fmt.Errorf("resume: no matching %s conversation history was found", provider)
	case resumeHistoryAmbiguous:
		return fmt.Errorf("resume: multiple matching %s conversation histories were found; refusing to guess", provider)
	case resumeHistoryUnsafe:
		return fmt.Errorf("resume: %s conversation history is unsafe to inspect", provider)
	case resumeHistoryUnreadable:
		return fmt.Errorf("resume: could not read %s conversation history safely", provider)
	case resumeHistoryFound:
		if !adapter.IsCanonicalConversationID(result.ConversationID) {
			return fmt.Errorf("resume: %s conversation history returned an unsafe identity", provider)
		}
	default:
		return fmt.Errorf("resume: %s conversation history returned an unsafe result", provider)
	}

	// This private seam deterministically exercises the narrow race between the
	// empty refetch and persistence. It is nil in production.
	if a.beforeRecoveryPersist != nil {
		a.beforeRecoveryPersist()
	}
	persistErr := a.core.SetConversationID(local, result.ConversationID)

	// SetConversationID is write-once. Always refetch its actual winner: another
	// authoritative producer may have stored a different valid id immediately
	// before this call, in which case composition must use that winner.
	current, ok = a.core.Get(local)
	if ok && current.ConversationID != "" {
		if err := validateStoredResumeConversationID(current); err != nil {
			return err
		}
		return nil
	}
	if persistErr != nil {
		return fmt.Errorf("resume: could not save recovered %s conversation identity", provider)
	}
	return fmt.Errorf("resume: recovered %s conversation identity was not stored", provider)
}
