package tui

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/charmbracelet/x/ansi"
)

func TestLogins_HandoverUsesExistingProviderStatusRow(t *testing.T) {
	m := loginsModel{available: true, loaded: true, reply: protocol.LoginsReply{Providers: []protocol.ProviderLoginView{
		{Provider: "claude", State: "native"},
		{Provider: "codex", State: "adopting", Message: "1/3 sessions switched"},
	}}}
	text := strings.Join(strings.Fields(ansi.Strip(m.view(80, 20, false, false))), " ")
	if !strings.Contains(text, "Codex Switching") || !strings.Contains(text, "1/3 sessions switched") {
		t.Fatalf("partial handover missing from provider row: %s", text)
	}
}
