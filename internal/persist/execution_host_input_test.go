package persist

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/status"
)

func hostInputFixture() ExecutionRecord {
	r := codexExecutionFixture()
	r.Codex.HostInputs = map[string]CodexHostInput{"client": {Claim: CodexHostInputClaim{NativeIncarnation: "backend-1", FeedEpoch: "dial-1", ThreadID: r.ConversationID, ClientID: "client", OwnerRevision: 1}, AcceptedTurnID: "turn-1"}}
	return r
}

func TestExecutionHostInputRoundTripAndVersionFence(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := hostInputFixture()
	input := r.Codex.HostInputs["client"]
	input.EchoTurnID, input.EchoItemID, input.EchoSequence = "turn-1", "user", 1
	r.Codex.HostInputs["client"] = input
	if err := s.SaveExecution(r); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadExecution(r.SessionID)
	if err != nil || !reflect.DeepEqual(got.Codex.HostInputs, r.Codex.HostInputs) {
		t.Fatalf("host receipt did not round-trip: %+v %v", got.Codex, err)
	}
	for _, version := range []int{1, 2} {
		r.Version = version
		if err := s.SaveExecution(r); err == nil {
			t.Fatalf("version %d accepted new host custody", version)
		}
	}
	r = codexExecutionFixture()
	r.Version = 2
	item := r.Codex.Items["tool-1"]
	item.Kind, item.ClientID = "userMessage", "client"
	r.Codex.Items["tool-1"] = item
	if err := s.SaveExecution(r); err == nil {
		t.Fatal("version2 accepted native client receipt")
	}
}

func TestExecutionHostInputBoundsAndIndependentRetirement(t *testing.T) {
	for _, scenario := range []string{"future-owner", "foreign-thread", "partial-echo", "missing-echo-sequence", "future-echo-sequence", "sequence-without-echo", "wrong-accepted", "actor-capacity"} {
		t.Run(scenario, func(t *testing.T) {
			r := hostInputFixture()
			input := r.Codex.HostInputs["client"]
			switch scenario {
			case "future-owner":
				input.Claim.OwnerRevision++
			case "foreign-thread":
				input.Claim.ThreadID = "foreign"
			case "partial-echo":
				input.EchoItemID = "item"
			case "missing-echo-sequence":
				input.EchoTurnID, input.EchoItemID = "turn-1", "user"
			case "future-echo-sequence":
				input.EchoTurnID, input.EchoItemID, input.EchoSequence = "turn-1", "user", 2
			case "sequence-without-echo":
				input.EchoSequence = 1
			case "wrong-accepted":
				input.Claim.ExpectedTurnID = "other"
			case "actor-capacity":
				for i := 0; i < MaxExecutionActors; i++ {
					next := input
					next.Claim.ClientID = fmt.Sprint(i)
					r.Codex.HostInputs[next.Claim.ClientID] = next
				}
			}
			r.Codex.HostInputs["client"] = input
			if err := r.Validate(); err == nil {
				t.Fatal("invalid host receipt accepted")
			}
		})
	}
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := hostInputFixture()
	r.MainTurn = status.TurnIdle
	r.Actors = nil
	r.Codex.State = "terminal"
	r.Codex.Items, r.Codex.Children, r.Codex.Decisions = nil, nil, nil
	if err := s.SaveExecution(r); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireExecution(r.SessionID, r.Incarnation); err == nil {
		t.Fatal("unresolved host input retired")
	}
	r.Codex.HostInputs = nil
	r.Codex.Children = map[string]CodexExecutionChild{"child": {ParentThreadID: r.ConversationID, NativeIncarnation: "backend-1", FeedEpoch: "dial-1", TurnID: r.TurnID, ChildTurnID: "old"}}
	r.Codex.Stimuli = map[string]CodexExecutionStimulus{"send": {NativeIncarnation: "backend-1", FeedEpoch: "dial-1", ActorThreadID: r.ConversationID, TurnID: r.TurnID, ItemID: "send", ReceiverThreadID: "child", PreviousTurnID: "old", StartSequence: 1, Phase: "started"}}
	if err := s.SaveExecution(r); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireExecution(r.SessionID, r.Incarnation); err == nil {
		t.Fatal("independent unresolved child stimulus retired")
	}
}

func TestExecutionHostInputOldFeedEchoSurvivesSequenceReset(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := hostInputFixture()
	input := r.Codex.HostInputs["client"]
	input.EchoTurnID, input.EchoItemID, input.EchoSequence = "turn-1", "user", 7
	r.Codex.HostInputs["client"] = input
	r.Codex.FeedEpoch, r.Codex.Sequence, r.Codex.LastEventHash = "next-feed", 0, ""
	if err := s.SaveExecution(r); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadExecution(r.SessionID)
	if err != nil || got.Codex.HostInputs["client"] != input {
		t.Fatalf("retained old feed echo lost: %+v %v", got.Codex, err)
	}
}
