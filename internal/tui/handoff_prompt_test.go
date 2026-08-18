package tui

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

// supervisionModes is the closed vocabulary of ADR-010 Amendment 3 C1, in the
// order the form cycles through it.
var supervisionModes = []string{protocol.SupervisionPassive, protocol.SupervisionManual, protocol.SupervisionNone}

func TestRenderHandoffPrompt_StructuredAuthoringAndExactCLI(t *testing.T) {
	got, err := renderHandoffPrompt("claude", "opus", protocol.SupervisionPassive)
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
	// The one launch line carries the target, the model, the supervision mode and the
	// protected context file. Where the mode sits among the fixed pieces is a template
	// choice; the pieces and their shell quoting are the contract.
	line := lineContaining(got, `child_id="$(swarm handoff`)
	if line == "" {
		t.Fatalf("prompt does not capture the child id from stdout:\n%s", got)
	}
	for _, want := range []string{
		`child_id="$(swarm handoff --cli 'claude' --model 'opus'`,
		`--supervision 'passive'`,
		`--context-file "$handoff_file")"`,
	} {
		if !strings.Contains(line, want) {
			t.Errorf("launch line missing %q:\n%s", want, line)
		}
	}
	if len(got) > protocol.MaxSendInputText {
		t.Fatalf("prompt is %d bytes, over bound %d", len(got), protocol.MaxSendInputText)
	}
}

// Manual mode is Amendment 2 B4 unchanged: the source runs the multi-state watch loop.
func TestRenderHandoffPrompt_SupervisionLifecycle(t *testing.T) {
	got, err := renderHandoffPrompt("claude", "", protocol.SupervisionManual)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"swarm watch \"$child_id\" --until needs_input,ready_for_review,completed --timeout 10m",
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
	if !strings.Contains(got, `--supervision 'manual'`) {
		t.Errorf("manual prompt does not launch with --supervision 'manual':\n%s", got)
	}
}

// ADR-010 Amendment 3 C1: each mode renders the exact command with its mode and a
// mode-specific supervision tail. Passive is woken by the daemon's notification line
// and never polls; none launches, reports the child id and stops.
func TestRenderHandoffPrompt_ModeSpecificTails(t *testing.T) {
	cases := []struct {
		mode      string
		want      []string // case-insensitive substrings
		forbidden []string // case-insensitive substrings
	}{
		{
			mode:      protocol.SupervisionPassive,
			want:      []string{`--supervision 'passive'`, "[swarm supervision", "wait", "peek", "send", "never approve"},
			forbidden: []string{"--timeout", "watch"},
		},
		{
			mode: protocol.SupervisionManual,
			want: []string{`--supervision 'manual'`, `swarm watch "$child_id" --until needs_input,ready_for_review,completed --timeout 10m`},
		},
		{
			mode:      protocol.SupervisionNone,
			want:      []string{`--supervision 'none'`, "report", "stop", "$child_id"},
			forbidden: []string{"watch", "[swarm supervision", "--timeout"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			got, err := renderHandoffPrompt("claude", "opus", tc.mode)
			if err != nil {
				t.Fatal(err)
			}
			lower := strings.ToLower(got)
			for _, want := range tc.want {
				if !strings.Contains(lower, strings.ToLower(want)) {
					t.Errorf("%s prompt missing %q:\n%s", tc.mode, want, got)
				}
			}
			for _, forbidden := range tc.forbidden {
				if strings.Contains(lower, strings.ToLower(forbidden)) {
					t.Errorf("%s prompt must not contain %q:\n%s", tc.mode, forbidden, got)
				}
			}
			// The authoring half is shared by every mode.
			for _, common := range []string{"# Goal", "# Next actions", `child_id="$(swarm handoff --cli 'claude' --model 'opus'`} {
				if !strings.Contains(got, common) {
					t.Errorf("%s prompt missing shared authoring text %q:\n%s", tc.mode, common, got)
				}
			}
			if len(got) > protocol.MaxSendInputText {
				t.Errorf("%s prompt is %d bytes, over bound %d", tc.mode, len(got), protocol.MaxSendInputText)
			}
		})
	}
}

func TestRenderHandoffPrompt_UnknownSupervisionRefused(t *testing.T) {
	got, err := renderHandoffPrompt("claude", "opus", "eager")
	if err == nil {
		t.Fatalf("unknown supervision mode rendered a prompt:\n%s", got)
	}
}

func TestRenderHandoffPrompt_ExcludesLegacySurfaces(t *testing.T) {
	for _, mode := range supervisionModes {
		t.Run(mode, func(t *testing.T) {
			got, err := renderHandoffPrompt("claude", "opus", mode)
			if err != nil {
				t.Fatal(err)
			}
			lower := strings.ToLower(got)
			for _, forbidden := range []string{"skill", "plugin", "mcp", "/swarm-handoff", "swarm agents", "swarm spawn"} {
				if strings.Contains(lower, forbidden) {
					t.Errorf("%s prompt contains retired surface %q", mode, forbidden)
				}
			}
		})
	}
}

func TestRenderHandoffPrompt_ShellQuotesTargetAndModel(t *testing.T) {
	got, err := renderHandoffPrompt("cl'au de", "op'us; touch /tmp/nope", protocol.SupervisionPassive)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`--cli 'cl'"'"'au de'`, `--model 'op'"'"'us; touch /tmp/nope'`, `--supervision 'passive'`} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing safely quoted %q:\n%s", want, got)
		}
	}
}

func TestRenderHandoffPrompt_OversizeRefused(t *testing.T) {
	got, err := renderHandoffPrompt("claude", strings.Repeat("x", protocol.MaxSendInputText), protocol.SupervisionPassive)
	if err == nil {
		t.Fatalf("oversize prompt accepted (%d bytes)", len(got))
	}
	if !strings.Contains(err.Error(), "4096") {
		t.Fatalf("oversize error = %q, want protocol bound", err)
	}
}
