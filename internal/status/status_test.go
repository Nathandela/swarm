package status

import "testing"

// Spec source: docs/specifications/system-spec.md, "Session status model"
// (system-spec.md:41-56).
//
// Three orthogonal dimensions (system-spec.md:45-47):
//
//	process:     running | exited | lost
//	turn:        active | idle | unknown
//	interaction: none | prompt | permission | unknown
//
// Amendment: the spec (system-spec.md:47) lists a fourth interaction value,
// `unknown` -- the spec's prose wins over the originally frozen 3-value API,
// so InteractionUnknown = "unknown" is now part of the API. This gives 3
// (process) x 3 (turn) x 4 (interaction) = 36 combinations.
//
// Derivation table (system-spec.md:51-56), reproduced verbatim:
//
//	| Group             | Rule                                                |
//	| Needs input       | running AND idle AND (permission OR prompt-after-question) |
//	| Working           | running AND (active OR turn unknown, marked `?`)    |
//	| Ready for review  | running AND idle AND turn-completed                 |
//	| Completed         | exited OR lost                                      |
//
// Read as a precedence hierarchy so every one of the 36 combinations is
// covered by exactly one rule (no combination is left unstated):
//
//  1. process != running (exited or lost) => Completed (line 56), regardless
//     of turn/interaction.
//  2. process == running AND turn == active => Working (line 54): the
//     Working rule ORs in "active" with no interaction qualifier at all, so
//     it wins regardless of the interaction value.
//  3. process == running AND turn == unknown => Working (line 54): same
//     rule, "turn unknown" is explicitly OR'd in with no interaction
//     qualifier.
//  4. process == running AND turn == idle: only now does interaction decide,
//     per lines 52 and 54:
//     interaction == none                => Ready for review ("turn-completed")
//     interaction == prompt               => Needs input ("prompt-after-question":
//     a free-text prompt becomes "needs input" once idle, i.e. after the
//     question that produced it)
//     interaction == permission            => Needs input (line 52, "permission")
//     interaction == unknown               => Ready for review -- orchestrator's
//     pin: the spec's Needs-input rule names an exhaustive set, "permission OR
//     prompt" (line 52); interaction=unknown satisfies neither disjunct, so it
//     falls to the same branch as "none" (Ready for review, "turn-completed").

// derivationTable is the 36-row 1:1 encoding of the table above.
var derivationTable = []struct {
	process     Process
	turn        Turn
	interaction Interaction
	want        Group
}{
	// process == running, turn == active: Working regardless of interaction (rule 2).
	{ProcessRunning, TurnActive, InteractionNone, GroupWorking},
	{ProcessRunning, TurnActive, InteractionPrompt, GroupWorking},
	{ProcessRunning, TurnActive, InteractionPermission, GroupWorking},
	{ProcessRunning, TurnActive, InteractionUnknown, GroupWorking},

	// process == running, turn == unknown: Working regardless of interaction (rule 3).
	{ProcessRunning, TurnUnknown, InteractionNone, GroupWorking},
	{ProcessRunning, TurnUnknown, InteractionPrompt, GroupWorking},
	{ProcessRunning, TurnUnknown, InteractionPermission, GroupWorking},
	{ProcessRunning, TurnUnknown, InteractionUnknown, GroupWorking},

	// process == running, turn == idle: interaction decides (rule 4).
	{ProcessRunning, TurnIdle, InteractionNone, GroupReadyForReview},
	{ProcessRunning, TurnIdle, InteractionPrompt, GroupNeedsInput},
	{ProcessRunning, TurnIdle, InteractionPermission, GroupNeedsInput},
	{ProcessRunning, TurnIdle, InteractionUnknown, GroupReadyForReview},

	// process == exited: Completed regardless of turn/interaction (rule 1).
	{ProcessExited, TurnActive, InteractionNone, GroupCompleted},
	{ProcessExited, TurnActive, InteractionPrompt, GroupCompleted},
	{ProcessExited, TurnActive, InteractionPermission, GroupCompleted},
	{ProcessExited, TurnActive, InteractionUnknown, GroupCompleted},
	{ProcessExited, TurnIdle, InteractionNone, GroupCompleted},
	{ProcessExited, TurnIdle, InteractionPrompt, GroupCompleted},
	{ProcessExited, TurnIdle, InteractionPermission, GroupCompleted},
	{ProcessExited, TurnIdle, InteractionUnknown, GroupCompleted},
	{ProcessExited, TurnUnknown, InteractionNone, GroupCompleted},
	{ProcessExited, TurnUnknown, InteractionPrompt, GroupCompleted},
	{ProcessExited, TurnUnknown, InteractionPermission, GroupCompleted},
	{ProcessExited, TurnUnknown, InteractionUnknown, GroupCompleted},

	// process == lost: Completed regardless of turn/interaction (rule 1).
	{ProcessLost, TurnActive, InteractionNone, GroupCompleted},
	{ProcessLost, TurnActive, InteractionPrompt, GroupCompleted},
	{ProcessLost, TurnActive, InteractionPermission, GroupCompleted},
	{ProcessLost, TurnActive, InteractionUnknown, GroupCompleted},
	{ProcessLost, TurnIdle, InteractionNone, GroupCompleted},
	{ProcessLost, TurnIdle, InteractionPrompt, GroupCompleted},
	{ProcessLost, TurnIdle, InteractionPermission, GroupCompleted},
	{ProcessLost, TurnIdle, InteractionUnknown, GroupCompleted},
	{ProcessLost, TurnUnknown, InteractionNone, GroupCompleted},
	{ProcessLost, TurnUnknown, InteractionPrompt, GroupCompleted},
	{ProcessLost, TurnUnknown, InteractionPermission, GroupCompleted},
	{ProcessLost, TurnUnknown, InteractionUnknown, GroupCompleted},
}

func TestDerive(t *testing.T) {
	if len(derivationTable) != 36 {
		t.Fatalf("derivation table has %d rows, want 36 (3x3x4)", len(derivationTable))
	}

	for _, tt := range derivationTable {
		tt := tt
		name := string(tt.process) + "/" + string(tt.turn) + "/" + string(tt.interaction)
		t.Run(name, func(t *testing.T) {
			got := Derive(Status{Process: tt.process, Turn: tt.turn, Interaction: tt.interaction})
			if got != tt.want {
				t.Errorf("Derive(process=%s, turn=%s, interaction=%s) = %q, want %q",
					tt.process, tt.turn, tt.interaction, got, tt.want)
			}
		})
	}
}

// TestStringConstants pins the exact wire/string values of every enum
// constant. These strings cross the daemon<->client protocol and are
// persisted verbatim in meta.json (system-spec.md S-2, R-1) -- they are a
// data contract, not an implementation detail, and must not silently drift.
func TestStringConstants(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"ProcessRunning", string(ProcessRunning), "running"},
		{"ProcessExited", string(ProcessExited), "exited"},
		{"ProcessLost", string(ProcessLost), "lost"},

		{"TurnActive", string(TurnActive), "active"},
		{"TurnIdle", string(TurnIdle), "idle"},
		{"TurnUnknown", string(TurnUnknown), "unknown"},

		{"InteractionNone", string(InteractionNone), "none"},
		{"InteractionPrompt", string(InteractionPrompt), "prompt"},
		{"InteractionPermission", string(InteractionPermission), "permission"},
		{"InteractionUnknown", string(InteractionUnknown), "unknown"},

		{"GroupNeedsInput", string(GroupNeedsInput), "needs_input"},
		{"GroupWorking", string(GroupWorking), "working"},
		{"GroupReadyForReview", string(GroupReadyForReview), "ready_for_review"},
		{"GroupCompleted", string(GroupCompleted), "completed"},
	}

	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestDeriveCompleteness guards against Derive falling through to an empty
// or otherwise undefined Group for any of the 36 possible inputs. Every
// combination of the three dimensions must resolve to one of the four
// defined groups (system-spec.md:51-56); none is legitimately "unknown".
func TestDeriveCompleteness(t *testing.T) {
	validGroups := map[Group]bool{
		GroupNeedsInput:     true,
		GroupWorking:        true,
		GroupReadyForReview: true,
		GroupCompleted:      true,
	}

	processes := []Process{ProcessRunning, ProcessExited, ProcessLost}
	turns := []Turn{TurnActive, TurnIdle, TurnUnknown}
	interactions := []Interaction{InteractionNone, InteractionPrompt, InteractionPermission, InteractionUnknown}

	for _, p := range processes {
		for _, tn := range turns {
			for _, i := range interactions {
				got := Derive(Status{Process: p, Turn: tn, Interaction: i})
				if !validGroups[got] {
					t.Errorf("Derive(process=%s, turn=%s, interaction=%s) = %q, want one of the four defined groups",
						p, tn, i, got)
				}
			}
		}
	}
}
