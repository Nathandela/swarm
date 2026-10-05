package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestAccountCoverageGuidanceIsHonestAndFitsNarrowBoard(t *testing.T) {
	a := accountsModel{reply: protocol.AccountsReply{Coverage: map[string]protocol.AccountCoverageView{
		"codex": {Managed: 2, Unmanaged: 12, RunningUnmanaged: 3},
	}}}
	text := a.coverageGuidance("codex")
	for _, want := range []string{"2 managed", "12 awaiting enrollment", "3 running"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing actual coverage %q: %q", want, text)
		}
	}
	for _, line := range strings.Split(accountWrap(text, 48), "\n") {
		if lipgloss.Width(line) > 48 {
			t.Fatalf("coverage overflows: %q", line)
		}
	}
	if got := a.coverageGuidance("claude"); !strings.Contains(got, "unavailable") {
		t.Fatalf("old daemon reply claimed coverage: %q", got)
	}
}
