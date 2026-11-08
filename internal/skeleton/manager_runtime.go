package skeleton

import (
	"context"
	"github.com/Nathandela/swarm/internal/adapter/codex"
	"strconv"
	"time"

	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func (a *coreAPI) ManagerInputStatus(ctx context.Context, local string, req protocol.ManagerAdmission) (protocol.ManagerNativeTurn, error) {
	if a.managerInputStatus == nil {
		return protocol.ManagerNativeTurn{}, managerproof.ErrUnavailable
	}
	return a.managerInputStatus(ctx, local, req)
}
func (a *coreAPI) ManagerRuntime(local, instance string) (protocol.ManagerRuntimeStatus, error) {
	if a.managerRuntime == nil {
		return protocol.ManagerRuntimeStatus{}, managerproof.ErrUnavailable
	}
	return a.managerRuntime(local, instance)
}
func (a *coreAPI) ResolveManagerCall(ctx context.Context, local string, req protocol.ManagerCallReq) (protocol.ManagerCallProof, error) {
	if a.managerCall == nil {
		return protocol.ManagerCallProof{}, managerproof.ErrUnavailable
	}
	return a.managerCall(ctx, local, req)
}
func (d *Daemon) managerMeta(local, instance string) (persist.Meta, string, error) {
	if d.core == nil {
		return persist.Meta{}, "", managerproof.ErrUnavailable
	}
	meta, ok := d.core.Get(local)
	if !ok || meta.Role != "manager" || meta.ManagerConversationID == "" || meta.ManagerBridgeSocket == "" || meta.ManagerAuthFile == "" || meta.CLIIdentity == nil || meta.Status.Process != status.ProcessRunning || meta.ResumeError != "" {
		return persist.Meta{}, "", managerproof.ErrUnavailable
	}
	current, ok := d.sessionInstance(local)
	if !ok || instance == "" || current != instance {
		return persist.Meta{}, "", managerproof.ErrStale
	}
	if d.logins != nil {
		if meta.LoginRuntime != nil && d.logins.store == nil {
			return persist.Meta{}, "", managerproof.ErrUnavailable
		}
		if err := d.logins.inputGate(local); err != nil {
			return persist.Meta{}, "", managerproof.ErrStale
		}
	}
	epoch := ""
	if meta.LoginRuntime != nil {
		if d.logins == nil || d.logins.store == nil {
			return persist.Meta{}, "", managerproof.ErrUnavailable
		}
		if err := d.logins.store.ValidateRuntime(*meta.LoginRuntime); err != nil {
			return persist.Meta{}, "", managerproof.ErrStale
		}
		epoch = "registry:" + strconv.FormatUint(meta.LoginRuntime.Epoch, 10) + ":" + meta.LoginRuntime.Identity
	} else {
		identity := ManagerNativeIdentity(meta)
		if identity == "" || identity != meta.AuthIdentity {
			return persist.Meta{}, "", managerproof.ErrStale
		}
		epoch = "identity:" + identity
	}
	if _, err := d.core.SessionNativeGeneration(local); err != nil {
		return persist.Meta{}, "", managerproof.ErrUnavailable
	}
	return meta, epoch, nil
}
func (d *Daemon) managerRuntime(local, instance string) (managerproof.Status, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return d.managerRuntimeContext(ctx, local, instance)
}
func (d *Daemon) managerRuntimeContext(ctx context.Context, local, instance string) (managerproof.Status, error) {
	meta, epoch, err := d.managerMeta(local, instance)
	if err != nil {
		return managerproof.Status{}, err
	}
	binding := managerproof.Binding{Session: local, SessionInstance: instance, ConversationID: meta.ManagerConversationID, Provider: meta.AgentType, LoginEpoch: epoch}
	var result managerproof.Status
	switch meta.AgentType {
	case "claude":
		if !claudeinput.SupportsNativeTurns(meta.CLIIdentity.Version) {
			return result, managerproof.ErrUnavailable
		}
		desc, err := d.core.SessionClaudeInput(local)
		if err != nil || desc.ManagerConversationID != meta.ManagerConversationID {
			return result, managerproof.ErrUnavailable
		}
		client := claudeinput.NewClient(desc)
		worker, err := client.Ready(ctx)
		if err != nil {
			return result, managerproof.ErrUnavailable
		}
		binding.NativeSessionID = worker.NativeSessionID
		binding.NativeGeneration, err = d.core.SessionNativeGeneration(local)
		if err != nil {
			return result, managerproof.ErrUnavailable
		}
		binding.ObservationGeneration, err = client.ManagerObservation(ctx, worker)
		if err != nil {
			return result, managerproof.ErrUnavailable
		}
		if err := client.BindManager(ctx, worker, binding); err != nil {
			return result, managerproof.ErrStale
		}
		turn, err := client.Turn(ctx, worker)
		if err != nil {
			return result, managerproof.ErrUnavailable
		}
		result = managerproof.Status{Binding: binding, ReadyForInput: turn.ReadyForInput, Turn: managerproof.Turn{NativeInputID: turn.NativeInputID, NativeTurnID: turn.TurnID, State: turn.State, Reason: turn.Reason, ExternalInput: turn.ExternalInput, ExternallySteered: turn.ExternallySteered}}
	case "codex":
		if !codex.SupportsNativeTurns(meta.CLIIdentity.Version) {
			return result, managerproof.ErrUnavailable
		}
		backend, ok := d.sessionBackendFor(local)
		if !ok || backend.feed == nil || backend.feed.retired.Load() || backend.sessionInstance != instance || backend.threadID == "" {
			return result, managerproof.ErrUnavailable
		}
		binding.NativeSessionID = backend.threadID
		binding.ObservationGeneration = backend.feed.epoch
		binding.NativeGeneration, err = d.core.SessionNativeGeneration(local)
		if err != nil {
			return result, managerproof.ErrUnavailable
		}
		registry, err := d.codexManagerRegistry(local, binding)
		if err != nil {
			return result, managerproof.ErrUnavailable
		}
		result, err = registry.Snapshot()
		if err != nil {
			return result, err
		}
	default:
		return result, managerproof.ErrUnavailable
	}
	if ctx.Err() != nil {
		return managerproof.Status{}, ctx.Err()
	}
	result.BridgeSocket = meta.ManagerBridgeSocket
	result.AuthFile = meta.ManagerAuthFile
	return result, nil
}
func (d *Daemon) checkedManagerBinding(local string, supplied managerproof.Binding) (managerproof.Status, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return d.checkedManagerBindingContext(ctx, local, supplied)
}
func (d *Daemon) checkedManagerBindingContext(ctx context.Context, local string, supplied managerproof.Binding) (managerproof.Status, error) {
	if supplied.Session != local && (d.api == nil || supplied.Session != protocol.NamespacedID(d.api.endpointID, local)) {
		return managerproof.Status{}, managerproof.ErrStale
	}
	supplied.Session = local
	current, err := d.managerRuntimeContext(ctx, local, supplied.SessionInstance)
	if err != nil {
		return current, err
	}
	if current.Binding != supplied {
		return managerproof.Status{}, managerproof.ErrStale
	}
	return current, nil
}
func (d *Daemon) resolveManagerCall(ctx context.Context, local string, req managerproof.CallRequest) (managerproof.Proof, error) {
	current, err := d.checkedManagerBindingContext(ctx, local, req.Binding)
	if err != nil {
		return managerproof.Proof{}, err
	}
	req.Binding = current.Binding
	if current.Turn.ExternallySteered {
		return managerproof.Proof{}, managerproof.ErrStale
	}
	var proof managerproof.Proof
	switch req.Binding.Provider {
	case "claude":
		desc, err := d.core.SessionClaudeInput(local)
		if err != nil {
			return proof, managerproof.ErrUnavailable
		}
		client := claudeinput.NewClient(desc)
		worker, err := client.Ready(ctx)
		if err != nil {
			return proof, managerproof.ErrUnavailable
		}
		proof, err = client.ResolveManagerCall(ctx, worker, req)
		if err != nil {
			return proof, managerproof.ErrUnavailable
		}
	case "codex":
		registry, err := d.codexManagerRegistry(local, req.Binding)
		if err != nil {
			return proof, err
		}
		proof, err = registry.Resolve(ctx, req)
		if err != nil {
			return proof, err
		}
	default:
		return proof, managerproof.ErrUnavailable
	}
	latest, err := d.checkedManagerBindingContext(ctx, local, req.Binding)
	if err != nil || latest.Turn.ExternallySteered || latest.Turn.State != "running" || latest.Turn.NativeInputID != proof.NativeInputID || latest.Turn.NativeTurnID != proof.NativeTurnID {
		return managerproof.Proof{}, managerproof.ErrStale
	}
	return proof, nil
}

func (d *Daemon) managerInputStatus(ctx context.Context, local string, req protocol.ManagerAdmission) (protocol.ManagerNativeTurn, error) {
	current, err := d.checkedManagerBindingContext(ctx, local, req.Binding)
	if err != nil {
		return protocol.ManagerNativeTurn{}, err
	}
	switch current.Binding.Provider {
	case "claude":
		desc, err := d.core.SessionClaudeInput(local)
		if err != nil {
			return protocol.ManagerNativeTurn{}, managerproof.ErrUnavailable
		}
		client := claudeinput.NewClient(desc)
		worker, err := client.Ready(ctx)
		if err != nil {
			return protocol.ManagerNativeTurn{}, managerproof.ErrUnavailable
		}
		return client.ManagerInputStatus(ctx, worker, current.Binding, req.Nonce)
	case "codex":
		registry, err := d.codexManagerRegistry(local, current.Binding)
		if err != nil {
			return protocol.ManagerNativeTurn{}, err
		}
		return registry.InputStatus(req.Nonce)
	default:
		return protocol.ManagerNativeTurn{}, managerproof.ErrUnavailable
	}
}
