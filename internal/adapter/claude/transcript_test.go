package claude

// FAILING-FIRST (TDD RED, GG-5) for the hands-off-handoff sweep, phase 3: the claude
// adapter learns its OWN transcript layout, so the daemon can NAME a transcript path
// without any layer above it duplicating provider knowledge.
//
// THE CONTRACT these tests freeze:
//
//	type TranscriptLayout interface {
//		ProjectDirName(cwd string) string
//		TranscriptFileName(convID string) string
//	}
//	func AsTranscriptLayout(a Adapter) (TranscriptLayout, bool)
//
// WHY TWO METHODS AND NOT ONE TranscriptPath(id). The resume-history MIGRATION path
// (internal/skeleton/resume_history.go resolveClaude) searches for an id it does not yet
// know: it needs the DIRECTORY first and reads the ids out of it. A single path-composing
// method cannot serve that caller, so the seam splits at exactly the join the two callers
// disagree about.
//
// WHY BOTH HALVES ARE PURE. Neither method opens, stats or reads anything. All I/O stays
// in the daemon's existing anchored, budgeted resolver (os.Root plus the history budgets),
// which is the only party that may touch the disk. The adapter names; the core opens --
// the same division Command/Resume and Detect(a, HostProber) already draw.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/agy"
	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/adapter/opencode"
)

// TestTranscriptLayout_ClaudeImplementsTheOptionalExtension compile-pins the additive
// seam. Transcript layout is OPTIONAL exactly as InteractionSource and TurnInterrupter
// are: it must not widen the frozen Adapter method set, and a provider whose layout
// nobody has characterized stays complete and fully supported.
func TestTranscriptLayout_ClaudeImplementsTheOptionalExtension(t *testing.T) {
	var _ adapter.TranscriptLayout = New()
	var _ func(adapter.Adapter) (adapter.TranscriptLayout, bool) = adapter.AsTranscriptLayout //nolint:staticcheck // compile-pin the exact optional seam

	if layout, ok := adapter.AsTranscriptLayout(New()); !ok || layout == nil {
		t.Fatalf("AsTranscriptLayout(claude) = (%v, %v), want a non-nil layout", layout, ok)
	}
}

// TestTranscriptLayout_ProjectDirNameMatchesTheRealCLIEncoder is the golden table for
// Claude Code's project-directory encoder.
//
// PROVENANCE MATTERS HERE, so every row carries it. Only the two "real claude CLI" rows
// are EVIDENCE: they were captured on 2026-08-26 by running the actual `claude` CLI with
// cwd set to the named directory and reading back the directory the CLI itself created
// under ~/.claude/projects. Every other row is CHARACTERIZATION of swarm's current Go
// implementation -- it records what we do today so a change is visible, and it is NOT a
// claim about what the CLI does.
//
// THE NON-ASCII ROW IS THE ONE WITH TEETH. It proves the encoder is RUNE-wise, not
// BYTE-wise: 'e-acute' is two UTF-8 bytes and the real CLI emitted exactly ONE dash for
// it, and each CJK ideograph is three UTF-8 bytes and emitted exactly ONE dash each. A
// byte-wise implementation would have written "caf---t--st" for the accented directory
// and seven trailing dashes for the two-ideograph one; the CLI wrote "caf--t-st" and
// three. Comparing the function to itself would have proved neither.
func TestTranscriptLayout_ProjectDirNameMatchesTheRealCLIEncoder(t *testing.T) {
	layout, ok := adapter.AsTranscriptLayout(New())
	if !ok {
		t.Fatal("Claude does not implement TranscriptLayout")
	}
	cases := []struct {
		name       string
		provenance string
		cwd        string
		want       string
	}{
		{
			name:       "non-ASCII path proves the encoder is rune-wise",
			provenance: "OBSERVED from the real claude CLI (2026-08-26)",
			cwd:        "/Users/Nathan/.claude/jobs/20bd7184/tmp/café.tëst/测试",
			want:       "-Users-Nathan--claude-jobs-20bd7184-tmp-caf--t-st---",
		},
		{
			name:       "the worktree this sweep runs in",
			provenance: "OBSERVED from the real claude CLI (2026-08-26)",
			cwd:        "/Users/Nathan/Code/swarm/.claude/worktrees/hands-off-handoff",
			want:       "-Users-Nathan-Code-swarm--claude-worktrees-hands-off-handoff",
		},
		{
			name:       "trailing slash keeps its dash",
			provenance: "CHARACTERIZATION of the current Go implementation, not observed CLI output",
			cwd:        "/Users/Nathan/Code/",
			want:       "-Users-Nathan-Code-",
		},
		{
			name:       "spaces become dashes",
			provenance: "CHARACTERIZATION of the current Go implementation, not observed CLI output",
			cwd:        "/Users/Nathan/My Repo",
			want:       "-Users-Nathan-My-Repo",
		},
		{
			name:       "dots become dashes",
			provenance: "CHARACTERIZATION of the current Go implementation, not observed CLI output",
			cwd:        "/Users/Nathan/.config/app.v2",
			want:       "-Users-Nathan--config-app-v2",
		},
		{
			name:       "bare root",
			provenance: "CHARACTERIZATION of the current Go implementation, not observed CLI output",
			cwd:        "/",
			want:       "-",
		},
		{
			name:       "empty cwd encodes to empty",
			provenance: "CHARACTERIZATION of the current Go implementation, not observed CLI output",
			cwd:        "",
			want:       "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := layout.ProjectDirName(tc.cwd); got != tc.want {
				t.Fatalf("ProjectDirName(%q) = %q, want %q\n  provenance: %s", tc.cwd, got, tc.want, tc.provenance)
			}
		})
	}
}

// TestTranscriptLayout_ProjectDirNameIsPureAndDeterministic holds the seam to the same
// terms every other pure adapter method is held to: same input, same output, and no
// dependence on anything outside the argument.
func TestTranscriptLayout_ProjectDirNameIsPureAndDeterministic(t *testing.T) {
	layout, ok := adapter.AsTranscriptLayout(New())
	if !ok {
		t.Fatal("Claude does not implement TranscriptLayout")
	}
	const cwd = "/Users/Nathan/Code/swarm"
	first := layout.ProjectDirName(cwd)
	if second := layout.ProjectDirName(cwd); second != first {
		t.Fatalf("ProjectDirName is not deterministic: %q then %q", first, second)
	}
}

// TestTranscriptLayout_TranscriptFileNameNamesTheFileExactly pins the second half.
//
// The real capture (2026-08-26) recorded the transcript as
// "f41b0e35-6fa4-4c8b-bfea-8687b311255b.jsonl", whose stem is exactly the record's own
// sessionId -- so the file name is the conversation id plus ".jsonl" and nothing else.
//
// THE PROJECT DIRECTORY IS NOT FLAT. That same capture found a `memory` entry sitting
// beside the .jsonl. A resolver must therefore NAME this file exactly and must never glob
// the directory: a glob would pick up entries that are not transcripts at all.
//
// IT DOES NOT SANITIZE, BY DESIGN. The function is a pure naming rule, so a junk id is
// named just as faithfully as a canonical one. The CALLER owes both checks -- validate
// with adapter.IsCanonicalConversationID, then open under an os.Root anchor -- because
// filepath.Join CLEANS, and a stored id of "../../../../etc/passwd" would otherwise
// resolve outside the projects root entirely. The last row records that this function is
// not the layer that stops it.
func TestTranscriptLayout_TranscriptFileNameNamesTheFileExactly(t *testing.T) {
	layout, ok := adapter.AsTranscriptLayout(New())
	if !ok {
		t.Fatal("Claude does not implement TranscriptLayout")
	}
	cases := []struct {
		name       string
		provenance string
		convID     string
		want       string
	}{
		{
			name:       "canonical id names the captured transcript",
			provenance: "OBSERVED from the real claude CLI (2026-08-26)",
			convID:     "f41b0e35-6fa4-4c8b-bfea-8687b311255b",
			want:       "f41b0e35-6fa4-4c8b-bfea-8687b311255b.jsonl",
		},
		{
			name:       "empty id is still named, never guessed at",
			provenance: "CHARACTERIZATION: the seam does not validate; the caller does",
			convID:     "",
			want:       ".jsonl",
		},
		{
			name:       "a traversal id is named verbatim and stopped by the caller",
			provenance: "CHARACTERIZATION: refusal belongs to IsCanonicalConversationID plus the os.Root anchor",
			convID:     "../../../../etc/passwd",
			want:       "../../../../etc/passwd.jsonl",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := layout.TranscriptFileName(tc.convID); got != tc.want {
				t.Fatalf("TranscriptFileName(%q) = %q, want %q\n  provenance: %s", tc.convID, got, tc.want, tc.provenance)
			}
		})
	}
}

// TestTranscriptLayout_UnsupportedProvidersDoNotImplementIt is the refusal half, and it is
// the whole reason the seam is optional rather than a method on Adapter.
//
// ABSENCE IS THE SIGNAL (ADR-010 section 5). Codex, agy and opencode have no characterized
// transcript layout in this sweep -- codex in particular needs a dated directory scan
// nobody has written yet -- so they implement NOTHING, the type assertion fails, and the
// caller refuses those providers BY NAME. A stub returning "" would look like an answer
// and would send an anchored open at a directory named "", which is a different and much
// worse failure than a named refusal.
func TestTranscriptLayout_UnsupportedProvidersDoNotImplementIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    adapter.Adapter
	}{
		{"codex", codex.New()},
		{"agy", agy.New()},
		{"opencode", opencode.New()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if layout, ok := adapter.AsTranscriptLayout(tc.a); ok {
				t.Fatalf("AsTranscriptLayout(%s) = (%v, true); this provider has no characterized "+
					"transcript layout and must be refused by name, not answered with a stub", tc.name, layout)
			}
		})
	}
}
