package skeleton

import (
	"context"
	"errors"
	"log"
	"sync"

	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

type openCodeObservations struct {
	mu       sync.Mutex
	closed   bool
	sessions map[string]*openCodeObservation
}

type openCodeObservation struct {
	identity      opencodebridge.Identity
	cancel        context.CancelFunc
	done          chan struct{}
	ready         chan struct{}
	readyErr      error
	view          OpenCodeView
	lastStatus    status.Status
	persistFailed bool
}

// openCodeReady is the native launch barrier: observe before any prompt is dispatched.
func (d *Daemon) openCodeReady(ctx context.Context, meta persist.Meta) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-d.closing:
		return errors.New("OpenCode observation is closing")
	case <-d.ready:
	}
	binding, err := d.core.SessionOpenCodeBridge(meta.ID)
	if err != nil {
		return err
	}
	identity := opencodebridge.Identity{Version: binding.Version, WorkerID: binding.WorkerID, SessionInstance: binding.SessionInstance, RootID: binding.RootID, Directory: binding.Directory, BackendGeneration: binding.BackendGeneration, NativeCreatedAtMS: binding.NativeCreatedAtMS}
	current, ok := d.core.Get(meta.ID)
	if !ok || !openCodeMetaMatches(current, identity) {
		return errors.New("OpenCode observation binding changed")
	}
	d.openCode.mu.Lock()
	if d.openCode.closed {
		d.openCode.mu.Unlock()
		return errors.New("OpenCode observation is closed")
	}
	old := d.openCode.sessions[meta.ID]
	reuse := old != nil && old.identity == identity
	if reuse {
		select {
		case <-old.ready:
			reuse = old.readyErr == nil
		default:
		}
	}
	if reuse {
		d.openCode.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-old.done:
			return errors.New("OpenCode observation stopped")
		case <-old.ready:
			return old.readyErr
		}
	}
	client, err := opencodebridge.NewClient(binding.SocketPath, binding.Token)
	if err != nil {
		d.openCode.mu.Unlock()
		return err
	}
	state, err := NewOpenCodeState(identity.RootID, identity.BackendGeneration)
	if err != nil {
		client.Close()
		d.openCode.mu.Unlock()
		return err
	}
	observeCtx, cancel := context.WithCancel(context.Background())
	stopStartupCancellation := context.AfterFunc(ctx, cancel)
	observation := &openCodeObservation{identity: identity, cancel: cancel, done: make(chan struct{}), ready: make(chan struct{})}
	if d.openCode.sessions == nil {
		d.openCode.sessions = make(map[string]*openCodeObservation)
	}
	d.openCode.sessions[meta.ID] = observation
	if old != nil {
		old.cancel()
	}
	d.openCode.mu.Unlock()
	if old != nil {
		<-old.done
	}
	go func() {
		defer close(observation.done)
		defer client.Close()
		observeOpenCode(observeCtx, client, identity, state, func(view OpenCodeView) { d.publishOpenCodeView(meta.ID, observation, view) }, func(err error) {
			stopStartupCancellation()
			observation.readyErr = err
			close(observation.ready)
		})
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-observation.done:
		return errors.New("OpenCode observation stopped")
	case <-observation.ready:
		return observation.readyErr
	}
}

func openCodeMetaMatches(meta persist.Meta, identity opencodebridge.Identity) bool {
	return identity.Version == 1 && meta.ID == identity.WorkerID && meta.AgentType == "opencode" && meta.RequiredRuntime == persist.RequiredRuntimeOpenCode && meta.Status.Process == status.ProcessRunning &&
		meta.OpenCodeBinding != nil && meta.OpenCodeBinding.Phase == persist.OpenCodePhaseReady && meta.OpenCodeBinding.Generation == identity.BackendGeneration && identity.SessionInstance == identity.BackendGeneration &&
		meta.ConversationID == identity.RootID && meta.ProviderCwd() == identity.Directory && identity.NativeCreatedAtMS > 0 && meta.OpenCodeBinding.NativeCreatedAtMS == identity.NativeCreatedAtMS
}

func (d *Daemon) publishOpenCodeView(local string, observation *openCodeObservation, view OpenCodeView) {
	d.openCode.mu.Lock()
	if d.openCode.closed || d.openCode.sessions[local] != observation {
		d.openCode.mu.Unlock()
		return
	}
	meta, ok := d.core.Get(local)
	if !ok || !openCodeMetaMatches(meta, observation.identity) {
		d.openCode.mu.Unlock()
		return
	}
	observation.view = view
	nativeStatus := openCodeProjectedStatus(view)
	if nativeStatus != observation.lastStatus {
		if err := d.core.SetOpenCodeStatus(local, observation.identity.BackendGeneration, nativeStatus); err != nil {
			if !observation.persistFailed {
				log.Printf("skeleton: OpenCode status persistence failed for %s: %v", local, err)
			}
			observation.persistFailed = true
		} else {
			observation.persistFailed = false
			observation.lastStatus = nativeStatus
		}
	}
	d.openCode.mu.Unlock()
	d.checkpointOpenCodeOutcomes(local, view)
	if d.api != nil {
		d.api.pokeWatch()
	}
	if d.sup != nil {
		d.sup.signal(local)
	}
}

func (d *Daemon) openCodeView(local string) (OpenCodeView, bool) {
	d.openCode.mu.Lock()
	defer d.openCode.mu.Unlock()
	observation := d.openCode.sessions[local]
	if d.openCode.closed || observation == nil || observation.view.Epoch == 0 || d.core == nil {
		return OpenCodeView{}, false
	}
	meta, ok := d.core.Get(local)
	if !ok || !openCodeMetaMatches(meta, observation.identity) {
		return OpenCodeView{}, false
	}
	return cloneOpenCodeView(observation.view), true
}

func cloneOpenCodeView(view OpenCodeView) OpenCodeView {
	view.Inputs = append([]OpenCodeInput(nil), view.Inputs...)
	view.Messages = append([]OpenCodeMessage(nil), view.Messages...)
	view.Uncertain = append([]OpenCodeUncertain(nil), view.Uncertain...)
	view.Pending = append([]OpenCodeRequest(nil), view.Pending...)
	for i := range view.Pending {
		view.Pending[i] = cloneOpenCodeRequest(view.Pending[i])
	}
	view.Descendants = append([]OpenCodeDescendant(nil), view.Descendants...)
	for i := range view.Descendants {
		child := &view.Descendants[i]
		child.Status = *cloneStatus(&child.Status)
		if child.Error != nil {
			value := *child.Error
			child.Error = &value
		}
	}
	view.Retry = cloneStatus(view.Retry)
	if view.Current != nil {
		value := *view.Current
		view.Current = &value
	}
	if view.Error != nil {
		value := *view.Error
		view.Error = &value
	}
	return view
}

func (d *Daemon) stopOpenCodeObservation(local string) {
	defer func() {
		d.nativeRequests.mu.Lock()
		delete(d.nativeRequests.sessions, local)
		d.nativeRequests.mu.Unlock()
	}()
	d.openCode.mu.Lock()
	observation := d.openCode.sessions[local]
	delete(d.openCode.sessions, local)
	if observation != nil {
		observation.cancel()
	}
	d.openCode.mu.Unlock()
	if observation != nil {
		<-observation.done
	}
	d.nativeReceipts.mu.Lock()
	delete(d.nativeReceipts.sessions, local)
	d.nativeReceipts.mu.Unlock()
}

func (d *Daemon) stopOpenCodeObservations() {
	defer func() { d.nativeRequests.mu.Lock(); d.nativeRequests.sessions = nil; d.nativeRequests.mu.Unlock() }()
	d.openCode.mu.Lock()
	d.openCode.closed = true
	sessions := d.openCode.sessions
	d.openCode.sessions = nil
	for _, observation := range sessions {
		observation.cancel()
	}
	d.openCode.mu.Unlock()
	for _, observation := range sessions {
		<-observation.done
	}
	d.nativeReceipts.mu.Lock()
	d.nativeReceipts.sessions = nil
	d.nativeReceipts.mu.Unlock()
}
