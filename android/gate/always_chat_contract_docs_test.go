package gate

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestAlwaysChatContract_CurrentNormativeSourcesAgree prevents the compatibility wire from
// silently becoming an Android navigation contract again. ADR-017 deliberately retains terminal
// view/control fields and verbs for rolling and non-Android consumers; every current Android
// source must nevertheless say that sessions open in one transcript/composer shell.
func TestAlwaysChatContract_CurrentNormativeSourcesAgree(t *testing.T) {
	root := repoRoot(t)
	wants := map[string][]string{
		"mobile/screen_coverage.tsv": {
			"session.destination\tApp.Session,App.Roster,SessionList.At",
			"production Android ignores the compatibility destination",
		},
		"docs/specifications/remote-phaseB-requirements.md": {
			"PB-BIND-3",
			"PB-APP-4",
			"AMENDED 2026-08-30",
			"one transcript and pinned composer shell",
		},
		"docs/specifications/mirror-program.md": {
			"CURRENT ANDROID AMENDMENT (2026-08-30)",
			"one conversation shell",
		},
		"docs/specifications/protocol.md": {
			"two explicit payload projections",
			"capability_transition payload",
		},
		"docs/specifications/interaction-schema.md": {
			"interaction or structured_gap",
			"capability_transition uses the separate capabilities field",
		},
		"docs/adr/ADR-013-mirror-capture-architecture.md": {
			"SUPERSEDED ON ANDROID, 2026-08-30",
			"wire compatibility",
		},
		"docs/operations/physical-handset-gate.md": {
			"PH-FALL-1..7 RETIRED ON ANDROID",
			"one conversation shell",
		},
	}
	for rel, needles := range wants {
		body := readFileOrFail(t, filepath.Join(root, filepath.FromSlash(rel)), "always-chat contract")
		for _, needle := range needles {
			if !strings.Contains(body, needle) {
				t.Errorf("%s has no %q; the current Android contract is not explicit there", rel, needle)
			}
		}
	}
}

func TestAlwaysChatContract_PhysicalGateDoesNotRequireRetiredFallbackScreens(t *testing.T) {
	body := readFileOrFail(t,
		filepath.Join(repoRoot(t), "docs", "operations", "physical-handset-gate.md"),
		"always-chat physical gate",
	)
	if strings.Contains(body, "**`[UNRUN]` PH-FALL-") {
		t.Error("the physical-handset release gate still requires production Android terminal-fallback screens; those rows are retired, while the machine/wire compatibility contract remains")
	}
}
