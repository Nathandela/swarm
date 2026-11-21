package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestDiscussionHandoffKeepsScopedReceiptReferences(t *testing.T) {
	st := State{Actions: map[string]Action{}, Admissions: map[string]Admission{}}
	current := Admission{ConversationID: "d", Project: "/work", Sequence: 4}
	for i, project := range []string{"/work", "/withdrawn"} {
		seq := uint64(i + 1)
		a := Action{ID: project, ConversationID: "d", Admission: seq, State: "executing"}
		st.Actions[project] = a
		st.Admissions[admissionKey("d", seq)] = Admission{ConversationID: "d", Project: project}
	}
	got := discussionHandoff(st, current, "Use gcloud", "codex", "", false)
	if !strings.Contains(got, `"action":"/work"`) || strings.Contains(got, "withdrawn") || !strings.HasPrefix(got, "Use gcloud") {
		t.Fatal(got)
	}
}

func TestDiscussionHandoffUsesVerifiedCallTranscript(t *testing.T) {
	a := Admission{Origin: &schema.MessageOrigin{Voice: schema.VoiceOrigin{Generation: 2}}}
	got := discussionHandoff(State{}, a, "Current verified Discord call transcript", "codex", "", false)
	if !strings.Contains(got, "verified call transcript above is the current request") || strings.Contains(got, "human message ID as anchor") {
		t.Fatal(got)
	}
}

func TestHandoffPreservesNativeInputBudget(t *testing.T) {
	nonce := strings.Repeat("a", 64)
	for _, provider := range []string{"claude", "codex"} {
		for n := 0; n <= protocol.MaxSendInputText; n += 7 {
			input := recoveryInputNotice + "local instructions\n" + strings.Repeat("界", n/3)
			got := discussionHandoff(State{}, Admission{}, input, provider, nonce, false)
			if !strings.HasPrefix(got, input) {
				t.Fatal("changed input")
			}
			if got == input {
				continue
			}
			if len(got) > protocol.MaxSendInputText {
				t.Fatal("oversize")
			}
			if provider == "claude" {
				if _, err := claudeinput.ManagerEnvelope(nonce, got); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func TestDiscussionHandoffShowsReplyDispositionWithoutPrivateContent(t *testing.T) {
	current := Admission{ConversationID: "dm", Project: "/work", Sequence: 7}
	st := State{Admissions: map[string]Admission{}, Publications: map[string]Publication{}}
	for i, msg := range []string{"101", "103", "106", "other-project", "other-discussion", "removed-body", "current"} {
		a := current
		a.Sequence = uint64(i + 1)
		a.Origin = &schema.MessageOrigin{MessageID: msg, RequestID: "op-" + msg}
		a.InputText = "private old native input"
		switch msg {
		case "other-project":
			a.Project = "/elsewhere"
		case "other-discussion":
			a.ConversationID = "thread"
		}
		st.Admissions[admissionKey(a.ConversationID, a.Sequence)] = a
	}
	for _, p := range []Publication{
		{ID: "progress", ConversationID: "dm", DiscussionSource: DiscussionOutputSource{AdmissionID: "op-103"}},
		{ID: "recovery", Admission: 6, ConversationID: "dm", Final: true, DiscussionSource: DiscussionOutputSource{AdmissionID: "op-101"}},
		{ID: "ignore", ConversationID: "thread", Final: true, DiscussionSource: DiscussionOutputSource{AdmissionID: "op-106"}},
	} {
		p.Text = "deleted Discord excerpt"
		st.Publications[p.ID] = p
	}
	got := discussionHandoff(st, current, "new request", "codex", "", false)
	for _, want := range []string{`{"message":"101","reply":"final scheduled"}`, `{"message":"103","reply":"progress scheduled"}`, `{"message":"106","reply":"outcome unknown"}`, "new request"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	for _, private := range []string{"other-project", "other-discussion", "private old native input", "deleted Discord excerpt", `"message":"current"`} {
		if strings.Contains(got, private) {
			t.Errorf("leaked %q", private)
		}
	}
}

func TestDiscussionReplyReceiptVerifiesExactSource(t *testing.T) {
	human := schema.MessageOrigin{Kind: "owner_direct", ActorID: "owner", RequesterID: "owner", InstallationID: "install", DiscussionID: "dm", ChannelID: "channel", MessageID: "101", Revision: "revision", RequestID: "op-101"}
	st := State{Admissions: map[string]Admission{"old": {Sequence: 1, ConversationID: "dm", Origin: &human, Project: "work"}}, Publications: map[string]Publication{key("dm", "final"): {ID: "final", Text: "original final", Exported: true, Final: true, ConversationID: "dm", DiscussionSource: DiscussionOutputSource{AdmissionID: "op-101"}}}}
	current := Admission{ConversationID: "dm", Sequence: 2, Project: "work"}
	var grantErr, audienceErr error
	deliveries := 0
	grants := 0
	c := Controller{discussion: &DiscussionScope{DMOwnerID: "owner", InstallationID: "install", DiscussionID: "dm", ThreadID: "channel", ConfirmedDelivery: func(_ context.Context, p DiscussionOutput) ([]string, error) {
		deliveries++
		if p.ID != "final" || p.Text != "original final" || p.AdmissionID != "op-101" {
			t.Fatalf("wrong publication %+v", p)
		}
		return []string{"201"}, nil
	}, VerifyAudience: func(context.Context) error { return audienceErr }, VerifyGrant: func(_ context.Context, o schema.MessageOrigin) error {
		grants++
		if o != human {
			t.Fatalf("wrong source %+v", o)
		}
		return grantErr
	}}}
	for _, tc := range []struct {
		anchor                string
		grant, audience, want error
		calls                 int
	}{
		{"101", nil, nil, nil, 1}, {"101", ErrDiscussionSourceChanged, nil, ErrDiscussionSourceChanged, 1}, {"101", nil, ErrExpired, ErrExpired, 0}, {"unrelated", nil, nil, ErrNotFound, 0},
	} {
		deliveries = 0
		grantErr, audienceErr, grants = tc.grant, tc.audience, 0
		raw, err := c.discussionReplyReceipt(t.Context(), st, current, tc.anchor)
		if !errors.Is(err, tc.want) || grants != tc.calls {
			t.Fatalf("anchor %s: err %v, verified %d", tc.anchor, err, grants)
		}
		if err == nil && (deliveries != 1 || string(raw) != `{"message":"101","reply":"final scheduled","confirmed_discord_message_ids":["201"]}`) || err != nil && (len(raw) != 0 || deliveries != 0) {
			t.Fatalf("unexpected evidence %q", raw)
		}
	}
}

type historyReadFixture struct {
	ControllerRuntime
	binding protocol.ManagerRuntimeBinding
	turn    protocol.ManagerNativeTurn
	reads   int
}

func (f *historyReadFixture) ManagerInputStatus(_ context.Context, binding protocol.ManagerRuntimeBinding, nonce string) (protocol.ManagerNativeTurn, error) {
	f.reads++
	if binding != f.binding || nonce != f.turn.NativeInputID {
		return protocol.ManagerNativeTurn{}, ErrStale
	}
	return f.turn, nil
}

func TestFormerDiscussionTurnEndBoundToNativeAndDiscordIdentities(t *testing.T) {
	nonce := strings.Repeat("a", 64)
	old := protocol.ManagerRuntimeBinding{Session: "m/old", SessionInstance: "incarnation", ConversationID: "dm", Provider: "codex", NativeSessionID: "old-cli", NativeGeneration: "g", ObservationGeneration: "o", LoginEpoch: "l"}
	origin := schema.MessageOrigin{MessageID: "human", RequestID: "op"}
	r := &historyReadFixture{binding: old, turn: protocol.ManagerNativeTurn{NativeInputID: nonce, NativeTurnID: "native-turn", State: "completed", Reason: "answer"}}
	c := Controller{binding: ManagerBinding{ConversationID: "dm", SessionID: "m/new", NativeSessionID: "new-cli"}, runtime: r}
	original := protocol.ComposerSendReq{Session: old.Session, SessionInstance: old.SessionInstance, Origin: &origin, ManagerAdmission: &protocol.ManagerAdmission{Binding: old, Nonce: nonce}}
	originalBytes, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	a := Admission{Origin: &origin, NativeInputID: nonce, NativeTurnID: "native-turn", Request: originalBytes}
	if got := c.formerDiscussionTurnEnd(t.Context(), a); got != "answer" || r.reads != 1 {
		t.Fatalf("history %q reads %d", got, r.reads)
	}
	for _, reason := range []string{"aborted", "error", "refusal", "private diagnostics"} {
		r.turn.Reason = reason
		want := reason
		if reason == "private diagnostics" {
			want = ""
		}
		if got := c.formerDiscussionTurnEnd(t.Context(), a); got != want {
			t.Fatalf("unexpected end %q", got)
		}
	}
	r.turn.Reason = "answer"
	r.turn.ExternallySteered = true
	if got := c.formerDiscussionTurnEnd(t.Context(), a); got != "" {
		t.Fatal("exported manually steered result", got)
	}
	r.turn.ExternallySteered = false
	a.NativeTurnID = "different"
	if got := c.formerDiscussionTurnEnd(t.Context(), a); got != "" {
		t.Fatal("wrong native turn", got)
	}
	a.NativeTurnID = "native-turn"
	reads := r.reads
	altered := origin
	altered.RequestID = "other-owner"
	a.Origin = &altered
	if got := c.formerDiscussionTurnEnd(t.Context(), a); got != "" || r.reads != reads {
		t.Fatal("looked up the wrong origin")
	}
	a.Origin = &origin
	c.binding.ConversationID = "other-thread"
	if got := c.formerDiscussionTurnEnd(t.Context(), a); got != "" || r.reads != reads {
		t.Fatal("looked up another discussion")
	}
}

func TestOlderDiscussionReceiptRemainsAddressableBeyondHandoffWindow(t *testing.T) {
	current := Admission{ConversationID: "same", Sequence: 21}
	st := State{Admissions: map[string]Admission{}}
	for seq := uint64(1); seq < current.Sequence; seq++ {
		msg := fmt.Sprint(seq)
		st.Admissions[admissionKey("same", seq)] = Admission{ConversationID: "same", Sequence: seq, Origin: &schema.MessageOrigin{MessageID: msg, RequestID: "op" + msg}}
	}
	old := priorDiscussionReplies(st, current, "2")
	recent := priorDiscussionReplies(st, current, "")
	if len(recent) != 8 || recent[7].Message != "13" || len(old) != 1 || old[0].Message != "2" {
		t.Fatalf("recent %v old %v", recent, old)
	}
}

func TestHandoffCorrelatesWorkerReportWithOriginalHuman(t *testing.T) {
	origin := schema.MessageOrigin{Kind: "external", InstallationID: "i", DiscussionID: "d", ChannelID: "c", GuildID: "g", ActorID: "u", RequesterID: "u", MessageID: "101", RequestID: "request", Revision: "rev"}
	original := Admission{ConversationID: "d", Sequence: 1, Project: "work", Origin: &origin}
	current := Admission{ConversationID: "d", Sequence: 3, Project: "work"}
	w := WorkerAssociation{ConversationID: "d", Session: "m/w", SessionInstance: "instance", JobID: "job", AttemptID: "attempt", ActionID: "attempt", Project: "work", Admission: 1}
	src := DiscussionOutputSource{AdmissionID: "wake", JobID: w.JobID, AttemptID: w.AttemptID}
	event := Event{ID: "event", WakeID: "wake", WorkerMessage: true, ConversationID: "d", JobID: w.JobID, AttemptID: w.AttemptID, Metadata: json.RawMessage(`{"session":"m/w","instance":"instance","kind":"complete"}`)}
	grant, _ := json.Marshal(controllerActionRequest{Tool: "swarm_create", Project: "work", Origin: &origin})
	launch := Action{ID: "attempt", State: "settled", ConversationID: "d", Request: grant, Admission: 1, Receipt: json.RawMessage(`{"state":"created","session":"m/w","session_instance":"instance"}`)}
	report := Publication{ID: "report", Admission: 2, OutputSequence: 2, ConversationID: "d", Final: true, Exported: true, DiscussionSource: src, Text: "report"}
	ack := Publication{ID: "ack", Admission: 1, OutputSequence: 1, ConversationID: "d", Exported: true, DiscussionSource: DiscussionOutputSource{AdmissionID: "request"}, Text: "working"}
	for _, tc := range []struct {
		name   string
		mutate func(*State)
		want   bool
	}{
		{"one worker", nil, true},
		{"exact native worker turn", func(st *State) {
			v := event
			v.ID = discussionEvidenceID(w)
			v.WorkerMessage = false
			v.WorkerProof = true
			v.NativeTurnID = "worker-turn"
			st.Events[key("d", v.ID)] = v
			wake := st.Wakes[key("d", "wake")]
			wake.EventID = v.ID
			st.Wakes[key("d", "wake")] = wake
		}, true},
		{"worker progress only", func(st *State) { v := report; v.Final = false; st.Publications[key("d", "report")] = v }, false},
		{"split attempt evidence", func(st *State) {
			p := report
			p.DiscussionAttempts = `["attempt","other"]`
			st.Publications[key("d", "report")] = p
		}, false},
		{"split project evidence", func(st *State) {
			p := report
			p.DiscussionProjects = `["work","elsewhere"]`
			st.Publications[key("d", "report")] = p
		}, false},
		{"settled later steering", func(st *State) {
			v := w
			v.ActionID = "steered"
			st.Workers[key("d", key(w.Session, w.SessionInstance))] = v
		}, false},
		{"pending later steering", func(st *State) {
			st.Actions[key("d", "steer")] = Action{ConversationID: "d", State: "executing", Target: key(w.Session, w.SessionInstance)}
		}, false},
		{"changed human grant", func(st *State) {
			v := origin
			v.Revision = "changed"
			a := original
			a.Origin = &v
			st.Admissions[admissionKey("d", 1)] = a
		}, false},
		{"shell child", func(st *State) { v := w; v.VMChild = true; st.Workers[key("d", key(w.Session, w.SessionInstance))] = v }, false},
		{"needs input", func(st *State) {
			v := event
			v.Metadata = json.RawMessage(`{"session":"m/w","instance":"instance","kind":"needs-input"}`)
			st.Events[key("d", "event")] = v
		}, false},
		{"different wake", func(st *State) { v := event; v.WakeID = "elsewhere"; st.Events[key("d", "event")] = v }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := State{Admissions: map[string]Admission{admissionKey("d", 1): original}, Actions: map[string]Action{key("d", "attempt"): launch}, Workers: map[string]WorkerAssociation{key("d", key(w.Session, w.SessionInstance)): w}, Wakes: map[string]Wake{key("d", "wake"): {ID: "wake", ConversationID: "d", EventID: "event", Admission: 2}}, Events: map[string]Event{key("d", "event"): event}, Publications: map[string]Publication{key("d", "report"): report, key("d", "ack"): ack}}
			if tc.mutate != nil {
				tc.mutate(&st)
			}
			c := Controller{discussion: &DiscussionScope{DiscussionID: "d", InstallationID: "i", GuildID: "g", ThreadID: "c", VerifyAudience: func(context.Context) error { return nil }, VerifyGrant: func(context.Context, schema.MessageOrigin) error { return nil }, ConfirmedDelivery: func(_ context.Context, p DiscussionOutput) ([]string, error) {
				switch p.ID {
				case "report":
					return []string{"303"}, nil
				case "ack":
					return []string{"201"}, nil
				}
				t.Fatal(p)
				return nil, nil
			}}}
			raw, err := c.discussionReplyReceipt(t.Context(), st, current, "101")
			if err != nil || !strings.Contains(string(raw), `"reply":"progress scheduled"`) || !strings.Contains(string(raw), `["201"]`) || strings.Contains(string(raw), `["303"]`) != tc.want {
				t.Fatalf("receipt %s err %v", raw, err)
			}
		})
	}
}
