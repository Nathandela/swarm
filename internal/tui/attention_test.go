package tui

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func TestAttentionLabelsAndSameGroupBanners(t *testing.T) {
	m := newGeneralModel(nil)
	m.width = 120
	for _, tc := range []struct {
		interaction status.Interaction
		label       string
	}{
		{status.InteractionPermission, "approval"}, {status.InteractionPrompt, "question"}, {status.InteractionError, "error"}, {status.InteractionUnknown, "needs input"},
	} {
		s := protocol.SessionView{ID: "endpoint/task", Agent: "codex", Group: status.GroupNeedsInput, Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: tc.interaction}}
		if cmd := m.apply(s); cmd == nil {
			t.Fatalf("no attention banner for %s", tc.label)
		}
		if !strings.Contains(stripANSI(m.renderRow(s, s.Group, true)), tc.label) || !strings.Contains(m.bannerText, tc.label) {
			t.Fatalf("attention %s not exposed on row and banner", tc.label)
		}
	}
}
