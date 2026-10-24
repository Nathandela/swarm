package skeleton

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

// Snapshot before entering Engine: ingress holds this state lock while entering Engine.
// The returned check never takes that lock or holds backend.mu during I/O.
func (d *Daemon) codexHostInputConnection(local string, b *sessionBackend) (persist.CodexHostInputClaim, func() bool) {
	var claim persist.CodexHostInputClaim
	if d.eng == nil || d.executionStore == nil || d.core == nil || b == nil || b.feed == nil {
		return claim, nil
	}
	state := &b.feed.codexExecution
	state.mu.Lock()
	prepared := state.prepared && state.begun && state.cli.Version == qualifiedCodexExecutionVersion
	native, endpoint, instance, thread := state.native, state.endpoint, state.instance, state.thread
	shimPID, shimStart, cli := state.shimPID, state.shimStart, state.cli
	state.mu.Unlock()
	if !prepared {
		return claim, nil
	}
	claim = persist.CodexHostInputClaim{NativeIncarnation: fmt.Sprintf("%d:%d", native.PID, native.StartedAtMS), FeedEpoch: b.feed.epoch, ThreadID: thread}
	return claim, func() bool {
		meta, current, err := d.readCodexExecutionNative(local, b.sessionInstance, native.SocketPath)
		if err != nil || current != native || meta.ShimPID != shimPID || meta.ShimStartTime != shimStart || meta.CLIIdentity == nil || *meta.CLIIdentity != cli || instance != b.sessionInstance || thread != b.threadID || !codexExecutionPeerMatches(b.conn, native.PID, endpoint) {
			return false
		}
		d.backend.mu.Lock()
		defer d.backend.mu.Unlock()
		return d.backend.live[local] == b && !b.feed.retired.Load()
	}
}

func (d *Daemon) beginCodexHostInput(local string, b *sessionBackend, client, expected string) (persist.CodexHostInputClaim, func(), error) {
	claim, current := d.codexHostInputConnection(local, b)
	if current == nil {
		return claim, nil, d.observeExecutionOwnerWrite(local)
	}
	if !current() {
		return claim, nil, fmt.Errorf("host input native connection changed")
	}
	claim.ClientID, claim.ExpectedTurnID = client, expected
	claim, err := d.eng.BeginCodexHostInput(local, claim)
	if err != nil {
		return claim, nil, err
	}
	d.backend.mu.Lock()
	if d.backend.live[local] != b || b.feed.retired.Load() {
		d.backend.mu.Unlock()
		return claim, nil, fmt.Errorf("host input backend retired before write")
	}
	// Release immediately after the write attempt, never after the RPC reply.
	return claim, d.backend.mu.Unlock, nil
}

func (d *Daemon) finishCodexHostInput(local string, b *sessionBackend, claim persist.CodexHostInputClaim, turn string, refused bool) error {
	if claim.OwnerRevision == 0 {
		return nil
	}
	snapshot, current := d.codexHostInputConnection(local, b)
	if current == nil || snapshot.NativeIncarnation != claim.NativeIncarnation || snapshot.FeedEpoch != claim.FeedEpoch || snapshot.ThreadID != claim.ThreadID {
		return fmt.Errorf("host input receipt native connection changed")
	}
	if refused {
		return d.eng.RefuseCodexHostInput(local, claim, current)
	}
	return d.eng.RecordCodexHostInputResult(local, claim, turn, current)
}

func (d *Daemon) callComposerHostInput(ctx context.Context, local string, lane *composerLane, barrier uint64, b *sessionBackend, method string, params any, expected, client string, begin func() error, prepare func()) (string, error) {
	var claim persist.CodexHostInputClaim
	var release func()
	var result json.RawMessage
	err := callComposerBackendAtWriteBoundaryPrepared(ctx, local, lane, barrier, b.conn, method, params, &result, func() error {
		if err := beginComposerDelivery(begin); err != nil {
			return err
		}
		var err error
		claim, release, err = d.beginCodexHostInput(local, b, client, expected)
		return err
	}, prepare, func() {
		if release != nil {
			release()
			release = nil
		}
	})
	if err != nil {
		if noActiveTurnToSteer(err) && claim.OwnerRevision != 0 {
			if refused := d.finishCodexHostInput(local, b, claim, "", true); refused != nil {
				return "", fmt.Errorf("%w: %v", protocol.ErrComposerOutcomeUnknown, refused)
			}
		}
		return "", err
	}
	native := ""
	if object, valid := decodeStrictObject(result); valid {
		if method == "turn/start" {
			if turn, valid := decodeStrictObject(object["turn"]); valid {
				native = executionString(turn["id"])
			}
		} else {
			native = executionString(object["turnId"])
		}
	}
	if claim.OwnerRevision != 0 {
		if err := d.finishCodexHostInput(local, b, claim, native, false); err != nil {
			return "", fmt.Errorf("%w: %v", protocol.ErrComposerOutcomeUnknown, err)
		}
	}
	return native, nil
}
