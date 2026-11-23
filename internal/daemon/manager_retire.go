package daemon

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/workermail"
)

func (d *Daemon) retirementCancellation(local, operation string) string {
	return filepath.Join(d.sessionDir(local), fmt.Sprintf("cancelled-retirement-%x", sha256.Sum256([]byte(operation))))
}

// CancelManagerRetire fences delayed copies of this request under the same custody lock as its claim.
func (d *Daemon) CancelManagerRetire(ctx context.Context, local, conversation, provider string, target persist.ManagerRetirement) error {
	unlock, err := d.lockComposerCustody()
	if err != nil {
		return err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	m, ok := d.Get(local)
	if !ok || m.Role != "manager" || m.ManagerConversationID != conversation || m.AgentType != provider ||
		m.ShimPID != target.ShimPID || m.ShimStartTime != target.ShimStartTime || m.ManagerRetirement.OperationID != "" {
		return managerproof.ErrStale
	}
	// An empty directory is a durable tombstone once its parent is synced.
	err = os.Mkdir(d.retirementCancellation(local, target.OperationID), 0700)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return syncLaunchDirectories(d.sessionDir(local))
}

// RetireManager records the checkpoint and signals under the same incarnation
// fence as the metadata writers. The caller holds both input lanes and the
// controller-lease fence while checking native readiness and entering here.
func (d *Daemon) RetireManager(ctx context.Context, local, conversation, provider string, target persist.ManagerRetirement) (claimed bool, err error) {
	unlock, err := d.lockComposerCustody()
	if err != nil {
		return false, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m, ok := d.Get(local)
	if !ok || m.Role != "manager" || m.AgentType != provider || m.ManagerConversationID != conversation ||
		m.ConversationID != target.NativeSessionID || m.Status.Process != status.ProcessRunning ||
		m.ShimPID != target.ShimPID || m.ShimStartTime != target.ShimStartTime ||
		(m.ManagerRetirement.OperationID != "" && m.ManagerRetirement != target) {
		return false, managerproof.ErrStale
	}
	if _, err := os.Stat(d.retirementCancellation(local, target.OperationID)); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return false, err
		}
		return false, managerproof.ErrStale
	}
	native, err := d.SessionNativeGeneration(local)
	if err != nil || native != target.NativeGeneration || !d.shimIdentityMatches(m) {
		return false, managerproof.ErrStale
	}
	if m.ManagerRetirement.OperationID == "" {
		pending, err := workermail.HasPending(d.cfg.StateDir, conversation)
		if err != nil {
			return false, err
		}
		if pending && target.Replacement == "" {
			return false, managerproof.ErrStale
		}
		target.ClaimedAt = time.Now().UTC()
		m.ManagerRetirement = target
		written, err := d.saveMetaLocked(&m)
		if err != nil || !written {
			return false, errors.Join(managerproof.ErrUnavailable, err)
		}
	}
	// Save already synced the file. The namespace must also survive a crash
	// before the signal receives authority.
	if err := syncLaunchDirectories(d.sessionDir(local)); err != nil {
		return true, err
	}
	if !d.shimIdentityMatches(m) {
		return true, managerproof.ErrStale
	}
	return true, signalShimChecked(shimSocketPath(d.cfg.StateDir, local), shimwire.SigTerm, func(conn net.Conn) bool {
		localConn, ok := conn.(*net.UnixConn)
		if !ok {
			return false
		}
		peer, err := retirementPeerPID(localConn)
		return err == nil && peer == m.ShimPID && d.shimIdentityMatches(m)
	})
}

// ManagerRetirementStatus uses the exit side-file and the old physical identity.
// A lost transport, missing process or different incarnation is insufficient.
func (d *Daemon) ManagerRetirementStatus(m persist.Meta, target persist.ManagerRetirement) string {
	if target.OperationID == "" || m.ManagerRetirement != target {
		return "unknown"
	}
	exit, reported := readExitSideFile(d.sessionDir(m.ID))
	if ManagerRetirementProven(m, target, exit, reported, retirementShimMayLive(m)) {
		return "retired"
	}
	return "uncertain"
}

func retirementShimMayLive(m persist.Meta) bool {
	if !pidAlive(m.ShimPID) {
		return false
	}
	actual, err := processStartTime(m.ShimPID)
	return err != nil || actual == m.ShimStartTime
}

// BindParkedManagerResume consumes a checkpoint for one reliable owner launch.
// It lives with the retired source, including after the launch receipt expires.
func (d *Daemon) BindParkedManagerResume(spec LaunchSpec) error {
	unlock, err := d.lockComposerCustody()
	if err != nil {
		return err
	}
	defer unlock()
	if spec.Manager == nil || spec.ResumeSource == nil || spec.OwnerLaunchHash == "" || spec.OperationID == "" {
		return ErrResumeSourceChanged
	}
	m, found := d.Get(spec.ResumedFrom)
	if !found || !d.ParkedManagerResumeAllowed(m, spec) ||
		m.ManagerResumeOperationID != "" && (m.ManagerResumeOperationID != spec.OperationID || m.ManagerResumeRequestHash != spec.OwnerLaunchHash) {
		return ErrResumeSourceChanged
	}
	if m.ManagerResumeOperationID == "" {
		m.ManagerResumeOperationID, m.ManagerResumeRequestHash = spec.OperationID, spec.OwnerLaunchHash
		written, err := d.saveMetaLocked(&m)
		if err != nil || !written {
			return errors.Join(ErrResumeSourceChanged, err)
		}
	}
	return syncLaunchDirectories(d.sessionDir(m.ID))
}

// FailedManagerStartupInstance corroborates a failed startup with the physical exit and
// the incarnation's verified empty proof ledger. A missing record is inconclusive.
func (d *Daemon) FailedManagerStartupInstance(m persist.Meta) string {
	if m.Role != "manager" || m.ResumedFrom == "" && !schema.IsDiscordManager(m.Role, m.ManagerConversationID) || m.ManagerRetirement.OperationID != "" ||
		m.Status.Process != status.ProcessExited || m.ExitCode == nil || *m.ExitCode == 0 ||
		m.ShimPID <= 0 || m.ShimStartTime <= 0 || retirementShimMayLive(m) {
		return ""
	}
	dir := d.sessionDir(m.ID)
	exit, ok := readExitSideFile(dir)
	if !ok || m.CreatedAt.IsZero() || exit.FinishedAt.IsZero() || exit.ExitCode != *m.ExitCode || exit.FinishedAt.Before(m.CreatedAt) {
		return ""
	}
	replay, err := managerproof.OpenReplay(dir, m.ID, m.ManagerConversationID, m.ManagerReplayID)
	if err != nil {
		return ""
	}
	stats, err := replay.Stats()
	closeErr := replay.Close()
	if err != nil || closeErr != nil || stats.Records != 0 || stats.GuardKeys != 0 {
		return ""
	}
	return d.recordedJournalInstance(m)
}

// ParkedManagerResumeAllowed permits one continuation from the retired source
// and one from its proved failed child. The child's own reservation fences retries.
func (d *Daemon) ParkedManagerResumeAllowed(source persist.Meta, spec LaunchSpec) bool {
	if spec.Manager == nil || spec.ResumeSource == nil || source.ConversationID != spec.ConversationID ||
		source.ShimPID != spec.ResumeSource.ShimPID || source.ShimStartTime != spec.ResumeSource.ShimStartTime || !matchesBoundResumeRole(source, spec) {
		return false
	}
	if source.ManagerRetirement.OperationID != "" {
		return d.ManagerRetirementStatus(source, source.ManagerRetirement) == "retired"
	}
	parent, ok := d.Get(source.ResumedFrom)
	if !ok || parent.ManagerRetirement.OperationID == "" || parent.ManagerRetirement.OperationID != spec.Manager.RetireOperationID ||
		parent.ManagerConversationID != source.ManagerConversationID || parent.ConversationID != source.ConversationID ||
		parent.ManagerBridgeSocket != source.ManagerBridgeSocket || parent.ManagerAuthFile != source.ManagerAuthFile ||
		parent.AgentType != source.AgentType || parent.ManagerResumeOperationID == "" ||
		d.ManagerRetirementStatus(parent, parent.ManagerRetirement) != "retired" {
		return false
	}
	out, ok := d.OwnerLaunchOutcome(parent.ManagerResumeOperationID)
	return ok && out.State == OpStateApplied && out.SessionID == source.ID && spec.ResumeSource.Instance != "" && d.FailedManagerStartupInstance(source) == spec.ResumeSource.Instance
}

func ManagerRetirementProven(m persist.Meta, target persist.ManagerRetirement, exit shim.ExitInfo, reported, shimAlive bool) bool {
	return m.Role == "manager" && m.Status.Process == status.ProcessExited && m.ShimPID == target.ShimPID &&
		m.ShimStartTime == target.ShimStartTime && m.ConversationID == target.NativeSessionID &&
		!target.ClaimedAt.IsZero() && reported && !exit.FinishedAt.Before(target.ClaimedAt) && !shimAlive
}

func matchesBoundResumeRole(source persist.Meta, spec LaunchSpec) bool {
	if spec.Manager == nil {
		return source.Role != "manager"
	}
	retirement := source.ManagerRetirement
	return retirement.Replacement == "" && source.Role == "manager" && source.ManagerConversationID == spec.Manager.ConversationID &&
		source.ManagerBridgeSocket == spec.Manager.BridgeSocket && source.ManagerAuthFile == spec.Manager.AuthFile &&
		((retirement.OperationID == "" && source.ResumedFrom != "" && spec.Manager.RetireOperationID != "") ||
			(retirement.OperationID != "" && retirement.OperationID == spec.Manager.RetireOperationID &&
				retirement.Instance == spec.ResumeSource.Instance && retirement.NativeSessionID == spec.ConversationID &&
				retirement.ShimPID == spec.ResumeSource.ShimPID && retirement.ShimStartTime == spec.ResumeSource.ShimStartTime))
}

// RecordManagerQuota binds authenticated terminal quota evidence to its native thread and turn.
func (d *Daemon) RecordManagerQuota(local, thread, turn string) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	m, ok := d.Get(local)
	if !ok || !schema.IsDiscordManager(m.Role, m.ManagerConversationID) || m.ConversationID != thread || thread == "" || turn == "" || m.Status.Process != status.ProcessRunning {
		return managerproof.ErrStale
	}
	if m.ManagerQuotaTurn == turn {
		return nil
	}
	m.ManagerQuotaTurn = turn
	written, err := d.saveMetaLocked(&m)
	if !written {
		return errors.Join(managerproof.ErrUnavailable, err)
	}
	return err
}
