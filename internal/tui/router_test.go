package tui

import (
	"strings"
	"testing"
	"time"
)

// E7.1 — the app starts on the GENERAL view. Only the router is the shared shell;
// the general/launch/attach sub-models are dispatched to.

func TestRouter_StartsOnGeneralView(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "compiling", 3*time.Minute))
	m := newModel(t, f, detectMixed())

	v := view(m)
	// The general view shows the group scaffold and its footer keymap.
	if !strings.Contains(v, "WORKING") {
		t.Fatalf("expected general view group header WORKING, got:\n%s", v)
	}
	if !strings.Contains(v, "new") { // footer "n new"
		t.Fatalf("expected general-view footer keymap (n new), got:\n%s", v)
	}
}

// E7.1 — router transitions general -> launch on `n`, and launch -> general on Esc.

func TestRouter_GeneralToLaunchAndBack(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "codex", "~/Code/x", "compiling", 3*time.Minute))
	m := newModel(t, f, detectMixed())

	m = send(m, keyRune('n'))
	v := view(m)
	if !strings.Contains(v, "new session") || !strings.Contains(v, "directory") {
		t.Fatalf("expected launch form (new session / directory) after `n`, got:\n%s", v)
	}
	if strings.Contains(v, "WORKING") {
		t.Fatalf("launch form must not render the general-view groups, got:\n%s", v)
	}

	m = send(m, keyEsc)
	v = view(m)
	if !strings.Contains(v, "WORKING") {
		t.Fatalf("expected general view (WORKING header) after Esc from launch, got:\n%s", v)
	}
}

// E7.1 — router transitions general -> attach on Enter over a row, and attach ->
// general on Esc. (Attach passthrough itself is Epic 8; Epic 7 only proves the
// router hosts the attach sub-model and carries the selected session into it.)

func TestRouter_GeneralToAttachAndBack(t *testing.T) {
	sel := sNeedsInput("endpoint/s1", "claude", "~/Code/quanthome-api", "Permission: run migration?", 12*time.Minute)
	f := newFakeClient(sel, sWorking("endpoint/s2", "codex", "~/Code/agents-tracker", "writing tests", 3*time.Minute))
	m := newModel(t, f, detectMixed())

	// First row (needs-input claude) is selected by default; Enter attaches to it.
	m = send(m, keyEnter)
	v := view(m)
	if !strings.Contains(v, "claude") || !strings.Contains(v, "~/Code/quanthome-api") {
		t.Fatalf("attach screen must identify the selected session (agent + cwd), got:\n%s", v)
	}
	if strings.Contains(v, "n new") {
		t.Fatalf("attach screen must not render the general-view footer, got:\n%s", v)
	}

	m = send(m, keyEsc)
	v = view(m)
	if !strings.Contains(v, "NEEDS INPUT") || !strings.Contains(v, "new") {
		t.Fatalf("expected general view after Esc from attach, got:\n%s", v)
	}
}
