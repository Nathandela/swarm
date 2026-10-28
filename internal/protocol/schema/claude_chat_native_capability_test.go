package schema

import "testing"

func TestClaudeNativeChatCapabilityExactV2NeverGrantsTerminal(t *testing.T) {
	base := SessionCapabilities{Provider: "claude", ProviderVersion: "2.1.310", SessionInstance: "native-instance", NativeInteractionVersion: 2, NativeComposer: true}
	if e := base.Validate(); e != nil {
		t.Fatalf("exact Claude native v2 composer refused: %v", e)
	}
	if base.AllowsTerminalWatch() || base.AllowsTerminalControl() || base.Interrupt {
		t.Fatal("native composer gained terminal authority")
	}
	for _, kind := range []string{"v1", "missing-version", "wrong-version", "fallback", "control", "interrupt", "generic-chat", "missing-instance"} {
		t.Run(kind, func(t *testing.T) {
			c := base
			switch kind {
			case "v1":
				c.NativeInteractionVersion = 1
			case "missing-version":
				c.ProviderVersion = ""
			case "wrong-version":
				c.ProviderVersion = "2.1.309"
			case "fallback":
				c.TerminalFallback = true
			case "control":
				c.TerminalControl = true
			case "interrupt":
				c.Interrupt = true
			case "generic-chat":
				c.StructuredChat = true
			case "missing-instance":
				c.SessionInstance = ""
			}
			if c.Validate() == nil {
				t.Fatalf("invalid native capability accepted: %s", kind)
			}
		})
	}
	fixture := SessionCapabilities{Provider: "fake", SessionInstance: "fixture", NativeInteractionVersion: 1, NativeComposer: true}
	ordinary := SessionCapabilities{Provider: "claude", ProviderVersion: "2.1.310", SessionInstance: "ordinary", TerminalFallback: true, TerminalControl: true, Interrupt: true}
	openCode := SessionCapabilities{Provider: "opencode", SessionInstance: "opencode", NativeInteractionVersion: 2, NativeComposer: true}
	for _, c := range []SessionCapabilities{fixture, ordinary, openCode} {
		if e := c.Validate(); e != nil {
			t.Fatalf("existing capability changed: %+v %v", c, e)
		}
	}
}
