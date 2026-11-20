package openailive

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const testKey = "sk-fixture-never-print"

const started = `{"type":"session.started","event_id":"evt_started","session":{"id":"live_123","model":"gpt-live-1","status":"active","audio":{"format":{"type":"audio/pcm","rate":24000},"output":{"voice":"marin"}},"delegation":{"type":"client"}}}`

func TestSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := fixture(t, func(ws *websocket.Conn) {
		checkCommand(t, ctx, ws, `{"type":"session.start","session":{"model":"gpt-live-1","instructions":"`+instructions+`","audio":{"format":{"type":"audio/pcm","rate":24000},"output":{"voice":"marin"}},"delegation":{"type":"client"}}}`)
		emit(t, ctx, ws, started)
		checkCommand(t, ctx, ws, `{"type":"session.input_audio.append","audio":"AQIDBA=="}`)
		checkCommand(t, ctx, ws, `{"type":"session.commentary.append","content":"The task is complete.","delegation_id":null}`)
		emit(t, ctx, ws, `{"type":"session.commentary.appended"}`)
		emit(t, ctx, ws, `{"type":"session.delegation.created","delegation":{"id":"del_abc","target":"client","type":"client"}}`)
		emit(t, ctx, ws, `{"type":"session.input_transcript.delta","delta":"Check the task."}`)
		emit(t, ctx, ws, `{"type":"session.output_transcript.delta","delta":"The task is complete."}`)
		emit(t, ctx, ws, `{"type":"session.output_audio.delta","delta":"AQIDBA=="}`)
		checkCommand(t, ctx, ws, `{"type":"session.close"}`)
		emit(t, ctx, ws, `{"type":"session.closed","reason":"close_requested","usage":{"seconds":1.2}}`)
	})

	session, err := open(ctx, url, testKey, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	if err := session.SendAudio(ctx, []byte{1}); err == nil {
		t.Fatal("accepted an incomplete PCM sample")
	}
	if err := session.SendAudio(ctx, []byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	if err := session.Speak(ctx, strings.Repeat("x", MaxCommentaryBytes+1)); err == nil {
		t.Fatal("accepted oversized commentary")
	}
	if err := session.Speak(ctx, "The task is complete."); err != nil {
		t.Fatal(err)
	}
	for _, want := range []Event{
		{Type: "session.commentary.appended"},
		{Type: "session.delegation.created"},
		{Type: "session.input_transcript.delta", Text: "Check the task."},
		{Type: "session.output_transcript.delta", Text: "The task is complete."},
		{Type: "session.output_audio.delta", Audio: []byte{1, 2, 3, 4}},
	} {
		got, err := session.Next(ctx)
		if err != nil || got.Type != want.Type || got.Text != want.Text || !bytes.Equal(got.Audio, want.Audio) {
			t.Fatalf("Next() = %+v, %v, want %+v", got, err, want)
		}
	}
	if err := session.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	if err := session.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	if err := session.SendAudio(ctx, []byte{1, 2}); err == nil {
		t.Fatal("accepted audio after Finish")
	}
	if got, err := session.Next(ctx); err != nil || got.Type != "session.closed" {
		t.Fatalf("closing event = %+v, %v", got, err)
	}
	if _, err := session.Next(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("Next() after session.closed = %v, want EOF", err)
	}
}

func TestSessionFailures(t *testing.T) {
	for _, tc := range []struct {
		name, start, next, want string
	}{
		{"startup rejection", `{"type":"error","error":{"message":"` + testKey + `"}}`, "", "rejected"},
		{"different audio", strings.Replace(started, "24000", "16000", 1), "", "startup"},
		{"truncated handshake", `{"type":"session.started"}`, "", "startup"},
		{"protocol error", started, `{"type":"error","error":{"message":"` + testKey + `"}}`, "rejected"},
		{"bad audio", started, `{"type":"session.output_audio.delta","delta":"not+base64?"}`, "audio"},
		{"oversized audio", started, `{"type":"session.output_audio.delta","delta":"` + base64.StdEncoding.EncodeToString(make([]byte, maxReceiveAudioBytes+2)) + `"}`, "audio"},
		{"oversized event", started, `{"type":"info","message":"` + strings.Repeat("x", maxEventBytes) + `"}`, "session.closed"},
		{"socket close", started, "", "session.closed"},
		{"upstream loss", started, `{"type":"session.closed","reason":"connection_lost"}`, "connection_lost"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			url := fixture(t, func(ws *websocket.Conn) {
				if _, _, err := ws.Read(ctx); err != nil {
					t.Error(err)
					return
				}
				emit(t, ctx, ws, tc.start)
				if tc.next != "" {
					emit(t, ctx, ws, tc.next)
				}
			})
			s, err := open(ctx, url, testKey, "marin")
			if s != nil {
				defer func() { _ = s.Close() }()
				_, err = s.Next(ctx)
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), testKey) {
				t.Fatalf("unexpected error %v, want %q with credentials redacted", err, tc.want)
			}
		})
	}
}

func TestOpenDoesNotFollowRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("followed redirect")
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	if _, err := open(context.Background(), "ws"+strings.TrimPrefix(origin.URL, "http"), testKey, ""); err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("open error = %v, want HTTP 307", err)
	}
}

func TestNextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	url := fixture(t, func(ws *websocket.Conn) {
		if _, _, err := ws.Read(ctx); err != nil {
			t.Error(err)
			return
		}
		emit(t, ctx, ws, started)
		_, _, _ = ws.Read(ctx)
	})
	session, err := open(ctx, url, testKey, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	readCtx, stop := context.WithTimeout(ctx, 10*time.Millisecond)
	defer stop()
	if _, err := session.Next(readCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Next() error = %v, want DeadlineExceeded", err)
	}
}

func fixture(t *testing.T, handle func(*websocket.Conn)) string {
	t.Helper()
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		if r.URL.RequestURI() != "/v1/live/sessions" || r.Header.Get("Authorization") != "Bearer "+testKey {
			t.Error("incorrect Live WebSocket path or authorization")
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = ws.CloseNow() }()
		handle(ws)
	}))
	t.Cleanup(func() {
		server.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("Live fixture handler did not exit")
		}
	})
	return "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/live/sessions"
}

func checkCommand(t *testing.T, ctx context.Context, ws *websocket.Conn, expected string) {
	t.Helper()
	kind, data, err := ws.Read(ctx)
	if err != nil {
		t.Error(err)
		return
	}
	var actual, want map[string]any
	if err := json.Unmarshal(data, &actual); err != nil {
		t.Error(err)
		return
	}
	if err := json.Unmarshal([]byte(expected), &want); err != nil {
		t.Error(err)
		return
	}
	actualJSON, _ := json.Marshal(actual)
	wantJSON, _ := json.Marshal(want)
	if kind != websocket.MessageText || !bytes.Equal(actualJSON, wantJSON) {
		t.Errorf("command = %s, want %s", actualJSON, wantJSON)
	}
}

func emit(t *testing.T, ctx context.Context, ws *websocket.Conn, event string) {
	t.Helper()
	if err := ws.Write(ctx, websocket.MessageText, []byte(event)); err != nil {
		t.Error(err)
	}
}

func TestSeededGreetingMatchesOneCorrelatedCommentary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	const greeting = "Say the synthetic line now, then pause."
	url := fixture(t, func(ws *websocket.Conn) {
		checkCommand(t, ctx, ws, `{"type":"session.start","session":{"model":"gpt-live-1","instructions":"`+instructions+`","audio":{"format":{"type":"audio/pcm","rate":24000},"output":{"voice":"marin"}},"delegation":{"type":"client"},"input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"`+greeting+`"}]}]}}`)
		emit(t, ctx, ws, started)
		_, raw, err := ws.Read(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		var command struct {
			Type       string          `json:"type"`
			Content    string          `json:"content"`
			EventID    string          `json:"event_id"`
			Delegation json.RawMessage `json:"delegation_id"`
		}
		if json.Unmarshal(raw, &command) != nil || command.Type != "session.commentary.append" ||
			command.Content != greeting || command.EventID == "" || string(command.Delegation) != "null" {
			t.Error("seeded greeting was not repeated with a unique correlation ID and null delegation")
			return
		}
		emit(t, ctx, ws, `{"type":"session.commentary.appended","client_event_id":"`+command.EventID+`"}`)
	})
	session, err := openSeeded(ctx, url, testKey, "marin", greeting)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	id, err := session.Greet(ctx, greeting)
	if err != nil {
		t.Fatal(err)
	}
	event, err := session.Next(ctx)
	if err != nil || event.Type != "session.commentary.appended" || event.ClientEventID != id {
		t.Fatalf("event = %+v, %v", event, err)
	}
}

func TestDispatcherSessionDelegationWire(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	url := fixture(t, func(ws *websocket.Conn) {
		if _, _, err := ws.Read(ctx); err != nil {
			t.Error(err)
			return
		}
		emit(t, ctx, ws, started)
		emit(t, ctx, ws, `{"type":"session.delegation.created","event_id":"other","delegation":{"id":"wrong","target":"responses"}}`)
		emit(t, ctx, ws, `{"type":"session.delegation.created","event_id":"del","offset_ms":123,"delegation":{"id":"item","target":"client"}}`)
		emit(t, ctx, ws, `{"type":"session.input_transcript.delta","event_id":"ascii","start_ms":80,"end_ms":140,"delta":"caf"}`)
		emit(t, ctx, ws, `{"type":"session.input_transcript.delta","event_id":"unicode","start_ms":140,"end_ms":160,"delta":"é\uD83D\uDE00"}`)
		_, data, err := ws.Read(ctx)
		var update map[string]string
		if err != nil || json.Unmarshal(data, &update) != nil || update["type"] != "session.commentary.append" ||
			update["delegation_id"] != "item" || update["event_id"] == "" || update["content"] != "Verified result." {
			t.Error("missing scoped delegation update")
		}
		emit(t, ctx, ws, `{"type":"session.commentary.appended"}`)
	})
	s, err := openConfigured(ctx, url, testKey, "", "", "Delegate each complete utterance.")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, want := range []Event{
		{Type: "session.delegation.created", EventID: "del", OffsetMS: 123, DelegationID: "item"},
		{Type: "session.input_transcript.delta", EventID: "ascii", StartMS: 80, EndMS: 140, Text: "caf"},
		{Type: "session.input_transcript.delta", EventID: "unicode", StartMS: 140, EndMS: 160, Text: "é😀"},
	} {
		got, err := s.Next(ctx)
		if err != nil || got.Type != want.Type || got.EventID != want.EventID || got.OffsetMS != want.OffsetMS ||
			got.DelegationID != want.DelegationID || got.StartMS != want.StartMS || got.EndMS != want.EndMS || got.Text != want.Text {
			t.Fatalf("wire event got %+v, %v, want %+v", got, err, want)
		}
	}
	if err := s.SpeakFor(ctx, "item", "Verified result."); err != nil {
		t.Fatal(err)
	}
	if ev, err := s.Next(ctx); err != nil || ev.Type != "session.commentary.appended" {
		t.Fatalf("commentary was not acknowledged, %v", err)
	}
}
