package gate

import (
	"regexp"
	"strings"
	"testing"
)

// The handset has one session UI: the structured conversation. A capability can shut its
// composer, but it must not route the reader into the terminal snapshot surface. This is a source
// fence because the gomobile-backed Ready branch cannot run under Robolectric.
func TestAlwaysChatShell_EveryOpenSessionUsesTheConversationDetail(t *testing.T) {
	const surface = "dev/swarm/phone/PhoneSurface.kt"
	code := kotlinCodeOnly(allProductionKotlin(t)[surface])
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
	code := kotlinCodeOnly(allProductionKotlin(t)[bridge])
	if strings.Contains(code, "fun terminalFallback(") {
		t.Errorf("%s still exports terminalFallback as an Android navigation decision. The daemon's "+
			"destination may remain on the compatibility wire, but every handset session uses the "+
			"conversation shell and capability only changes its composer state.", bridge)
	}
}

// The superseded Android destination has no production home. Keep the fence broad enough to
// catch a renamed drawer, binding, model, view, or watch lane anywhere in production Kotlin.
func TestAlwaysChatShell_NoRetiredTerminalFallbackOrWatch(t *testing.T) {
	fallback := regexp.MustCompile(`\bTerminalFallback(?:View|Screen|Model|Binding)\b|\bterminalFallback(?:View|Screen|Model|Binding)\b`)
	watch := regexp.MustCompile(`\b(?:TerminalWatchLane|TerminalWatchHandle)\b|\b[A-Za-z_]*[Tt]erminal[A-Za-z_]*[Ww]atch\s*\(`)
	terminalWell := regexp.MustCompile(`monoWell\s*\([^)]*terminal\s*=\s*true`)
	for name, src := range allProductionKotlin(t) {
		code := kotlinCodeOnly(src)
		if fallback.MatchString(code) {
			t.Errorf("%s retains a retired Android terminal-fallback production symbol", name)
		}
		if watch.MatchString(code) {
			t.Errorf("%s retains a production terminal-watch lane or call", name)
		}
		if terminalWell.MatchString(code) {
			t.Errorf("%s retains the retired terminal variant of monoWell", name)
		}
	}
}

// Composer availability is a live capability contract and survives the removed destination.
func TestAlwaysChatShell_ComposerAvailabilityComesFromCapabilities(t *testing.T) {
	const panel = "dev/swarm/phone/ui/screens/SessionDetailPanel.kt"
	src, ok := allProductionKotlin(t)[panel]
	if !ok {
		t.Fatalf("%s does not exist", panel)
	}
	code := kotlinCodeOnly(src)
	if regexp.MustCompile(`structuredChat\s*=\s*!\s*transcript\.structureTorn`).MatchString(code) {
		t.Errorf("%s derives composer availability from transcript shape", panel)
	}
	if !regexp.MustCompile(`\b(capabilities|structuredChatCapability|capabilityRecord)\b`).MatchString(code) {
		t.Errorf("%s reads no capability record for composer availability", panel)
	}
}
