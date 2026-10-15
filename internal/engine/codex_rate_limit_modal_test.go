package engine

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

// codexRateLimitModalScreen: Codex's "Approaching rate limits" model-switch modal.
// Reconstructed from the R2 field report (RC5). The exact footer wording is not
// yet confirmed against a live capture, so the footer follows the "press enter to
// select" hint family named in the report.
func codexRateLimitModalScreen() *vt.Snap {
	return snapFromLines(80, 0, 0, false, []string{
		"  Approaching rate limits",
		"  Switch to gpt-5.1-codex-mini for lower credit usage?",
		"",
		"› 1. Switch to gpt-5.1-codex-mini",
		"  2. Keep current model",
		"  3. Keep current model (never show again)",
		"",
		"  Press enter to select or esc to go back",
	})
}

// codexOutOfMessagesModalScreen: the live Codex rate-limit modal captured from a
// running session (read-only swarm peek). The composer row stays rendered below
// the modal, and the choice rows are numbered with a "›" selection marker.
func codexOutOfMessagesModalScreen() *vt.Snap {
	return snapFromLines(80, 0, 0, false, []string{
		"⚠ Heads up, you have less than 5% of your weekly limit left. Run /status for a breakdown.",
		"",
		"  You're out of Codex messages",
		"  Your rate limit resets on 06:20 on 22 Oct. Use one of your rate limit resets or add credits to continue now.",
		"",
		"› 1. Add Credits",
		"  2. Reset usage",
		"",
		"  Press a number to choose",
		"",
		"",
		"› Ask Codex to do anything",
	})
}

// codexOutOfMessagesProseScreen: an idle Codex composer whose transcript quotes the
// modal title inside a sentence, with no choice rows and no footer.
func codexOutOfMessagesProseScreen() *vt.Snap {
	return snapFromLines(80, 0, 0, false, []string{
		"• The log said \"You're out of Codex messages\" during the last run, so I retried.",
		"",
		"› Ask Codex to do anything",
	})
}

// TestOnOutput_CodexOutOfMessagesModalMapsToNeedsInput: the captured modal must be
// read as an interaction prompt that derives to needs_input, even though the
// composer row is still rendered below it.
func TestOnOutput_CodexOutOfMessagesModalMapsToNeedsInput(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
	e.RegisterSession("s1", "tok1", 1, gridSig("codex"))

	e.OnOutput("s1", codexOutOfMessagesModalScreen())
	got, ok := rec.last()
	if !ok {
		t.Fatal("out-of-messages modal emitted no status change from the unknown seed")
	}
	if got.s.Turn != status.TurnIdle || got.s.Interaction != status.InteractionPrompt {
		t.Fatalf("out-of-messages modal -> turn=%s interaction=%s, want idle+prompt", got.s.Turn, got.s.Interaction)
	}
	if g := status.Derive(got.s); g != status.GroupNeedsInput {
		t.Fatalf("out-of-messages modal derives group %s, want needs_input", g)
	}
}

// TestCodexOutOfMessagesProseIsNotAPrompt: the title quoted in prose, over a bare
// composer, must not be read as the modal.
func TestCodexOutOfMessagesProseIsNotAPrompt(t *testing.T) {
	_, inter, _ := evaluateCodexGrid(codexOutOfMessagesProseScreen())
	if inter == status.InteractionPrompt {
		t.Fatal("prose quoting the out-of-messages title misread as an interaction prompt")
	}
}

// codexRateLimitProseScreen: an idle Codex composer whose transcript quotes the
// rate-limit wording in ordinary prose. It must not read as a human-input prompt.
func codexRateLimitProseScreen() *vt.Snap {
	return snapFromLines(80, 0, 0, false, []string{
		"• I checked the rate limits for the API and they look fine.",
		"  Approaching rate limits is a warning we log at 80 percent of quota.",
		"",
		"› Ask Codex to do anything",
	})
}

// TestOnOutput_CodexRateLimitModalMapsToNeedsInput (RC5): the model-switch modal
// has no typed signal, so its standing frame must be readable from the grid as an
// interaction prompt that derives to needs_input.
func TestOnOutput_CodexRateLimitModalMapsToNeedsInput(t *testing.T) {
	clk := newClock()
	rec := &emitRecorder{}
	e := newEngine(clk, constCPU(0), rec, time.Minute, time.Second)
	e.RegisterSession("s1", "tok1", 1, gridSig("codex"))

	e.OnOutput("s1", codexRateLimitModalScreen())
	got, ok := rec.last()
	if !ok {
		t.Fatal("rate-limit modal emitted no status change from the unknown seed")
	}
	if got.s.Turn != status.TurnIdle || got.s.Interaction != status.InteractionPrompt {
		t.Fatalf("rate-limit modal -> turn=%s interaction=%s, want idle+prompt", got.s.Turn, got.s.Interaction)
	}
	if g := status.Derive(got.s); g != status.GroupNeedsInput {
		t.Fatalf("rate-limit modal derives group %s, want needs_input", g)
	}
}

// TestCodexRateLimitProseIsNotAPrompt: ordinary output that mentions rate limits
// must not be read as the model-switch modal.
func TestCodexRateLimitProseIsNotAPrompt(t *testing.T) {
	_, inter, _ := evaluateCodexGrid(codexRateLimitProseScreen())
	if inter == status.InteractionPrompt {
		t.Fatal("ordinary prose mentioning rate limits misread as an interaction prompt")
	}
}
