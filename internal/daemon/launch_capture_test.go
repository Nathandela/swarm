package daemon

// FAILING-FIRST (TDD RED, GG-5) for ADR-010 §6's carriage, layer 3: the fifth per-session hook
// variable. A `swarm hook` process knows its event name but NOT the adapter that launched it,
// so the capture rows the adapter declared in its SignalSources have to reach it the same way
// its token does -- injected at spawn, post allowlist-filter.
//
// The daemon does not resolve the adapter itself (internal/daemon imports no adapter package,
// by layering): the assembly composes the rows into LaunchSpec.CaptureEvents beside the argv it
// already composes there, and this layer only carries them.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/persist"
)

// TestInjectHookEnv_CarriesTheAdaptersCaptureRows.
func TestInjectHookEnv_CarriesTheAdaptersCaptureRows(t *testing.T) {
	filtered := persist.FilterEnv([]string{"CONDA_PREFIX=/x"})
	rows := []string{"UserPromptSubmit", "PreToolUse", "Stop"}

	got := injectHookEnv(filtered, "sid-1", "tok-abc", "/run/d.sock", "/state/sid-1/hook.seq", rows)

	want := hookclient.EnvCapture + "=" + hookclient.CaptureEnv(rows)
	if lineIndex(got, want) < 0 {
		t.Fatalf("injected env missing %q; got %v. Without it every hook post flattens its body away "+
			"and the shipped producer shapes nothing (ADR-010 §6)", want, got)
	}
}

// TestInjectHookEnv_ASessionWithNoCaptureRowsDeclaresNone. Every shipped adapter but claude
// implements no capture extension, and ADR-010 §5 makes that a fully supported state, never a
// defect. The variable is still injected -- empty -- so a hook reads one contract rather than
// two ("absent" and "empty" are the same answer, and neither captures).
func TestInjectHookEnv_ASessionWithNoCaptureRowsDeclaresNone(t *testing.T) {
	got := injectHookEnv(nil, "sid-1", "tok-abc", "/run/d.sock", "/state/sid-1/hook.seq", nil)

	if want := hookclient.EnvCapture + "="; lineIndex(got, want) < 0 {
		t.Fatalf("injected env missing %q; got %v", want, got)
	}
}
