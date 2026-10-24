package skeleton

import (
	"fmt"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// Caller holds provider admission. Transfer consumes the legacy journal before
// any replacement launch; its complete cohort is never reduced by later lists.
func (m *loginManager) transferCodexMigration(r logins.Registry, op logins.Switch, toID string) (logins.Switch, error) {
	if !op.OwnershipMigration || op.Provider != logins.ProviderCodex || (op.Phase != "committed" && op.Phase != "resuming") {
		return logins.Switch{}, logins.ErrPending
	}
	if m.codexHandover == nil {
		m.codexHandover = newCodexHandoverCoordinator(m)
	}
	c := m.codexHandover
	members, err := c.members()
	if err != nil {
		return logins.Switch{}, err
	}
	var children []logins.BootstrapChild
	for _, row := range op.Sessions {
		var found persist.Meta
		ambiguous := false
		for _, meta := range m.list() {
			if row.ChildID != "" && meta.ID != row.ChildID {
				continue
			}
			if meta.ResumedFrom != row.SourceID || meta.ConversationID != row.ConversationID || meta.AgentType != logins.ProviderCodex || meta.LoginRuntime == nil || !meta.LoginRuntime.ExternalAuth || meta.LoginRuntime.Epoch != op.ToEpoch || meta.InputEmbargo != op.ID && (row.ChildID != meta.ID || meta.InputEmbargo != "") {
				continue
			}
			if found.ID != "" {
				ambiguous = true
				continue
			}
			found = meta
		}
		if ambiguous {
			if row.ChildID != "" || op.Phase != "resuming" {
				return logins.Switch{}, logins.ErrPending
			}
			// Preserve the complete cohort, but never choose between old launches.
			continue
		}
		if found.ID == "" {
			continue
		}
		n, err := c.bootstrapNative(found, op.ID)
		if err != nil {
			// Transfer retains a named unproved child as an ambiguous hint.
			// Full live inventory above remains mandatory.
			continue
		}
		phase := "pending"
		if c.ended(n) {
			phase = "retired"
		} else {
			present := false
			for _, member := range members {
				if member == n {
					present = true
					break
				}
			}
			if !present {
				return logins.Switch{}, logins.ErrPending
			}
		}
		children = append(children, logins.BootstrapChild{SourceID: row.SourceID, Native: n, Phase: phase})
	}
	_, h, err := m.store.TransferCodexMigration(r.Revision, op.ID, toID, members, children)
	if err != nil {
		return logins.Switch{}, err
	}
	for _, b := range h.Bootstrap {
		if b.Phase == "ready" || b.Phase == "failed" || b.Phase == "cancelled" {
			m.d.clearAuthRecycle(b.SourceID)
			if b.ChildID != "" {
				m.d.clearAuthRecycle(b.ChildID)
			}
		} else {
			m.d.restoreAuthRecycle(b.SourceID)
		}
	}
	c.signalSelection()
	return handoverSwitch(h), nil
}

// Caller owns stepMu; an unresolved launch is visibly refused, never cancelled
// by forgetting its nonce. Known children use the ordinary exact owner fence.
func (m *loginManager) endCodexBootstrap(r logins.Registry, local, action string) (bool, error) {
	h, ok := r.Handovers[logins.ProviderCodex]
	if !ok {
		return false, nil
	}
	for _, b := range h.Bootstrap {
		if b.SourceID != local && b.ChildID != local && b.LegacyChildID != local {
			continue
		}
		if b.ChildID == "" && (b.Launch != nil || b.Phase == "ambiguous") {
			return true, fmt.Errorf("%w: exact replacement launch outcome is pending; cancellation was not applied", logins.ErrPending)
		}
		target := b.SourceID
		if b.ChildID == "" && action == "delete" && bootstrapOutcomeSettled(b) {
			if _, exists := m.get(target); !exists {
				return true, m.d.api.deleteDiscussion(target)
			}
		}
		var native logins.NativeMember
		if b.ChildID != "" {
			target = b.ChildID
			meta, exists := m.get(target)
			if !exists {
				// A settled, retired binding may already have been deleted before a lost
				// owner acknowledgment. deleteDiscussion only accepts an absent directory.
				if action == "delete" && bootstrapOutcomeSettled(b) {
					for _, member := range h.Members {
						if member.Native.SessionID == b.ChildID && member.Native.NativeIncarnation == b.ChildIncarnation && member.Phase == "retired" {
							return true, m.d.api.deleteDiscussion(target)
						}
					}
				}
				return true, logins.ErrPending
			}
			if meta.ResumedFrom != b.SourceID || meta.ConversationID != b.ConversationID {
				return true, logins.ErrPending
			}
			if action == "delete" && b.OwnerDeleteAuthorized {
				if !m.codexBootstrapDeleteRetryMatches(h, b, meta) {
					return true, logins.ErrPending
				}
				m.d.clearAuthRecycle(target)
				err := m.d.withOwnerSessionEnd(target, func() error { return m.d.api.deleteDiscussion(target) })
				if err == nil {
					m.d.clearAuthRecycle(b.SourceID)
				}
				return true, err
			}
			var err error
			native, err = m.codexHandover.bootstrapNative(meta, bootstrapNonce(h, b))
			if err != nil || native.NativeIncarnation != b.ChildIncarnation {
				return true, logins.ErrPending
			}
			exact := false
			for _, member := range h.Members {
				if member.Native == native {
					exact = true
				}
			}
			if !exact {
				return true, logins.ErrPending
			}
		}
		// stepMu excludes the bootstrap worker while the owner takes this lane.
		m.d.clearAuthRecycle(target)
		err := m.d.withOwnerSessionEnd(target, func() error { return m.stopCodexBootstrapTarget(target, h, b, native) })
		if err != nil {
			if !bootstrapOutcomeSettled(b) {
				m.d.restoreAuthRecycle(target)
			}
			return true, err
		}
		// Keep the ended row and private native proof until BOTH durable facts
		// are recorded. Never acquire provider admission under the owner lane.
		if err = m.settleCodexBootstrapEnd(h, b, native, action == "delete"); err != nil {
			if !bootstrapOutcomeSettled(b) {
				m.d.restoreAuthRecycle(target)
			}
			return true, err
		}
		m.d.clearAuthRecycle(b.SourceID)
		if action == "delete" {
			err = m.d.withOwnerSessionEnd(target, func() error { return m.d.api.deleteDiscussion(target) })
			if err != nil {
				return true, err
			}
		}
		m.d.clearAuthRecycle(target)
		return true, nil
	}
	return false, nil
}
func bootstrapOutcomeSettled(b logins.BootstrapObligation) bool {
	return b.Phase == "ready" || b.Phase == "failed" || b.Phase == "cancelled"
}
func (m *loginManager) stopCodexBootstrapTarget(target string, h logins.Handover, b logins.BootstrapObligation, expected logins.NativeMember) error {
	var err error
	if m.d.core != nil {
		err = m.d.core.Kill(target)
	} else if m.d.authw != nil && m.d.authw.kill != nil {
		err = m.d.authw.kill(target)
	} else {
		return logins.ErrPending
	}
	if err != nil {
		return err
	}
	deadline := time.Now().Add(authRecycleExitWait)
	for {
		meta, exists := m.get(target)
		if !exists {
			return logins.ErrPending
		}
		if meta.Status.Process != status.ProcessRunning {
			if b.ChildID == "" {
				return nil
			}
			if meta.ResumedFrom != b.SourceID || meta.ConversationID != b.ConversationID {
				return logins.ErrPending
			}
			proof, e := m.codexHandover.bootstrapNative(meta, bootstrapNonce(h, b))
			if e == nil && proof == expected && m.codexHandover.ended(proof) {
				return nil
			}
		}
		if !time.Now().Before(deadline) {
			return logins.ErrPending
		}
		timer := time.NewTimer(authRecycleExitPoll)
		select {
		case <-m.d.closing:
			timer.Stop()
			return logins.ErrPending
		case <-timer.C:
		}
	}
}
func (m *loginManager) settleCodexBootstrapEnd(expected logins.Handover, b logins.BootstrapObligation, native logins.NativeMember, deleting bool) error {
	gate := &m.admission[loginProviderIndex(logins.ProviderCodex)]
	gate.Lock()
	defer gate.Unlock()
	r, err := m.store.Snapshot()
	if err != nil {
		return err
	}
	h, exists := r.Handovers[logins.ProviderCodex]
	row, found := bootstrapRow(h, b.SourceID)
	if !exists || h.ID != expected.ID || !found || row.ChildID != b.ChildID || row.ChildIncarnation != b.ChildIncarnation || claimID(row) != claimID(b) {
		return logins.ErrPending
	}
	if row.ChildID != "" {
		exact := false
		for _, member := range h.Members {
			if member.Native != native {
				continue
			}
			exact = true
			if member.Phase != "retired" {
				r, h, err = m.store.RecordCodexReceipt(r.Revision, logins.HandoverReceipt{HandoverID: h.ID, ToID: h.ToID, Epoch: h.Epoch, DestinationCredentialRevision: h.DestinationCredentialRevision, Native: native, Phase: "retired"})
				if err != nil {
					return err
				}
			}
			break
		}
		if !exact {
			return logins.ErrPending
		}
	}
	if !bootstrapOutcomeSettled(row) {
		r, h, err = m.store.RecordCodexBootstrapOutcome(r.Revision, h.ID, row.SourceID, claimID(row), "cancelled")
	}
	if err == nil && deleting && row.ChildID != "" {
		_, _, err = m.store.AuthorizeCodexBootstrapDelete(r.Revision, h.ID, row.SourceID, claimID(row), native)
	}
	return err
}

// A previous verified owner stop survives partial payload removal. Ordinary
// retirement/cancellation cannot substitute for this exact deletion authority.
func (m *loginManager) codexBootstrapDeleteRetryMatches(h logins.Handover, b logins.BootstrapObligation, meta persist.Meta) bool {
	if !b.OwnerDeleteAuthorized || !bootstrapOutcomeSettled(b) || meta.ID != b.ChildID || meta.ResumedFrom != b.SourceID || meta.ConversationID != b.ConversationID || meta.AgentType != logins.ProviderCodex || meta.LoginRuntime == nil || !meta.LoginRuntime.ExternalAuth || meta.LoginRuntime.Provider != logins.ProviderCodex || meta.LoginRuntime.Home != m.store.Home(logins.ProviderCodex) || meta.InputEmbargo != "" && meta.InputEmbargo != bootstrapNonce(h, b) || meta.ShimPID <= 0 || meta.ShimStartTime <= 0 || loginProcessAlive(meta.ShimPID, meta.ShimStartTime) {
		return false
	}
	if meta.Status.Process != status.ProcessExited && meta.Status.Process != status.ProcessLost {
		return false
	}
	for _, member := range h.Members {
		n := member.Native
		if member.Phase == "retired" && n.SessionID == b.ChildID && n.ConversationID == b.ConversationID && n.NativeIncarnation == b.ChildIncarnation && n.NativeIncarnation == fmt.Sprintf("%d:%d", n.NativePID, n.NativeStartTime) && m.codexHandover.ended(n) {
			return true
		}
	}
	return false
}
