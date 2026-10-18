package schema

import "testing"

func TestNativeComposerRequiresItsExactReadContract(t *testing.T) {
	valid := SessionCapabilities{Provider: "opencode", SessionInstance: "instance", NativeInteractionVersion: 2, NativeComposer: true}
	for _, mutate := range []func(*SessionCapabilities){
		func(c *SessionCapabilities) { c.Provider = "claude" },
		func(c *SessionCapabilities) { c.NativeInteractionVersion = 0 },
		func(c *SessionCapabilities) { c.NativeInteractionVersion = 3 },
		func(c *SessionCapabilities) { c.StructuredChat = true },
	} {
		bad := valid
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatalf("inconsistent native composer accepted: %+v", bad)
		}
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
}
