package claude

// bd agents-tracker-e8nn (code half), argv arm — no remote-control flag may ever
// reach a supervised argv. The env arm lives in internal/shim
// (spawn_remotecontrol_test.go); this is the other half of the same fence: a
// supervised session's approvals belong to swarm's PermissionRequest hook, so the
// adapter must not compose a `--remote-control*` token from anything an operator
// can type into the launch form.

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
)

// remoteControlFlagPrefix is the argv token family that turns on Claude Code's
// first-party Remote Control. Verbatim from `claude --help` at 2.1.224:
// `--remote-control [name]` and `--remote-control-session-name-prefix <prefix>`.
const remoteControlFlagPrefix = "--remote-control"

// TestCommand_OperatorOptionsNeverForwardARemoteControlFlag — no launch-option
// value can smuggle a remote-control flag into the composed argv. `model` is
// free-form text on the launch form (Options() declares it "string" with mere
// suggestions), and it is the only option whose VALUE becomes an argv token, so
// it is the whole vector.
func TestCommand_OperatorOptionsNeverForwardARemoteControlFlag(t *testing.T) {
	cases := []struct {
		name string
		opts map[string]string
	}{
		{"no options", nil},
		{"honest model", map[string]string{"model": "opus"}},
		{"model smuggles the flag", map[string]string{"model": remoteControlFlagPrefix}},
		{"model smuggles the prefix flag", map[string]string{"model": "--remote-control-session-name-prefix"}},
		{"model smuggles the flag with a name", map[string]string{"model": "--remote-control=SWARM"}},
		{"unknown option key is not a flag source", map[string]string{"remote-control": "true"}},
		{"skip-permissions still composes its own literal", map[string]string{
			"dangerously-skip-permissions": "true",
			"model":                        "--remote-control",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv, err := New().Command(adapter.LaunchSpec{Options: tc.opts})
			if err != nil {
				t.Fatalf("Command: %v", err)
			}
			for i, tok := range argv {
				if strings.HasPrefix(tok, remoteControlFlagPrefix) {
					t.Errorf("argv[%d] = %q — an operator option forwarded a remote-control flag into a supervised argv (e8nn)\nargv: %q", i, tok, argv)
				}
			}
		})
	}
}

// TestOptions_DeclareNoRemoteControlSwitch — the declared option schema itself
// offers no remote-control switch, so the launch form can never render one. A
// drift fence: adding such an option is the change this test exists to catch.
func TestOptions_DeclareNoRemoteControlSwitch(t *testing.T) {
	for _, o := range New().Options() {
		if strings.Contains(strings.ToLower(o.Key), "remote-control") {
			t.Errorf("Options() declares %q — swarm must not offer Remote Control on a supervised session (e8nn)", o.Key)
		}
		for _, s := range append(append([]string(nil), o.Suggest...), append(o.Choices, o.Default)...) {
			if strings.HasPrefix(s, remoteControlFlagPrefix) {
				t.Errorf("Options() key %q suggests/defaults %q — a remote-control flag must never be offered as a value (e8nn)", o.Key, s)
			}
		}
	}
}
