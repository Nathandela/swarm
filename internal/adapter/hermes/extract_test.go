package hermes

import (
	"testing"

	"github.com/Nathandela/swarm/internal/vt"
)

const secondConversationID = "20260829_103824_6329fe"

func TestExtractConversationIDFromCharacterizedBanner(t *testing.T) {
	tail := []byte("model prose says Session: 20260829_000000_deadbe\r\n" +
		"│      swarm-test · Nous Research       available tools                       │\r\n" +
		"│  /work/project                        creative skills                       │\r\n" +
		"│    Session: " + fixtureConversationID + "    more                                   │\r\n")
	got, ok := newAdapter().ExtractConversationID(nil, tail)
	if !ok || got != fixtureConversationID {
		t.Fatalf("ExtractConversationID(banner) = (%q,%v); want (%q,true)", got, ok, fixtureConversationID)
	}
}

func TestExtractConversationIDCurrentExitBlockPrecedesStartupBanner(t *testing.T) {
	tail := []byte("│ Session: " + fixtureConversationID + " │\n" +
		"Resume this session with:\r\n" +
		"  hermes --resume " + secondConversationID + "\r\n" +
		exitSummary(secondConversationID))
	got, ok := newAdapter().ExtractConversationID(nil, tail)
	if !ok || got != secondConversationID {
		t.Fatalf("ExtractConversationID = (%q,%v); want current exit id %q", got, ok, secondConversationID)
	}
}

func TestExtractConversationIDExitFallbackUsesLastCommand(t *testing.T) {
	tail := []byte("Resume this session with:\r\n" +
		"  hermes --resume " + secondConversationID + "\r\n" +
		exitSummary(secondConversationID) +
		"assistant chatter\r\n" +
		"Resume this session with:\r\n" +
		"  hermes --resume " + fixtureConversationID + "\x1b[0m\r\n" +
		exitSummary(fixtureConversationID))
	got, ok := newAdapter().ExtractConversationID(nil, tail)
	if !ok || got != fixtureConversationID {
		t.Fatalf("ExtractConversationID(exit) = (%q,%v); want last command id %q", got, ok, fixtureConversationID)
	}
}

func TestExtractConversationIDGridFallback(t *testing.T) {
	grid := snapWithLines(
		"Hermes Agent",
		"│      swarm-test · Nous Research       tools │",
		"│  /work/project                        skills │",
		"│    Session: "+fixtureConversationID+"    tools │",
	)
	got, ok := newAdapter().ExtractConversationID(grid, []byte("raw tail has no identity\n"))
	if !ok || got != fixtureConversationID {
		t.Fatalf("ExtractConversationID(grid) = (%q,%v); want (%q,true)", got, ok, fixtureConversationID)
	}
}

func TestExtractConversationIDAcceptsSafeTerminators(t *testing.T) {
	for name, term := range map[string]string{
		"newline": "\n",
		"space":   " ",
		"ansi":    "\x1b[0m",
		"c1":      "\xc2\x9b0m",
		"border":  "│",
	} {
		t.Run(name, func(t *testing.T) {
			tail := []byte("Resume this session with:\n  hermes --resume " + fixtureConversationID + term +
				exitSummary(fixtureConversationID))
			got, ok := newAdapter().ExtractConversationID(nil, tail)
			if !ok || got != fixtureConversationID {
				t.Fatalf("ExtractConversationID = (%q,%v); want (%q,true)", got, ok, fixtureConversationID)
			}
		})
	}
}

func TestExtractConversationIDRejectsCompleteThreeLineModelSpoof(t *testing.T) {
	tail := []byte("assistant output quotes terminal chrome verbatim:\n" +
		"Resume this session with:\n" +
		"  hermes --resume " + fixtureConversationID + "\n" +
		"Session:        " + fixtureConversationID + "\n" +
		"That was only an example, not a graceful exit.\n")
	if got, ok := newAdapter().ExtractConversationID(nil, tail); ok || got != "" {
		t.Fatalf("ExtractConversationID(complete three-line spoof) = (%q,%v); want no identity without Duration and Messages summary fields", got, ok)
	}
}

func TestGracefulExitSummaryGrammarMatchesUpstream(t *testing.T) {
	for _, duration := range []string{"0s", "59s", "1m 0s", "59m 59s", "1h 0m 0s", "123h 59m 59s"} {
		if !validExitDuration(duration) {
			t.Errorf("validExitDuration(%q) = false; want true", duration)
		}
	}
	for _, duration := range []string{"", "7", "60s", "01s", "0m 7s", "1m 60s", "1h 60m 0s", "1h 0s", "1d 0h 0m 0s"} {
		if validExitDuration(duration) {
			t.Errorf("validExitDuration(%q) = true; want false", duration)
		}
	}

	for _, messages := range []string{"0 (0 user, 0 tool calls)", "2 (1 user, 0 tool calls)", "123 (12 user, 99 tool calls)"} {
		if !validExitMessages(messages) {
			t.Errorf("validExitMessages(%q) = false; want true", messages)
		}
	}
	for _, messages := range []string{"", "2", "02 (1 user, 0 tool calls)", "2 (01 user, 0 tool calls)", "2 (1 users, 0 tool calls)", "2 (1 user, 0 tool call)", "2 (1 user, 0 tool calls) extra"} {
		if validExitMessages(messages) {
			t.Errorf("validExitMessages(%q) = true; want false", messages)
		}
	}
}

func TestExtractConversationIDRejectsMalformedGracefulExitMetrics(t *testing.T) {
	for name, metrics := range map[string]string{
		"missing messages":   "Duration:       7s\n",
		"malformed duration": "Duration:       seven seconds\nMessages:       2 (1 user, 0 tool calls)\n",
		"malformed messages": "Duration:       7s\nMessages:       two messages\n",
		"reordered fields":   "Messages:       2 (1 user, 0 tool calls)\nDuration:       7s\n",
	} {
		t.Run(name, func(t *testing.T) {
			tail := []byte("Resume this session with:\n" +
				"  hermes --resume " + fixtureConversationID + "\n" +
				"\nSession:        " + fixtureConversationID + "\n" + metrics)
			if got, ok := newAdapter().ExtractConversationID(nil, tail); ok || got != "" {
				t.Fatalf("ExtractConversationID(malformed metrics) = (%q,%v); want no identity", got, ok)
			}
		})
	}
}

func TestExtractConversationIDRejectsQuotedExitBlockWithoutSummary(t *testing.T) {
	tail := []byte("assistant output follows:\n" +
		"Resume this session with:\n" +
		"  hermes --resume " + fixtureConversationID + "\n" +
		"That was only an example.\n")
	if got, ok := newAdapter().ExtractConversationID(nil, tail); ok || got != "" {
		t.Fatalf("ExtractConversationID(quoted block) = (%q,%v); want no identity without corroborating final summary", got, ok)
	}
}

func TestExtractConversationIDRejectsExitBlockWithMismatchedSummary(t *testing.T) {
	tail := []byte("│ swarm-test · Nous Research │\n" +
		"│ /work/project                 │\n" +
		"│ Session: " + fixtureConversationID + " │\n" +
		"Resume this session with:\n" +
		"  hermes --resume " + secondConversationID + "\n" +
		exitSummary(fixtureConversationID))
	got, ok := newAdapter().ExtractConversationID(nil, tail)
	if !ok || got != fixtureConversationID {
		t.Fatalf("ExtractConversationID(mismatched exit) = (%q,%v); want bordered startup fallback %q", got, ok, fixtureConversationID)
	}
}

func TestExtractConversationIDRejectsTruncationMalformedIDsAndSpoofs(t *testing.T) {
	banner := "│ swarm-test · Nous Research │\n│ /work/project                 │\n"
	tests := map[string]string{
		"id at eof":                            banner + "│ Session: " + fixtureConversationID,
		"short suffix":                         banner + "│ Session: 20260829_103232_1a7c2\n",
		"gateway eight hex suffix":             banner + "│ Session: 20260829_103232_1a7c23ff\n",
		"uppercase suffix":                     banner + "│ Session: 20260829_103232_1A7C23\n",
		"non hex suffix":                       banner + "│ Session: 20260829_103232_1a7c2g\n",
		"wrong separators":                     banner + "│ Session: 20260829-103232-1a7c23\n",
		"impossible timestamp":                 banner + "│ Session: 20261340_296199_1a7c23\n",
		"alnum token extension":                banner + "│ Session: " + fixtureConversationID + "z\n",
		"plain prose session":                  "please mention Session: " + fixtureConversationID + " in the answer\n",
		"line-start prose session":             "Session: " + fixtureConversationID + " is an example, not terminal chrome\n",
		"inline resume prose":                  "the assistant says run hermes --resume " + fixtureConversationID + "\n",
		"bare resume command":                  "  hermes --resume " + fixtureConversationID + "\n",
		"bare id":                              fixtureConversationID + "\n",
		"bordered line without banner context": "│ Session: " + fixtureConversationID + " │\n",
	}
	for name, tail := range tests {
		t.Run(name, func(t *testing.T) {
			if got, ok := newAdapter().ExtractConversationID(nil, []byte(tail)); ok || got != "" {
				t.Fatalf("ExtractConversationID(%q) = (%q,%v); want no identity", tail, got, ok)
			}
		})
	}
}

func TestExtractConversationIDAmbiguousBannerFallsBackToExitCommand(t *testing.T) {
	tail := []byte("│ swarm-test · Nous Research │\n" +
		"│ /work/project                 │\n" +
		"│ Session: " + secondConversationID + " │\n" +
		"│ Session: " + fixtureConversationID + " │\n" +
		"Resume this session with:\n" +
		"  hermes --resume " + fixtureConversationID + "\n" +
		exitSummary(fixtureConversationID))
	got, ok := newAdapter().ExtractConversationID(nil, tail)
	if !ok || got != fixtureConversationID {
		t.Fatalf("ExtractConversationID = (%q,%v); want unambiguous exit id %q", got, ok, fixtureConversationID)
	}
}

func exitSummary(id string) string {
	return "\r\nSession:        " + id + "\r\n" +
		"Title:          fixture title\r\n" +
		"Duration:       7s\r\n" +
		"Messages:       2 (1 user, 0 tool calls)\r\n"
}

func TestExtractConversationIDRepeatedBannerIsNotAmbiguous(t *testing.T) {
	tail := []byte("│ swarm-test · Nous Research │\n" +
		"│ /work/project                 │\n" +
		"│ Session: " + fixtureConversationID + " │\n" +
		"│ Session: " + fixtureConversationID + " │\n")
	got, ok := newAdapter().ExtractConversationID(nil, tail)
	if !ok || got != fixtureConversationID {
		t.Fatalf("ExtractConversationID = (%q,%v); want repeated id %q", got, ok, fixtureConversationID)
	}
}

func TestExtractConversationIDAcceptsUnconfiguredBannerContext(t *testing.T) {
	tail := []byte("│ no model configured — run /model or hermes setup │\n" +
		"│ /work/project                                  │\n" +
		"│ Session: " + fixtureConversationID + "                         │\n")
	got, ok := newAdapter().ExtractConversationID(nil, tail)
	if !ok || got != fixtureConversationID {
		t.Fatalf("ExtractConversationID(unconfigured banner) = (%q,%v); want (%q,true)", got, ok, fixtureConversationID)
	}
}

func snapWithLines(lines ...string) *vt.Snap {
	snap := &vt.Snap{Version: 1, Rows: len(lines), Lines: make([]vt.Line, len(lines))}
	for i, line := range lines {
		snap.Lines[i] = vt.Line{Runs: []vt.Run{{Text: line, Width: len([]rune(line))}}}
	}
	return snap
}
