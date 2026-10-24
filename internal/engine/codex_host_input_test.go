package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func beginCodexHostInput(t *testing.T, e *Engine, feed CodexFeedIdentity, client, expected string) persist.CodexHostInputClaim {
	t.Helper()
	claim, err := e.BeginCodexHostInput("s1", persist.CodexHostInputClaim{NativeIncarnation: feed.NativeIncarnation, FeedEpoch: feed.FeedEpoch, ThreadID: feed.ThreadID, ClientID: client, ExpectedTurnID: expected})
	if err != nil {
		t.Fatal(err)
	}
	return claim
}
func codexHostEcho(t *testing.T, e *Engine, feed CodexFeedIdentity, turn, client string) {
	t.Helper()
	for _, kind := range []CodexExecutionKind{CodexItemStarted, CodexItemCompleted} {
		if err := codexApplyOne(e, feed, CodexExecutionEvent{Kind: kind, TurnID: turn, ItemID: "user-" + client, ItemKind: CodexUserMessage, ClientID: client}); err != nil {
			t.Fatal(err)
		}
	}
}
func TestCodexHostInputJoinsExactRPCAndEchoInEitherOrder(t *testing.T) {
	for _, echoFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(echoFirst), func(t *testing.T) {
			e, feed := codexExecutionEngine(t)
			claim := beginCodexHostInput(t, e, feed, "client-1", "")
			if claim.OwnerRevision != 2 {
				t.Fatal("owner fence not advanced")
			}
			if err := codexApplyOne(e, feed, CodexExecutionEvent{Kind: CodexTurnStarted, TurnID: "owner-turn"}); err != nil {
				t.Fatal(err)
			}
			if echoFirst {
				codexHostEcho(t, e, feed, "owner-turn", "client-1")
			} else if err := e.RecordCodexHostInputResult("s1", claim, "owner-turn", nil); err != nil {
				t.Fatal(err)
			}
			snapshot, _ := e.ExecutionSnapshot("s1")
			if len(snapshot.Codex.HostInputs) != 1 || !e.ExecutionCustodyUnsafe("s1") {
				t.Fatal("one receipt prematurely released host input")
			}
			if echoFirst {
				if err := e.RecordCodexHostInputResult("s1", claim, "owner-turn", nil); err != nil {
					t.Fatal(err)
				}
			} else {
				codexHostEcho(t, e, feed, "owner-turn", "client-1")
			}
			snapshot, _ = e.ExecutionSnapshot("s1")
			if len(snapshot.Codex.HostInputs) != 0 || snapshot.Codex.OwnerRevision != 2 || snapshot.Codex.UnknownCustody {
				t.Fatalf("exact receipt not joined: %+v", snapshot.Codex)
			}
		})
	}
}
func TestCodexHostInputOlderReplyCannotRebindNewerOwner(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	if err := codexApplyOne(e, feed, CodexExecutionEvent{Kind: CodexTurnStarted, TurnID: "active"}); err != nil {
		t.Fatal(err)
	}
	first := beginCodexHostInput(t, e, feed, "first", "active")
	second := beginCodexHostInput(t, e, feed, "second", "active")
	codexHostEcho(t, e, feed, "active", "first")
	codexHostEcho(t, e, feed, "active", "second")
	if err := e.RecordCodexHostInputResult("s1", second, "active", nil); err != nil {
		t.Fatal(err)
	}
	if err := e.RecordCodexHostInputResult("s1", first, "active", nil); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := e.ExecutionSnapshot("s1")
	if snapshot.Codex.OwnerRevision != second.OwnerRevision || len(snapshot.Codex.HostInputs) != 0 {
		t.Fatal("older reply changed current owner or erased newer input")
	}
}
func TestCodexHostInputNeverClearsIndependentCustody(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	trace := codexExecutionTrace(t, "quota")
	applyCodexTrace(t, e, feed, trace[:len(trace)-1])
	turn := trace[0].TurnID
	codexSpawnChild(t, e, feed, "", turn, "child", "child-turn")
	e.mu.Lock()
	c := e.sessions["s1"].codexExecution
	c.UnknownCustody = true
	c.Stimuli = map[string]persist.CodexExecutionStimulus{"held": {NativeIncarnation: feed.NativeIncarnation, FeedEpoch: feed.FeedEpoch, ActorThreadID: feed.ThreadID, TurnID: turn, ItemID: "send", ReceiverThreadID: "child", PreviousTurnID: "old", StartSequence: 1, Phase: "started"}}
	beforeChildren := cloneCodexExecution(c).Children
	beforeStimuli := cloneCodexExecution(c).Stimuli
	e.mu.Unlock()
	claim := beginCodexHostInput(t, e, feed, "owner", turn)
	codexHostEcho(t, e, feed, turn, "owner")
	if err := e.RecordCodexHostInputResult("s1", claim, turn, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := e.ExecutionSnapshot("s1")
	if !snapshot.Codex.UnknownCustody || !reflect.DeepEqual(snapshot.Codex.Children, beforeChildren) || !reflect.DeepEqual(snapshot.Codex.Stimuli, beforeStimuli) {
		t.Fatal("host receipt cleared independent native custody")
	}
}
func TestCodexHostInputRejectsForeignReceiptAndKeepsAmbiguity(t *testing.T) {
	for _, kind := range []string{"feed", "native", "revision", "turn", "empty"} {
		t.Run(kind, func(t *testing.T) {
			e, feed := codexExecutionEngine(t)
			claim := beginCodexHostInput(t, e, feed, "client", "active")
			result := "active"
			switch kind {
			case "feed":
				claim.FeedEpoch = "other"
			case "native":
				claim.NativeIncarnation = "other"
			case "revision":
				claim.OwnerRevision++
			case "turn":
				result = "other"
			case "empty":
				result = ""
			}
			if err := e.RecordCodexHostInputResult("s1", claim, result, nil); err == nil {
				t.Fatal("foreign or malformed receipt accepted")
			}
			snapshot, _ := e.ExecutionSnapshot("s1")
			if len(snapshot.Codex.HostInputs) != 1 {
				t.Fatal("ambiguous input forgotten")
			}
		})
	}
}
func TestCodexHostInputDefinitiveRefusalCannotEraseEcho(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	if err := codexApplyOne(e, feed, CodexExecutionEvent{Kind: CodexTurnStarted, TurnID: "active"}); err != nil {
		t.Fatal(err)
	}
	claim := beginCodexHostInput(t, e, feed, "client", "active")
	codexHostEcho(t, e, feed, "active", "client")
	if err := e.RefuseCodexHostInput("s1", claim, nil); err == nil {
		t.Fatal("refusal erased observed input effect")
	}
	snapshot, _ := e.ExecutionSnapshot("s1")
	if len(snapshot.Codex.HostInputs) != 1 {
		t.Fatal("echo custody lost")
	}
}
func TestCodexHostInputDurabilityFailureRetainsClaim(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	claim := beginCodexHostInput(t, e, feed, "client", "")
	if err := codexApplyOne(e, feed, CodexExecutionEvent{Kind: CodexTurnStarted, TurnID: "turn"}); err != nil {
		t.Fatal(err)
	}
	codexHostEcho(t, e, feed, "turn", "client")
	e.mu.Lock()
	e.storeExecution = func(persist.ExecutionRecord) error { return errors.New("disk full") }
	e.mu.Unlock()
	if err := e.RecordCodexHostInputResult("s1", claim, "turn", nil); err == nil {
		t.Fatal("failed receipt persistence acknowledged")
	}
	snapshot, _ := e.ExecutionSnapshot("s1")
	if len(snapshot.Codex.HostInputs) != 1 || !e.ExecutionCustodyUnsafe("s1") {
		t.Fatal("failed receipt persistence released custody")
	}
}

func TestCodexHostInputExactReceiptRequalifiesOnlyItsOwnerTurn(t *testing.T) {
	for _, replyFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(replyFirst), func(t *testing.T) {
			e, feed := codexExecutionEngine(t)
			trace := codexExecutionTrace(t, "quota")
			applyCodexTrace(t, e, feed, trace)
			claim := beginCodexHostInput(t, e, feed, "owner", "")
			if replyFirst {
				if err := e.RecordCodexHostInputResult("s1", claim, "next", nil); err != nil {
					t.Fatal(err)
				}
			}
			for _, event := range trace {
				event.TurnID = "next"
				if event.ItemKind == CodexUserMessage {
					event.ClientID = "owner"
				}
				if err := codexApplyOne(e, feed, event); err != nil {
					t.Fatal(err)
				}
			}
			if !replyFirst {
				if _, ok := e.CodexQuotaCandidate("s1"); ok {
					t.Fatal("echo without typed reply qualified owner turn")
				}
				if err := e.RecordCodexHostInputResult("s1", claim, "next", nil); err != nil {
					t.Fatal(err)
				}
			}
			candidate, ok := e.CodexQuotaCandidate("s1")
			if !ok || candidate.TurnID != "next" || candidate.OwnerRevision != claim.OwnerRevision {
				t.Fatalf("exact observed owner turn did not qualify: %+v %v", candidate, ok)
			}
		})
	}
}

func TestCodexHostInputLateEchoCannotInheritOldQuotaWork(t *testing.T) {
	for _, replyFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(replyFirst), func(t *testing.T) {
			e, feed := codexExecutionEngine(t)
			trace := codexExecutionTrace(t, "quota")
			applyCodexTrace(t, e, feed, trace[:len(trace)-2])
			turn := trace[0].TurnID
			claim := beginCodexHostInput(t, e, feed, "new-input", turn)
			if err := codexApplyOne(e, feed, CodexExecutionEvent{Kind: CodexItemStarted, TurnID: turn, ItemID: "new-user", ItemKind: CodexUserMessage, ClientID: "new-input"}); err != nil {
				t.Fatal(err)
			}
			for _, event := range trace[len(trace)-2:] {
				if err := codexApplyOne(e, feed, event); err != nil {
					t.Fatal(err)
				}
			}
			if replyFirst {
				if err := e.RecordCodexHostInputResult("s1", claim, turn, nil); err != nil {
					t.Fatal(err)
				}
			}
			if err := codexApplyOne(e, feed, CodexExecutionEvent{Kind: CodexItemCompleted, TurnID: turn, ItemID: "new-user", ItemKind: CodexUserMessage, ClientID: "new-input"}); err != nil {
				t.Fatal(err)
			}
			if !replyFirst {
				if err := e.RecordCodexHostInputResult("s1", claim, turn, nil); err != nil {
					t.Fatal(err)
				}
			}
			snapshot, _ := e.ExecutionSnapshot("s1")
			if len(snapshot.Codex.HostInputs) != 0 {
				t.Fatal("exact historical delivery receipt remained unresolved")
			}
			if candidate, ok := e.CodexQuotaCandidate("s1"); ok || snapshot.Codex.OwnerRevision == claim.OwnerRevision {
				t.Fatalf("late echo transferred old quota work to new input: %+v state=%+v", candidate, snapshot.Codex)
			}
		})
	}
}

func TestCodexHostInputCapacityNeverOverwritesPendingInputs(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	first := beginCodexHostInput(t, e, feed, "first", "active")
	if _, err := e.BeginCodexHostInput("s1", persist.CodexHostInputClaim{NativeIncarnation: feed.NativeIncarnation, FeedEpoch: feed.FeedEpoch, ThreadID: feed.ThreadID, ClientID: "first"}); err == nil {
		t.Fatal("duplicate client replaced unresolved input")
	}
	for i := 0; i < persist.MaxExecutionActors; i++ {
		_, err := e.BeginCodexHostInput("s1", persist.CodexHostInputClaim{NativeIncarnation: feed.NativeIncarnation, FeedEpoch: feed.FeedEpoch, ThreadID: feed.ThreadID, ClientID: fmt.Sprintf("input-%d-%s", i, strings.Repeat("<", 200))})
		if err != nil {
			before, _ := e.ExecutionSnapshot("s1")
			if before.Codex.HostInputs["first"].Claim != first || len(before.Codex.HostInputs) == 0 || len(before.Codex.HostInputs) >= persist.MaxExecutionActors {
				t.Fatal("bounded byte reservation lost or overfilled pending custody")
			}
			if _, again := e.BeginCodexHostInput("s1", persist.CodexHostInputClaim{NativeIncarnation: feed.NativeIncarnation, FeedEpoch: feed.FeedEpoch, ThreadID: feed.ThreadID, ClientID: fmt.Sprintf("input-%d-%s", i, strings.Repeat("<", 200))}); again == nil {
				t.Fatal("capacity failure was not repeatable")
			}
			after, _ := e.ExecutionSnapshot("s1")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("refused capacity advanced owner or replaced pending inputs")
			}
			return
		}
	}
	t.Fatal("receipt reservation never reached its bound")
}

func TestCodexHostInputCapacityReservesEchoSequenceBeforeEffects(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	claim := persist.CodexHostInputClaim{NativeIncarnation: feed.NativeIncarnation, FeedEpoch: feed.FeedEpoch, ThreadID: feed.ThreadID, ClientID: "edge"}
	func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		s := e.sessions["s1"]
		c := s.codexExecution
		future := claim
		future.OwnerRevision = s.executionIdentity.OwnerRevision + 1
		sizeWithoutEchoSequence := func() int {
			r := executionSnapshot("s1", s)
			r.OwnerRevision = future.OwnerRevision
			r.Codex.HostInputs = map[string]persist.CodexHostInput{"edge": {Claim: future, AcceptedTurnID: strings.Repeat("<", 256), EchoTurnID: strings.Repeat("<", 256), EchoItemID: strings.Repeat("<", 256)}}
			encoded, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			return len(encoded)
		}
		const target = persist.MaxExecutionBytes - 1
		padding := persist.CodexExecutionDecision{NativeIncarnation: "n", FeedEpoch: "f", TurnID: "t", RequestID: "r", Kind: "question"}
		key := "pad"
		c.Decisions[key] = padding
		for i := 0; i < persist.MaxExecutionActors-2; i++ {
			child := fmt.Sprintf("padding-%d", i)
			c.Children[child] = persist.CodexExecutionChild{ParentThreadID: feed.ThreadID, NativeIncarnation: strings.Repeat("n", 256), FeedEpoch: strings.Repeat("f", 256), TurnID: strings.Repeat("t", 256), ChildTurnID: strings.Repeat("c", 256)}
			if sizeWithoutEchoSequence() > target {
				delete(c.Children, child)
				break
			}
		}
		remaining := target - sizeWithoutEchoSequence()
		grow := min(remaining, 1024-len(key))
		delete(c.Decisions, key)
		key += strings.Repeat("p", grow)
		remaining -= grow
		for _, field := range []*string{&padding.NativeIncarnation, &padding.FeedEpoch, &padding.TurnID, &padding.RequestID} {
			grow = min(remaining, 256-len(*field))
			*field += strings.Repeat("x", grow)
			remaining -= grow
		}
		c.Decisions[key] = padding
		if remaining != 0 || sizeWithoutEchoSequence() != target {
			t.Fatal("capacity-edge fixture did not fill exact byte bound")
		}
	}()
	before, _ := e.ExecutionSnapshot("s1")
	if err := before.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.BeginCodexHostInput("s1", claim); err == nil {
		t.Fatal("input crossed byte boundary without reserving echo sequence")
	}
	after, _ := e.ExecutionSnapshot("s1")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("size-only reservation mutated live claim or owner state")
	}
}

func TestCodexHostInputReconnectRetainsUnresolvedOldClaim(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	claim := beginCodexHostInput(t, e, feed, "client", "")
	feed.FeedEpoch = "new-connection"
	feed.OwnerRevision = claim.OwnerRevision
	if err := e.BeginCodexExecutionFeed("s1", feed); err != nil {
		t.Fatal(err)
	}
	if err := e.RecordCodexHostInputResult("s1", claim, "accepted", nil); err == nil {
		t.Fatal("old connection receipt accepted after reconnect")
	}
	snapshot, _ := e.ExecutionSnapshot("s1")
	if snapshot.Codex.HostInputs["client"].Claim != claim || !snapshot.Codex.UnknownCustody {
		t.Fatal("reconnect erased unresolved old host custody")
	}
}

func TestCodexHostInputEchoPersistenceFailureKeepsReceiptCustody(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	claim := beginCodexHostInput(t, e, feed, "client", "")
	if err := e.RecordCodexHostInputResult("s1", claim, "turn", nil); err != nil {
		t.Fatal(err)
	}
	if err := codexApplyOne(e, feed, CodexExecutionEvent{Kind: CodexTurnStarted, TurnID: "turn"}); err != nil {
		t.Fatal(err)
	}
	if err := codexApplyOne(e, feed, CodexExecutionEvent{Kind: CodexItemStarted, TurnID: "turn", ItemID: "user", ItemKind: CodexUserMessage, ClientID: "client"}); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.storeExecution = func(persist.ExecutionRecord) error { return errors.New("disk full") }
	e.mu.Unlock()
	if err := codexApplyOne(e, feed, CodexExecutionEvent{Kind: CodexItemCompleted, TurnID: "turn", ItemID: "user", ItemKind: CodexUserMessage, ClientID: "client"}); err == nil {
		t.Fatal("failed echo persistence accepted")
	}
	snapshot, _ := e.ExecutionSnapshot("s1")
	if snapshot.Codex.HostInputs["client"].Claim != claim || !snapshot.Codex.UnknownCustody {
		t.Fatal("failed echo join lost exact pending claim")
	}
}

func TestCodexHostInputChildEchoCannotConsumeMainClaim(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	if err := codexApplyOne(e, feed, CodexExecutionEvent{Kind: CodexTurnStarted, TurnID: "main"}); err != nil {
		t.Fatal(err)
	}
	codexSpawnChild(t, e, feed, "", "main", "child", "child-turn")
	claim := beginCodexHostInput(t, e, feed, "client", "")
	if err := e.RecordCodexHostInputResult("s1", claim, "child-turn", nil); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []CodexExecutionKind{CodexItemStarted, CodexItemCompleted} {
		if err := codexApplyOne(e, feed, CodexExecutionEvent{Kind: kind, ActorThreadID: "child", TurnID: "child-turn", ItemID: "child-user", ItemKind: CodexUserMessage, ClientID: "client"}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, _ := e.ExecutionSnapshot("s1")
	if snapshot.Codex.HostInputs["client"].Claim != claim || snapshot.Codex.HostInputs["client"].EchoTurnID != "" {
		t.Fatal("child user echo consumed main-thread claim")
	}
}

func TestCodexHostInputDefinitiveRefusalRequiresFreshClaim(t *testing.T) {
	e, feed := codexExecutionEngine(t)
	claim := beginCodexHostInput(t, e, feed, "client", "old")
	if err := e.RefuseCodexHostInput("s1", claim, nil); err != nil {
		t.Fatal(err)
	}
	fresh := beginCodexHostInput(t, e, feed, "client", "")
	if fresh.OwnerRevision != claim.OwnerRevision+1 || fresh.ExpectedTurnID != "" {
		t.Fatal("retry reused refused owner claim")
	}
	if err := e.RecordCodexHostInputResult("s1", claim, "old", nil); err == nil {
		t.Fatal("old reply overwrote retry claim")
	}
	snapshot, _ := e.ExecutionSnapshot("s1")
	if snapshot.Codex.HostInputs["client"].Claim != fresh {
		t.Fatal("fresh retry claim lost")
	}
}

func TestCodexHostInputReceiptGuardChangeRetainsDurableCustody(t *testing.T) {
	for _, changedAt := range []int{1, 2} {
		t.Run(fmt.Sprint(changedAt), func(t *testing.T) {
			e, feed := codexExecutionEngine(t)
			claim := beginCodexHostInput(t, e, feed, "client", "")
			if err := codexApplyOne(e, feed, CodexExecutionEvent{Kind: CodexTurnStarted, TurnID: "turn"}); err != nil {
				t.Fatal(err)
			}
			codexHostEcho(t, e, feed, "turn", "client")
			calls := 0
			guard := func() bool {
				calls++
				if _, err := e.ExecutionSnapshot("s1"); err != nil {
					t.Fatal(err)
				}
				if !e.ExecutionCustodyUnsafe("s1") {
					t.Fatal("physical check exposed eligible partial settlement")
				}
				return calls < changedAt
			}
			var saved persist.ExecutionRecord
			e.mu.Lock()
			e.storeExecution = func(record persist.ExecutionRecord) error { saved = record; return nil }
			e.mu.Unlock()
			if err := e.RecordCodexHostInputResult("s1", claim, "turn", guard); err == nil {
				t.Fatal("changed connection settled host receipt")
			}
			snapshot, _ := e.ExecutionSnapshot("s1")
			if snapshot.Codex.HostInputs["client"].Claim != claim || !snapshot.Codex.UnknownCustody || saved.Codex.HostInputs["client"].Claim != claim || !saved.Codex.UnknownCustody {
				t.Fatal("changed connection lost live or durable exact claim")
			}
		})
	}
}

func TestCodexHostInputReceiptCannotAlterReplacementSession(t *testing.T) {
	for _, replaceAt := range []int{1, 2} {
		t.Run(fmt.Sprint(replaceAt), func(t *testing.T) {
			e, feed := codexExecutionEngine(t)
			claim := beginCodexHostInput(t, e, feed, "client", "")
			calls, saves := 0, 0
			e.mu.Lock()
			e.storeExecution = func(persist.ExecutionRecord) error { saves++; return nil }
			e.mu.Unlock()
			guard := func() bool {
				calls++
				if calls == replaceAt {
					if err := e.RegisterExecutionSession("s1", "", 1, nil, status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}, ExecutionIdentity{Incarnation: "replacement", ConversationID: "new-thread", OwnerRevision: 99}, nil); err != nil {
						t.Fatal(err)
					}
				}
				return true
			}
			if err := e.RecordCodexHostInputResult("s1", claim, "turn", guard); err == nil {
				t.Fatal("old receipt survived session replacement")
			}
			snapshot, _ := e.ExecutionSnapshot("s1")
			if snapshot.Incarnation != "replacement" || snapshot.OwnerRevision != 99 || snapshot.Codex != nil || saves != replaceAt-1 {
				t.Fatalf("old receipt mutated or rewrote successor: %+v saves=%d", snapshot, saves)
			}
		})
	}
}
