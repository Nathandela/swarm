package gate

import (
	"regexp"
	"strings"
	"testing"
)

func TestComposerSendLedger_SurfaceDoesNotUseASingleOverwritableOperation(t *testing.T) {
	const surface = "dev/swarm/phone/PhoneSurface.kt"
	code := kotlinCodeOnly(r8AllProductionKotlin(t)[surface])
	for _, scalar := range []string{
		"composerOp", "composerSendFor", "composerSentText", "composerSendState",
		"composerRefusal", "composerRefusalDetail",
	} {
		if regexp.MustCompile(`private\s+var\s+` + scalar + `\b`).MatchString(code) {
			t.Errorf("%s stores composer sends in singleton %s; a second send overwrites the first "+
				"operation before its outcome or transcript echo can settle", surface, scalar)
		}
	}
	for _, required := range []string{"ComposerSendLedger", "unansweredOperations()", "pendingFor("} {
		if !strings.Contains(code, required) {
			t.Errorf("%s does not use %s for ordered, per-operation sends", surface, required)
		}
	}
}

func TestComposerSendLedger_LocalSealClearsOnlyTheDraftThatWasSent(t *testing.T) {
	const surface = "dev/swarm/phone/PhoneSurface.kt"
	code := kotlinCodeOnly(r8AllProductionKotlin(t)[surface])
	remember := kotlinMember(t, code, "private fun rememberComposerSend(")
	if !strings.Contains(remember, "typed.text") || !strings.Contains(remember, ".clear()") {
		t.Errorf("rememberComposerSend does not clear the draft when the local command is sealed, " +
			"so the reader cannot type a second message while the first waits for its machine outcome")
	}
	if !strings.Contains(remember, "sentText") {
		t.Errorf("rememberComposerSend does not compare/carry the text captured at the press; an " +
			"unconditional clear can erase words typed while the command was sealing")
	}
	verdict := kotlinMember(t, code, "private fun renderComposerVerdict(")
	if strings.Contains(verdict, "typed.text.clear()") {
		t.Errorf("renderComposerVerdict still waits for the machine outcome to clear the draft; " +
			"multiple ordered sends cannot be composed while the first outcome is pending")
	}
}

func TestComposerSendLedger_EachRefusalStaysOnItsOwnBubble(t *testing.T) {
	all := r8AllProductionKotlin(t)
	ledger := kotlinCodeOnly(all["dev/swarm/phone/ui/screens/ComposerSendLedger.kt"])
	for _, required := range []string{
		"notice = verdict.notice",
		"detail = verdict.detail",
		"notice = it.notice",
		"detail = it.detail",
	} {
		if !strings.Contains(ledger, required) {
			t.Errorf("ComposerSendLedger does not carry %q through the operation's own PendingSend", required)
		}
	}

	panel := kotlinCodeOnly(all["dev/swarm/phone/ui/screens/TranscriptPanel.kt"])
	for _, required := range []string{
		"sendNotice = pendingSend.notice",
		"sendNoticeDetail = pendingSend.detail",
	} {
		if !strings.Contains(panel, required) {
			t.Errorf("TranscriptPanel does not attach one send's refusal through %q", required)
		}
	}

	view := kotlinCodeOnly(all["dev/swarm/phone/ui/screens/TranscriptView.kt"])
	for _, required := range []string{
		"notice(context, block.sendNotice, NoticeKind.ERROR)",
		"noticeDetail(context, block.sendNoticeDetail)",
	} {
		if !strings.Contains(view, required) {
			t.Errorf("TranscriptView does not render one send's refusal through %q", required)
		}
	}

	surface := kotlinCodeOnly(all["dev/swarm/phone/PhoneSurface.kt"])
	detail := kotlinMember(t, surface, "private fun detailPanel(")
	if strings.Contains(detail, "latestSend?.refusal") || strings.Contains(detail, "latestSend?.detail") {
		t.Errorf("detailPanel resurrects a sealed send's refusal in the global composer notice; " +
			"the refusal belongs to that operation's bubble")
	}
}
