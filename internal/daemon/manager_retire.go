package daemon

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
)

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
	native, err := d.SessionNativeGeneration(local)
	if err != nil || native != target.NativeGeneration || !d.shimIdentityMatches(m) {
		return false, managerproof.ErrStale
	}
	if m.ManagerRetirement.OperationID == "" {
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
	if managerRetirementProven(m, target, exit, reported, retirementShimMayLive(m)) {
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
	if !found || !matchesBoundResumeRole(m, spec) || d.ManagerRetirementStatus(m, m.ManagerRetirement) != "retired" ||
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

func managerRetirementProven(m persist.Meta, target persist.ManagerRetirement, exit shim.ExitInfo, reported, shimAlive bool) bool {
	return m.Role == "manager" && m.Status.Process == status.ProcessExited && m.ShimPID == target.ShimPID &&
		m.ShimStartTime == target.ShimStartTime && m.ConversationID == target.NativeSessionID &&
		!target.ClaimedAt.IsZero() && reported && !exit.FinishedAt.Before(target.ClaimedAt) && !shimAlive
}

func matchesBoundResumeRole(source persist.Meta, spec LaunchSpec) bool {
	if spec.Manager == nil {
		return source.Role != "manager"
	}
	retirement := source.ManagerRetirement
	return source.Role == "manager" && source.ManagerConversationID == spec.Manager.ConversationID &&
		source.ManagerBridgeSocket == spec.Manager.BridgeSocket && source.ManagerAuthFile == spec.Manager.AuthFile &&
		retirement.OperationID != "" && retirement.OperationID == spec.Manager.RetireOperationID &&
		retirement.Instance == spec.ResumeSource.Instance && retirement.NativeSessionID == spec.ConversationID &&
		retirement.ShimPID == spec.ResumeSource.ShimPID && retirement.ShimStartTime == spec.ResumeSource.ShimStartTime
}
