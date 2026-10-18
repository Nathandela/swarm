package shim

import (
	"context"
	"errors"
	"time"

	"github.com/Nathandela/swarm/internal/opencodebridge"
)

var errNativeDispatchUnknown = errors.New("native_dispatch_uncertain")
var errNativeBinding = errors.New("native_binding_mismatch")

// Hold the same guard as raw owner input until the native request has settled.
func (p *ptyWriter) dispatchOpenCode(cfg *OpenCodeConfig, req opencodebridge.DispatchRequest) (opencodebridge.Receipt, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || cfg == nil {
		return opencodebridge.Receipt{}, errNativeBinding
	}
	if p.embargo != nil && p.embargo.held.Load() {
		return opencodebridge.Receipt{}, p.embargo.refusal
	}
	if p.inputLine.dirty() {
		return opencodebridge.Receipt{}, errInputBusy
	}
	b, err := opencodebridge.ReadBinding(cfg.BindingFile)
	if err != nil || b.RootID != cfg.RootID || b.BackendGeneration != cfg.Generation || req.Version != b.Version || req.RootID != b.RootID || req.BackendGeneration != b.BackendGeneration || req.SessionInstance != b.SessionInstance {
		return opencodebridge.Receipt{}, errNativeBinding
	}
	client, err := opencodebridge.NewClient(b.SocketPath, b.Token)
	if err != nil {
		return opencodebridge.Receipt{}, errNativeBinding
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := client.Dispatch(ctx, req)
	if err != nil {
		return opencodebridge.Receipt{}, errNativeDispatchUnknown
	}
	return result, nil
}
