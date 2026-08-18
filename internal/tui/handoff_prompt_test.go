package tui

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestRenderHandoffPrompt_StructuredAuthoringAndExactCLI(t *testing.T) {
	got, err := renderHandoffPrompt("claude", "opus")
	if err != nil {
		t.Fatal(err)
	}
	for _, heading := range []string{
		"Goal", "Current state", "Decisions and constraints", "Evidence and validation", "Next actions", "Pointers",
	} {
		if !strings.Contains(got, heading) {
			t.Errorf("prompt missing section %q", heading)
		}
	}
	want := `swarm handoff --cli 'claude' --model 'opus' --context-file "$handoff_file"`
	if !strings.Contains(got, want) {
		t.Errorf("prompt missing exact CLI %q:\n%s", want, got)
	}
	if !strings.Contains(got, `child_id="$(swarm handoff`) {
		t.Errorf("prompt does not capture the child id from stdout:\n%s", got)
	}
	if len(got) > protocol.MaxSendInputText {
		t.Fatalf("prompt is %d bytes, over bound %d", len(got), protocol.MaxSendInputText)
	}
}

func TestRenderHandoffPrompt_SupervisionLifecycle(t *testing.T) {
	got, err := renderHandoffPrompt("claude", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"swarm watch \"$child_id\" --until needs_input,ready_for_review,completed --timeout",
		"swarm peek \"$child_id\"",
		"swarm send \"$child_id\"",
		"swarm watch \"$child_id\" --until change --timeout",
		"permission", "never approve", "independently validate", "completed", "exit 2", "still working",
	} {
		if !strings.Contains(strings.ToLower(got), strings.ToLower(want)) {
			t.Errorf("prompt missing supervision rule %q", want)
		}
	}
	if !strings.Contains(got, "Stop editing the shared checkout") {
		t.Error("prompt does not tell source to stop editing after launch")
	}
}

func TestRenderHandoffPrompt_ExcludesLegacySurfaces(t *testing.T) {
	got, err := renderHandoffPrompt("claude", "opus")
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(got)
	for _, forbidden := range []string{"skill", "plugin", "mcp", "/swarm-handoff", "swarm agents", "swarm spawn"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("prompt contains retired surface %q", forbidden)
		}
	}
}

func TestRenderHandoffPrompt_ShellQuotesTargetAndModel(t *testing.T) {
	got, err := renderHandoffPrompt("cl'au de", "op'us; touch /tmp/nope")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`--cli 'cl'"'"'au de'`, `--model 'op'"'"'us; touch /tmp/nope'`} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing safely quoted %q:\n%s", want, got)
		}
	}
}

func TestRenderHandoffPrompt_OversizeRefused(t *testing.T) {
	got, err := renderHandoffPrompt("claude", strings.Repeat("x", protocol.MaxSendInputText))
	if err == nil {
		t.Fatalf("oversize prompt accepted (%d bytes)", len(got))
	}
	if !strings.Contains(err.Error(), "4096") {
		t.Fatalf("oversize error = %q, want protocol bound", err)
	}
}
