package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestAccountsLayoutFitsBoardAndSeparatesFooter(t *testing.T) {
	for _, size := range [][2]int{{48, 14}, {80, 24}, {120, 32}, {25, 7}} {
		for _, skew := range []bool{false, true} {
			m := accountUIOpen(t, newAccountUIClient()).(rootModel)
			m.width, m.height = size[0], size[1]
			m.clientVersion, m.daemonVersion = "0.15.1", "0.15.1"
			if skew {
				m.daemonVersion = "0.15.0"
			}
			m.accounts.wizardOpen = true
			m.accounts.wizard = accountWizard{
				step: accountStepAuthenticate, provider: "codex", method: "device-code",
				job: protocol.AccountEnrollmentView{
					ID: "synthetic-layout-job", State: "authenticating",
					VerificationURL: "https://example.invalid/device/" + strings.Repeat("long-path/", 10),
					UserCode:        "SYNTHETIC-CODE",
				},
			}
			lines := strings.Split(view(m), "\n")
			if len(lines) != m.height {
				t.Fatalf("%dx%d skew=%v: got %d rows", m.width, m.height, skew, len(lines))
			}
			for _, line := range lines {
				if lipgloss.Width(line) > m.width {
					t.Fatalf("%dx%d skew=%v: row overflow: %q", m.width, m.height, skew, line)
				}
			}
			if !strings.Contains(lines[len(lines)-1], "esc") {
				t.Fatalf("%dx%d: exit action missing from footer", m.width, m.height)
			}
			if m.width >= 48 {
				if !strings.Contains(lines[len(lines)-1], "ctrl+x") {
					t.Fatalf("%dx%d: cancel action clipped from footer", m.width, m.height)
				}
				for _, line := range lines[:len(lines)-1] {
					if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
						t.Fatalf("%dx%d: content has no left margin: %q", m.width, m.height, line)
					}
				}
			}
		}
	}
}
