package claudeinput

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
)

func (b *Broker) registryIO(fn func() error) error {
	registry := b.manager
	b.mu.Unlock()
	err := fn()
	b.mu.Lock()
	if registry != b.manager || b.revoked {
		return managerproof.ErrQuarantined
	}
	return err
}

// Native hook requests acknowledge queue custody only. The MCP proof resolver
// waits for committed authority separately, outside the native event callback.
func (b *Broker) serveCustody(w http.ResponseWriter, r *http.Request, req request) bool {
	if b.custody == nil {
		return false
	}
	path := r.URL.Path
	async := false
	switch path {
	case "/interrupt-result", "/turn", "/activity", "/manager-append", "/manager-tool-step", "/manager-tool", "/manager-tool-complete", "/manager-idle":
		async = true
	case "/exchange":
		if req.OperationID == "" || req.State != "rejected" {
			return false
		}
		async = true
	case "/interrupt", "/manager-bind", "/manager-submit":
	default:
		return false
	}
	if req.Call != nil && len(req.Call.Arguments) > 0 {
		hash, err := managerproof.ArgumentsHash(req.Call.Arguments)
		if err != nil {
			http.Error(w, "invalid native arguments", http.StatusBadRequest)
			return true
		}
		req.Call.ArgumentsHash = hash
		req.Call.Arguments = nil
	}
	// Proof requests are resolved directly and never belong in this queue.
	req.ProofRequest = nil
	// The normalized encoding bounds retained strings and nested fields. The
	// fixed allowance covers the request structs, captured path, and tool hash.
	queued, err := json.Marshal(req)
	if err != nil {
		http.Error(w, "invalid custody request", http.StatusBadRequest)
		return true
	}
	size := 2048 + len(queued)
	var value any
	refused := false
	apply := func(context.Context) error {
		b.mu.Lock()
		defer b.mu.Unlock()
		if path == "/exchange" {
			message, ok := b.operations[req.OperationID]
			if !ok {
				if old, found := b.compactOperationLocked(req.OperationID); found && old.Worker == req.Worker {
					return nil
				}
				return managerproof.ErrUnbound
			}
			if req.Worker != b.worker || message.NativeInputID == "" || message.NativeTurnID != "" {
				return managerproof.ErrConflict
			}
			b.seen = time.Now()
			if err := b.registryIO(func() error { return b.manager.RejectInput(message.NativeInputID) }); err != nil {
				return err
			}
			b.pruneManagerTerminalLocked()
			return nil
		}
		respond := func(v any) { value = v }
		refuse := func(string) { refused = true }
		if !b.serveManagerLocked(path, req, respond, refuse) && !b.serveRuntimeLocked(path, req, b.liveLocked(), respond, refuse) {
			return managerproof.ErrInvalid
		}
		if refused {
			return managerproof.ErrUnavailable
		}
		b.pruneManagerTerminalLocked()
		if b.manager != nil {
			return b.manager.CustodyError()
		}
		return nil
	}
	if async {
		err = b.custody.Enqueue(size, apply)
	} else {
		err = b.custody.Do(r.Context(), size, apply)
	}
	if err != nil {
		http.Error(w, "native custody unavailable; fresh manager session required", http.StatusConflict)
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	if async {
		_ = json.NewEncoder(w).Encode(nil)
	} else {
		_ = json.NewEncoder(w).Encode(value)
	}
	return true
}

func (b *Broker) pruneManagerTerminalLocked() {
	if b.manager == nil || b.manager.CustodyError() != nil {
		return
	}
	for nonce, input := range b.managerInputs {
		if b.manager.InputDetailRetained(nonce) {
			continue
		}
		delete(b.operations, input.OperationID)
		delete(b.managerInputs, nonce)
	}
	kept := b.order[:0]
	for _, id := range b.order {
		if _, ok := b.operations[id]; ok {
			kept = append(kept, id)
		}
	}
	b.order = kept
}

// The caller holds b.mu; historical reads release it and never recreate proof.
func (b *Broker) compactOperationLocked(id string) (Message, bool) {
	if b.manager == nil || b.replay == nil {
		return Message{}, false
	}
	binding := b.manager.ImmutableBinding()
	b.mu.Unlock()
	receipt, err := b.replay.Operation(binding, id)
	b.mu.Lock()
	if err != nil {
		return Message{}, false
	}
	state := "started"
	if receipt.State == "rejected" {
		state = "rejected"
	}
	return Message{NativeInputID: receipt.Nonce, NativeTurnID: receipt.NativeTurnID, NativeMessageID: receipt.NativeMessageID, OperationID: receipt.OperationID, Hash: receipt.TextHash, State: state, Reason: receipt.Reason, Worker: Worker{NativeSessionID: receipt.Binding.NativeSessionID, Generation: receipt.WorkerGeneration, Version: receipt.ProviderVersion}}, true
}
