package opencode

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// The legacy-wire decoder's contract is deliberately asymmetric: a kind it does
// not know decodes to a fact-less event with NO error (forward compatibility is
// the reducer's decision), while a malformed payload for a kind it does know is
// an error. That is what lets the reducer tell "newer than us" apart from
// "corrupt".

func TestDecodeEvent_SessionStatus(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want SessionStatus
	}{
		{
			name: "busy",
			raw:  `{"id":"evt_1","type":"session.status","properties":{"sessionID":"ses_root","status":{"type":"busy"}}}`,
			want: SessionStatus{Type: "busy"},
		},
		{
			name: "idle",
			raw:  `{"id":"evt_1","type":"session.status","properties":{"sessionID":"ses_root","status":{"type":"idle"}}}`,
			want: SessionStatus{Type: "idle"},
		},
		{
			name: "retry with action",
			raw:  `{"id":"evt_1","type":"session.status","properties":{"sessionID":"ses_root","status":{"type":"retry","attempt":2,"message":"rate limited","next":1500,"action":{"reason":"rate-limit","provider":"openrouter","title":"Provider busy","message":"rate limited","label":"Retrying"}}}}`,
			want: SessionStatus{
				Type: "retry", Attempt: 2, Message: "rate limited", Next: 1500,
				Action: &RetryAction{Reason: "rate-limit", Provider: "openrouter", Title: "Provider busy", Message: "rate limited", Label: "Retrying"},
			},
		},
		{
			name: "unknown subtype decodes verbatim",
			raw:  `{"id":"evt_1","type":"session.status","properties":{"sessionID":"ses_root","status":{"type":"paused"}}}`,
			want: SessionStatus{Type: "paused"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := DecodeEvent([]byte(tc.raw))
			if err != nil {
				t.Fatalf("DecodeEvent: %v", err)
			}
			if ev.ID != "evt_1" || ev.Kind != EventSessionStatus {
				t.Fatalf("envelope = %q/%q, want evt_1/%q", ev.ID, ev.Kind, EventSessionStatus)
			}
			if ev.SessionStatus == nil {
				t.Fatal("SessionStatus fact missing")
			}
			if ev.SessionStatus.SessionID != "ses_root" {
				t.Fatalf("SessionID = %q", ev.SessionStatus.SessionID)
			}
			if !reflect.DeepEqual(ev.SessionStatus.Status, tc.want) {
				t.Fatalf("Status = %+v, want %+v", ev.SessionStatus.Status, tc.want)
			}
			if ev.SessionIdle != nil || ev.SessionError != nil || ev.MessageUpdated != nil || ev.MessagePartUpdated != nil {
				t.Fatal("other facts should be nil")
			}
		})
	}
}

func TestDecodeEvent_SessionIdle(t *testing.T) {
	ev, err := DecodeEvent([]byte(`{"id":"evt_2","type":"session.idle","properties":{"sessionID":"ses_root"}}`))
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.Kind != EventSessionIdle || ev.SessionIdle == nil || ev.SessionIdle.SessionID != "ses_root" {
		t.Fatalf("idle event = %+v", ev)
	}
}

func TestDecodeEvent_SessionError(t *testing.T) {
	raw := `{"id":"evt_3","type":"session.error","properties":{"sessionID":"ses_root","error":{"name":"APIError","data":{"message":"quota exceeded","statusCode":403,"isRetryable":false}}}}`
	ev, err := DecodeEvent([]byte(raw))
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.SessionError == nil || ev.SessionError.SessionID != "ses_root" {
		t.Fatalf("session error fact = %+v", ev.SessionError)
	}
	e := ev.SessionError.Error
	if e.Name != "APIError" || e.Data.Message != "quota exceeded" {
		t.Fatalf("error = %+v", e)
	}
	if e.Data.StatusCode == nil || *e.Data.StatusCode != 403 {
		t.Fatalf("statusCode = %v", e.Data.StatusCode)
	}
	if e.Data.IsRetryable == nil || *e.Data.IsRetryable {
		t.Fatalf("isRetryable = %v", e.Data.IsRetryable)
	}
}

func TestDecodeEvent_MessageUpdatedUser(t *testing.T) {
	raw := `{"id":"evt_4","type":"message.updated","properties":{"sessionID":"ses_root","info":{"id":"msg_u1","sessionID":"ses_root","role":"user","time":{"created":1700},"agent":"build","summary":{"title":"ctx","body":"prev","diffs":[]}}}}`
	ev, err := DecodeEvent([]byte(raw))
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.MessageUpdated == nil {
		t.Fatal("message fact missing")
	}
	m := ev.MessageUpdated.Info
	if m.ID != "msg_u1" || m.Role != "user" || m.Time.Created != 1700 {
		t.Fatalf("info = %+v", m)
	}
	if !bytes.HasPrefix(bytes.TrimSpace(m.Summary), []byte("{")) || m.IsAssistantSummary() {
		t.Fatalf("summary classification: user=%v assistant=%v", bytes.HasPrefix(bytes.TrimSpace(m.Summary), []byte("{")), m.IsAssistantSummary())
	}
	if !json.Valid(m.Raw) || !bytes.Contains(m.Raw, []byte(`"msg_u1"`)) {
		t.Fatalf("raw retention lost: %s", m.Raw)
	}
}

func TestDecodeEvent_MessageUpdatedAssistant(t *testing.T) {
	t.Run("terminal error keeps full error data", func(t *testing.T) {
		raw := `{"id":"evt_5","type":"message.updated","properties":{"sessionID":"ses_root","info":{"id":"msg_a1","sessionID":"ses_root","role":"assistant","parentID":"msg_u1","time":{"created":2000,"completed":2500},"finish":"stop","error":{"name":"APIError","data":{"message":"no credit","statusCode":403,"isRetryable":false}}}}}`
		ev, err := DecodeEvent([]byte(raw))
		if err != nil {
			t.Fatalf("DecodeEvent: %v", err)
		}
		if ev.MessageUpdated == nil {
			t.Fatal("message fact missing")
		}
		m := ev.MessageUpdated.Info
		if m.ParentID != "msg_u1" || m.Time.Completed != 2500 || m.Finish != "stop" {
			t.Fatalf("info = %+v", m)
		}
		if m.Error == nil || m.Error.Name != "APIError" {
			t.Fatalf("error = %+v", m.Error)
		}
		if m.IsAssistantSummary() || bytes.HasPrefix(bytes.TrimSpace(m.Summary), []byte("{")) {
			t.Fatal("plain assistant message misread as summary")
		}
	})

	t.Run("assistant summary marker", func(t *testing.T) {
		raw := `{"id":"evt_6","type":"message.updated","properties":{"sessionID":"ses_root","info":{"id":"msg_s1","sessionID":"ses_root","role":"assistant","parentID":"msg_u1","time":{"created":2000,"completed":2500},"summary":true}}}`
		ev, err := DecodeEvent([]byte(raw))
		if err != nil {
			t.Fatalf("DecodeEvent: %v", err)
		}
		if ev.MessageUpdated == nil {
			t.Fatal("message fact missing")
		}
		if m := ev.MessageUpdated.Info; !m.IsAssistantSummary() || bytes.HasPrefix(bytes.TrimSpace(m.Summary), []byte("{")) {
			t.Fatalf("summary classification: assistant=%v user=%v", m.IsAssistantSummary(), bytes.HasPrefix(bytes.TrimSpace(m.Summary), []byte("{")))
		}
	})

	t.Run("session id filled from properties when info omits it", func(t *testing.T) {
		raw := `{"id":"evt_7","type":"message.updated","properties":{"sessionID":"ses_root","info":{"id":"msg_a2","role":"assistant","time":{"created":2000}}}}`
		ev, err := DecodeEvent([]byte(raw))
		if err != nil {
			t.Fatalf("DecodeEvent: %v", err)
		}
		if ev.MessageUpdated == nil {
			t.Fatal("message fact missing")
		}
		if got := ev.MessageUpdated.Info.SessionID; got != "ses_root" {
			t.Fatalf("SessionID = %q, want ses_root", got)
		}
	})
}

func TestDecodeEvent_MessagePartUpdated(t *testing.T) {
	t.Run("text part flags and raw retention", func(t *testing.T) {
		raw := `{"id":"evt_8","type":"message.part.updated","properties":{"sessionID":"ses_root","part":{"id":"prt_1","sessionID":"ses_root","messageID":"msg_u1","type":"text","text":"context","synthetic":true,"ignored":true}}}`
		ev, err := DecodeEvent([]byte(raw))
		if err != nil {
			t.Fatalf("DecodeEvent: %v", err)
		}
		if ev.MessagePartUpdated == nil {
			t.Fatal("part fact missing")
		}
		p := ev.MessagePartUpdated.Part
		if p.ID != "prt_1" || p.MessageID != "msg_u1" || p.Type != "text" || p.Text != "context" {
			t.Fatalf("part = %+v", p)
		}
		if !p.Synthetic || !p.Ignored {
			t.Fatalf("flags: synthetic=%v ignored=%v", p.Synthetic, p.Ignored)
		}
		if !json.Valid(p.Raw) || !bytes.Contains(p.Raw, []byte(`"prt_1"`)) {
			t.Fatalf("raw retention lost: %s", p.Raw)
		}
	})

	t.Run("tool part state", func(t *testing.T) {
		raw := `{"id":"evt_9","type":"message.part.updated","properties":{"sessionID":"ses_root","part":{"id":"prt_t1","sessionID":"ses_root","messageID":"msg_a1","type":"tool","callID":"call_1","tool":"bash","state":{"status":"completed","title":"ls","input":{"command":"ls"},"output":"file.txt"}}}}`
		ev, err := DecodeEvent([]byte(raw))
		if err != nil {
			t.Fatalf("DecodeEvent: %v", err)
		}
		if ev.MessagePartUpdated == nil {
			t.Fatal("part fact missing")
		}
		p := ev.MessagePartUpdated.Part
		if p.Tool != "bash" || p.CallID != "call_1" || p.State == nil {
			t.Fatalf("part = %+v", p)
		}
		if p.State.Status != "completed" || p.State.Output != "file.txt" || !json.Valid(p.State.Input) {
			t.Fatalf("state = %+v", p.State)
		}
	})

	t.Run("session id filled from properties when part omits it", func(t *testing.T) {
		raw := `{"id":"evt_10","type":"message.part.updated","properties":{"sessionID":"ses_root","part":{"id":"prt_2","messageID":"msg_u1","type":"text","text":"hi"}}}`
		ev, err := DecodeEvent([]byte(raw))
		if err != nil {
			t.Fatalf("DecodeEvent: %v", err)
		}
		if ev.MessagePartUpdated == nil {
			t.Fatal("part fact missing")
		}
		if got := ev.MessagePartUpdated.Part.SessionID; got != "ses_root" {
			t.Fatalf("SessionID = %q, want ses_root", got)
		}
	})
}

func TestDecodeEvent_UnknownKindIsNotAnError(t *testing.T) {
	for _, kind := range []string{"permission.updated", "session.created", "session.turn.started", "message.updated.v2", "question.asked"} {
		raw := `{"id":"evt_q","type":"` + kind + `","properties":{"sessionID":"ses_root","anything":true}}`
		ev, err := DecodeEvent([]byte(raw))
		if err != nil {
			t.Fatalf("DecodeEvent(%q): %v", kind, err)
		}
		if ev.Kind != kind || ev.ID != "evt_q" {
			t.Fatalf("envelope = %q/%q, want evt_q/%q", ev.ID, ev.Kind, kind)
		}
		if ev.SessionStatus != nil || ev.SessionIdle != nil || ev.SessionError != nil || ev.MessageUpdated != nil || ev.MessagePartUpdated != nil {
			t.Fatalf("kind %q carried facts", kind)
		}
	}
}

func TestDecodeEvent_MalformedKnownKindsAreErrors(t *testing.T) {
	raws := []string{
		`not json`,
		`{"type":"session.status"}`,
		`{"type":"session.status","properties":null}`,
		`{"type":"session.status","properties":[]}`,
		`{"type":"session.status","properties":{"sessionID":"ses_r","status":5}}`,
		`{"type":"session.idle","properties":[]}`,
		`{"type":"message.updated","properties":{"sessionID":"ses_r"}}`,
		`{"type":"message.updated","properties":{"sessionID":"ses_r","info":7}}`,
		`{"type":"message.part.updated","properties":{"sessionID":"ses_r"}}`,
		`{"type":"message.part.updated","properties":{"sessionID":"ses_r","part":null}}`,
		`{"type":"session.error","properties":{"sessionID":"ses_r"}}`,
		`{"id":7,"type":"session.idle","properties":{"sessionID":"ses_r"}}`,
	}
	for _, raw := range raws {
		if _, err := DecodeEvent([]byte(raw)); err == nil {
			t.Fatalf("DecodeEvent(%s) accepted malformed input", raw)
		}
	}
}

func TestDecodeStatusSnapshot(t *testing.T) {
	raw := `{"ses_a":{"type":"busy"},"ses_b":{"type":"retry","attempt":2,"message":"m","next":1000,"action":{"reason":"r","provider":"p","title":"t","message":"am","label":"l"}}}`
	snap, err := DecodeStatusSnapshot([]byte(raw))
	if err != nil {
		t.Fatalf("DecodeStatusSnapshot: %v", err)
	}
	if len(snap) != 2 || snap["ses_a"].Type != "busy" {
		t.Fatalf("snapshot = %+v", snap)
	}
	b := snap["ses_b"]
	if b.Type != "retry" || b.Attempt != 2 || b.Message != "m" || b.Next != 1000 {
		t.Fatalf("retry = %+v", b)
	}
	if b.Action == nil || b.Action.Label != "l" {
		t.Fatalf("action = %+v", b.Action)
	}

	empty, err := DecodeStatusSnapshot([]byte(`{}`))
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty snapshot = %+v, %v", empty, err)
	}
	for _, bad := range []string{`null`, `[]`, `not json`} {
		if _, err := DecodeStatusSnapshot([]byte(bad)); err == nil {
			t.Fatalf("DecodeStatusSnapshot(%s) accepted malformed input", bad)
		}
	}
}

func TestDecodeEventRejectsAmbiguousScopeAndUnboundedData(t *testing.T) {
	for _, raw := range []string{
		`null`, `{}`, `{"type":""}`, `{"type":"session.status","properties":{"sessionID":"ses_root"}}`,
		`{"type":"message.updated","properties":{"sessionID":"ses_root","info":{"id":"msg_u1","sessionID":"ses_other","role":"user","time":{"created":1}}}}`,
		`{"type":"message.part.updated","properties":{"sessionID":"ses_root","part":{"id":"prt_1","messageID":"msg_u1","sessionID":"ses_other","type":"text","text":"x"}}}`,
	} {
		if _, err := DecodeEvent([]byte(raw)); err == nil {
			t.Fatalf("accepted malformed scope/envelope: %s", raw)
		}
	}
	raw := []byte(`{"type":"message.part.updated","properties":{"sessionID":"ses_root","part":{"id":"prt_1","messageID":"msg_u1","sessionID":"ses_root","type":"text","text":"` + string(bytes.Repeat([]byte("x"), 2<<20)) + `"}}}`)
	if _, err := DecodeEvent(raw); err == nil {
		t.Fatal("unbounded raw event accepted")
	}
}
