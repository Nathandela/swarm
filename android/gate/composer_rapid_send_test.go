package gate

import (
	"strings"
	"testing"
)

// Composer sends are messages, not idempotent control presses. The command lane supplies FIFO;
// VerbDispatch.press's view-keyed fence would instead disable Send and discard the second draft.
func TestComposerRapidSend_ProductionUsesTheUnfencedFIFOCommandLane(t *testing.T) {
	const surface = "dev/swarm/phone/PhoneSurface.kt"
	code := kotlinCodeOnly(r8AllProductionKotlin(t)[surface])
	send := code[strings.Index(code, "private val send:"):strings.Index(code, "private fun stopping(")]
	if !strings.Contains(send, "delivery = PressDelivery.FIFO") {
		t.Fatalf("composer Send still uses the generic single-flight press delivery; a held facade call disables/drops the next Send")
	}
	dispatch := kotlinMember(t, code, "private fun dispatchPress(")
	if !strings.Contains(dispatch, "PressDelivery.FIFO") || !strings.Contains(dispatch, "dispatch.enqueue(") {
		t.Fatalf("composer FIFO delivery is not routed through the serial unfenced command enqueue")
	}
	if strings.Contains(send, "typed.isEnabled = false") || strings.Contains(send, "send.isEnabled = false") {
		t.Fatalf("composer disables its editor or Send while an operation is pending")
	}
	for _, want := range []string{
		"val target = session",
		"val line = typed.text.toString()",
		"val turn = detailDrawn?.expectedTurn.orEmpty()",
		"rememberComposerSend(answer, target, turn, line)",
	} {
		if !strings.Contains(send, want) {
			t.Errorf("composer does not capture one press's independent draft through %q", want)
		}
	}
	remember := kotlinMember(t, code, "private fun rememberComposerSend(")
	if !strings.Contains(remember, "typed.text.toString() == sentText") || !strings.Contains(remember, "typed.text.clear()") {
		t.Errorf("composer does not clear only the exact draft whose local seal settled")
	}
}

// input_busy is the daemon's explicit proof that the operation wrote no bytes. It is the only
// outcome that may replace an operation id under the same logical bubble automatically.
func TestComposerRapidSend_InputBusyRetriesTheLogicalBubbleWithFreshOperationOwnership(t *testing.T) {
	all := r8AllProductionKotlin(t)
	surface := kotlinCodeOnly(all["dev/swarm/phone/PhoneSurface.kt"])
	verdict := kotlinMember(t, surface, "private fun renderComposerVerdict(")
	for _, want := range []string{"verdict.retryable", "beginRetry(", "composerRetry.schedule(", "retryReady("} {
		if !strings.Contains(verdict, want) {
			t.Errorf("composer outcome path does not contain %q, so input_busy becomes a final failure", want)
		}
	}
	retry := kotlinMember(t, surface, "private fun retryComposerSend(")
	for _, want := range []string{
		"app.composerRetry(",
		"previousOperationId",
		"retrySealed(",
		"val accepted = dispatch.enqueueCompleting(",
		"complete =",
		"routeFacadeErrorSafely(",
		"settle = { render() }",
		"return accepted",
	} {
		if !strings.Contains(retry, want) {
			t.Errorf("composer retry path does not contain %q", want)
		}
	}
	if strings.Contains(retry, "app.composerSend(") {
		t.Error("input_busy retry calls ComposerSend, creating a second LogicalID instead of replacing the exact prior operation")
	}

	ledger := kotlinCodeOnly(all["dev/swarm/phone/ui/screens/ComposerSendLedger.kt"])
	for _, want := range []string{"fun beginRetry(", "fun retrySealed(", "fun retryRejected(", "expectedTurn", "retryAttempt"} {
		if !strings.Contains(ledger, want) {
			t.Errorf("composer ledger does not contain %q; retry cannot preserve one logical bubble", want)
		}
	}
}

func TestComposerRapidSend_RenderHydratesDurableOperationsBeforeClaimingOutcomes(t *testing.T) {
	all := r8AllProductionKotlin(t)
	surface := kotlinMember(t, kotlinCodeOnly(all["dev/swarm/phone/PhoneSurface.kt"]), "private fun renderReady(")
	hydrate := strings.Index(surface, "composerSends.hydrate(bridge.composerPublications())")
	verdicts := strings.Index(surface, "renderVerdicts(bridge, startup.app)")
	if hydrate < 0 || verdicts < 0 || hydrate > verdicts {
		t.Fatalf("renderReady does not hydrate durable composer operations before claiming their outcomes")
	}
	bridge := kotlinMember(t, kotlinCodeOnly(all["dev/swarm/phone/ui/FacadeBridge.kt"]), "fun composerPublications(")
	for _, want := range []string{
		"app.composerPublications()", "publication.getLogicalID()", "publication.getOperationID()",
		"publication.getSessionID()", "publication.getText()", "publication.getPhase()",
		"publication.getTerminalCode()",
	} {
		if !strings.Contains(bridge, want) {
			t.Errorf("durable composer facade mapping omits %q", want)
		}
	}
}

func TestComposerRapidSend_ReleaseCancelsEveryDelayedRetryGeneration(t *testing.T) {
	const surface = "dev/swarm/phone/PhoneSurface.kt"
	code := kotlinCodeOnly(r8AllProductionKotlin(t)[surface])
	release := kotlinMember(t, code, "fun release(")
	if !strings.Contains(release, "composerRetry.cancel()") {
		t.Fatalf("PhoneSurface.release does not cancel delayed composer retries; an old Activity can enqueue and render after replacement")
	}
	if !strings.Contains(release, "composerSends.rearmScheduledRetries()") {
		t.Fatalf("PhoneSurface.release cancels the clock but leaves its ledger records retrying forever on same-surface resume")
	}
}
