package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
)

// v0.3 — detectAgents derives a human-readable unavailability reason from the raw
// Detection so the launch picker can explain why an agent cannot launch. A usable
// (found + in-range) agent and a plainly not-installed one carry no reason (the
// latter keeps the existing install-hint behavior); a found-but-unusable agent
// carries the specific cause.
func TestUnavailabilityReason(t *testing.T) {
	cases := []struct {
		name string
		det  adapter.Detection
		want string
	}{
		{"usable in-range", adapter.Detection{Found: true, Version: "1.5.0", InRange: true}, ""},
		{"not installed", adapter.Detection{Found: false}, ""},
		{"found but version probe failed", adapter.Detection{Found: true, Version: "", InRange: false}, "version probe failed - reinstall?"},
		{"found but out of range", adapter.Detection{Found: true, Version: "3.0.0", InRange: false}, "unsupported version 3.0.0"},
		// bead 8c0: a crashed probe carries the CLI's own first error line; the
		// picker shows that real cause instead of the generic reinstall hint.
		{"found but probe crashed with a diagnostic", adapter.Detection{Found: true, Version: "", InRange: false, ProbeErr: "Missing optional dependency. Reinstall Codex."}, "Missing optional dependency. Reinstall Codex."},
	}
	for _, c := range cases {
		if got := unavailabilityReason(c.det); got != c.want {
			t.Errorf("%s: unavailabilityReason = %q, want %q", c.name, got, c.want)
		}
	}
}

// bead 8c0 — when swarm itself is an x86_64 binary running under Rosetta on Apple
// Silicon (the real cause of the codex node-shebang crash), a found-but-crashed
// agent's reason is augmented with a rebuild hint; usable, not-installed, and
// out-of-range agents are untouched, and the hint is absent when not translated.
func TestArchAugmentedReason(t *testing.T) {
	crashed := adapter.Detection{Found: true, Version: "", InRange: false, ProbeErr: "Reinstall Codex."}
	base := unavailabilityReason(crashed)

	if got := archAugmentedReason(base, crashed, false); got != base {
		t.Errorf("not translated: reason = %q, want unchanged %q", got, base)
	}
	got := archAugmentedReason(base, crashed, true)
	if !strings.Contains(got, base) {
		t.Errorf("translated: reason %q must still carry the CLI's own cause %q", got, base)
	}
	if !strings.Contains(strings.ToLower(got), "rosetta") || !strings.Contains(got, "arm64") {
		t.Errorf("translated: reason %q must add a Rosetta/arm64 rebuild hint", got)
	}
	// A usable agent (empty base reason) is never augmented, translated or not.
	if got := archAugmentedReason("", adapter.Detection{Found: true, Version: "1.5.0", InRange: true}, true); got != "" {
		t.Errorf("a usable agent must carry no reason, got %q", got)
	}
	// A plainly out-of-range agent (versioned) is not an arch symptom: not augmented.
	oor := adapter.Detection{Found: true, Version: "3.0.0", InRange: false}
	if got := archAugmentedReason(unavailabilityReason(oor), oor, true); got != "unsupported version 3.0.0" {
		t.Errorf("out-of-range agent must not get the arch hint, got %q", got)
	}
}

func TestDispatch(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{
			// No args opens the TUI (F1). Under `go test` stdout is not a terminal,
			// so the interactive-terminal guard fires with a clear error and a
			// non-zero exit — never a panic or a half-drawn screen. The real TUI path
			// (live daemon + PTY) is exercised by TestTUI_OpensAndRestoresOverPTY.
			name:       "no args, no tty, reports not-a-terminal",
			args:       []string{},
			wantExit:   1,
			wantStderr: "not a terminal",
		},
		{
			name:       "daemon subcommand routes to stub",
			args:       []string{"daemon"},
			wantExit:   1,
			wantStderr: "daemon: not implemented",
		},
		{
			name:       "shim subcommand without --config prints usage",
			args:       []string{"shim"},
			wantExit:   2,
			wantStderr: "usage",
		},
		{
			name:       "hook subcommand routes to stub",
			args:       []string{"hook"},
			wantExit:   1,
			wantStderr: "hook: not implemented",
		},
		{
			name:       "unknown subcommand prints usage",
			args:       []string{"bogus"},
			wantExit:   2,
			wantStderr: "usage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			gotExit := dispatch(tt.args, &stdout, &stderr)
			if gotExit != tt.wantExit {
				t.Errorf("dispatch(%v) exit = %d, want %d", tt.args, gotExit, tt.wantExit)
			}
			got := strings.ToLower(stderr.String())
			want := strings.ToLower(tt.wantStderr)
			if !strings.Contains(got, want) {
				t.Errorf("dispatch(%v) stderr = %q, want substring %q", tt.args, stderr.String(), tt.wantStderr)
			}
		})
	}
}

// codex v0.5 audit HIGH-1: overlayModelOptions was an identity stub, so discovered
// models never reached the form. These pin the integration seam.
func TestOverlayModelOptions(t *testing.T) {
	models := []adapter.ModelChoice{{ID: "gpt-5.6-sol", Display: "GPT-5.6-Sol"}, {ID: "gpt-5.5", Display: "GPT-5.5"}}
	specs := []adapter.OptionSpec{
		{Key: "model", Label: "Model", Type: "string"},
		{Key: "sandbox", Label: "Sandbox mode", Type: "choice"},
	}

	out := overlayModelOptions(specs, "gpt-5.6-sol", models)
	if out[0].Default != "gpt-5.6-sol" {
		t.Errorf("model Default = %q, want the configured model", out[0].Default)
	}
	if want := []string{"gpt-5.6-sol", "gpt-5.5"}; !reflect.DeepEqual(out[0].Suggest, want) {
		t.Errorf("model Suggest = %v, want discovered IDs %v", out[0].Suggest, want)
	}
	if out[1].Key != "sandbox" || out[1].Default != "" {
		t.Errorf("non-model option must be untouched, got %+v", out[1])
	}
	if specs[0].Default != "" || specs[0].Suggest != nil {
		t.Errorf("input specs mutated: %+v", specs[0])
	}

	// Claude shape: configured default, no enumeration - default prepended to the
	// curated aliases, deduped.
	claude := []adapter.OptionSpec{{Key: "model", Type: "string", Suggest: []string{"sonnet", "opus", "haiku"}}}
	out = overlayModelOptions(claude, "fable", nil)
	if out[0].Default != "fable" {
		t.Errorf("claude Default = %q, want fable", out[0].Default)
	}
	if want := []string{"fable", "sonnet", "opus", "haiku"}; !reflect.DeepEqual(out[0].Suggest, want) {
		t.Errorf("claude Suggest = %v, want default-first dedup %v", out[0].Suggest, want)
	}
	out = overlayModelOptions(claude, "opus", nil)
	if want := []string{"opus", "sonnet", "haiku"}; !reflect.DeepEqual(out[0].Suggest, want) {
		t.Errorf("dedup: Suggest = %v, want %v", out[0].Suggest, want)
	}

	// Nothing discovered: unchanged.
	out = overlayModelOptions(specs, "", nil)
	if !reflect.DeepEqual(out, specs) {
		t.Errorf("nothing discovered must return specs unchanged")
	}
}
