package gate

import (
	"strings"
	"testing"
)

// The handset has one session UI: the structured conversation. A capability can shut its
// composer, but it must not route the reader into the terminal snapshot surface. This is a source
// fence because the gomobile-backed Ready branch cannot run under Robolectric.
func TestAlwaysChatShell_EveryOpenSessionUsesTheConversationDetail(t *testing.T) {
	const surface = "dev/swarm/phone/PhoneSurface.kt"
	code := kotlinCodeOnly(r8AllProductionKotlin(t)[surface])
	if code == "" {
		t.Fatalf("%s does not exist", surface)
	}
	draw := kotlinMember(t, code, "private fun drawContent(")
	for _, forbidden := range []string{
		"terminalFallback(",
		"drawTerminalFallback(",
		"reconcileTerminalWatch(",
	} {
		if strings.Contains(draw, forbidden) {
			t.Errorf("%s routes an open session through %s. Capability loss must retain the normal "+
				"detail/transcript/composer shell and only disable the composer inline.", surface, forbidden)
		}
	}
	for _, required := range []string{"detailPanel(", "drawDetail("} {
		if !strings.Contains(draw, required) {
			t.Errorf("%s has no %s on the Inbox drill-down path; the always-chat shell has no "+
				"rendering route", surface, required)
		}
	}
}

func TestAlwaysChatShell_NoAndroidRouterCanSelectATerminalReplacement(t *testing.T) {
	const bridge = "dev/swarm/phone/ui/FacadeBridge.kt"
	code := kotlinCodeOnly(r8AllProductionKotlin(t)[bridge])
	if strings.Contains(code, "fun terminalFallback(") {
		t.Errorf("%s still exports terminalFallback as an Android navigation decision. The daemon's "+
			"destination may remain on the compatibility wire, but every handset session uses the "+
			"conversation shell and capability only changes its composer state.", bridge)
	}
}
