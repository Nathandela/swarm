package claudeinput

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func controlBroker(t *testing.T) (*Broker, Worker) {
	t.Helper()
	b, err := StartNativeControl(shortDir(t), "local-session", "2.1.310", 123, 456, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	w := Worker{NativeSessionID: "native-session", Generation: "observer", Version: "2.1.310"}
	heartbeat(t, b, w, "", 0, 0)
	return b, w
}
func controlHTTP(b *Broker, path string, raw []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://localhost"+path, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+b.Descriptor().Token)
	w := httptest.NewRecorder()
	b.serve(w, r)
	return w
}
func heartbeat(t *testing.T, b *Broker, w Worker, nonce string, captured uint64, queued int) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"generation": b.Descriptor().Generation, "worker": w, "nonce": nonce, "captured_sequence": captured, "queued": queued, "lost": false})
	r := controlHTTP(b, "/guard", raw)
	if r.Code != 200 {
		t.Fatalf("heartbeat: %d %s", r.Code, r.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(r.Body.Bytes(), &out)
	return out
}
func reportBytes(b *Broker, w Worker, seq uint64) []byte {
	raw, _ := json.Marshal(map[string]any{"generation": b.Descriptor().Generation, "worker": w, "sequence": seq, "invocation": seq, "event": "turn.step", "phase": "entered", "context": map[string]any{"turn_id": "turn", "index": 0, "message_count": 1, "origin": map[string]string{"plugin": "engine", "tier": "core"}}, "data": map[string]any{}})
	return raw
}
func TestNativeControlDurableAck(t *testing.T) {
	b, w := controlBroker(t)
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	if err := b.SetControlSink(func(e ControlEnvelope) (uint64, error) {
		if e.SessionID != "local-session" || e.Worker != w || e.Event != NativeControlRuntime || e.ReportHash == "" || e.Token != "" {
			t.Error("envelope was not stamped from broker identity")
		}
		close(entered)
		<-release
		return 17, nil
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- controlHTTP(b, "/control", reportBytes(b, w, 1)) }()
	select {
	case <-entered:
	case r := <-done:
		t.Fatalf("no durable sink before response: %d %s", r.Code, r.Body.String())
	case <-time.After(time.Second):
		t.Fatal("sink not called")
	}
	select {
	case <-done:
		t.Fatal("ACK preceded storage completion")
	default:
	}
	revoked := make(chan struct{})
	go func() { b.Revoke(); close(revoked) }()
	select {
	case <-revoked:
	case <-time.After(time.Second):
		t.Fatal("revocation blocked behind persistence")
	}
	close(release)
	r := <-done
	if r.Code != 200 {
		t.Fatalf("pre-Stop fact not retained: %d %s", r.Code, r.Body.String())
	}
	if r := controlHTTP(b, "/control", reportBytes(b, w, 2)); r.Code == 200 {
		t.Fatal("late ACK resurrected observation")
	}
}
func TestNativeControlExactRetry(t *testing.T) {
	b, w := controlBroker(t)
	var calls atomic.Int32
	_ = b.SetControlSink(func(ControlEnvelope) (uint64, error) { calls.Add(1); return 9, nil })
	raw := reportBytes(b, w, 1)
	first := controlHTTP(b, "/control", raw)
	second := controlHTTP(b, "/control", raw)
	if first.Code != 200 || second.Code != 200 || first.Body.String() != second.Body.String() || calls.Load() != 1 {
		t.Fatalf("lost ACK retry: first=%d second=%d appends=%d", first.Code, second.Code, calls.Load())
	}
}
func TestNativeControlMissingSink(t *testing.T) {
	b, w := controlBroker(t)
	raw := reportBytes(b, w, 1)
	if r := controlHTTP(b, "/control", raw); r.Code != 503 {
		t.Fatalf("missing sink should retain source head, got %d", r.Code)
	}
	_ = b.SetControlSink(func(ControlEnvelope) (uint64, error) { return 1, nil })
	if r := controlHTTP(b, "/control", raw); r.Code != 200 {
		t.Fatalf("same retained head after attachment: %d", r.Code)
	}
}
func TestNativeControlAppendFailure(t *testing.T) {
	b, w := controlBroker(t)
	var calls atomic.Int32
	_ = b.SetControlSink(func(ControlEnvelope) (uint64, error) { calls.Add(1); return 0, errors.New("ENOSPC") })
	if r := controlHTTP(b, "/control", reportBytes(b, w, 1)); r.Code == 200 {
		t.Fatal("failed append ACKed")
	}
	_ = controlHTTP(b, "/control", reportBytes(b, w, 1))
	if calls.Load() != 1 {
		t.Fatalf("failed feed not latched, calls=%d", calls.Load())
	}
}

func TestNativeControlLease(t *testing.T) {
	b, w := controlBroker(t)
	_ = b.SetControlSink(func(ControlEnvelope) (uint64, error) { return 1, nil })
	initial := heartbeat(t, b, w, "", 0, 0)
	if initial["live"] != false || initial["nonce"] == "" {
		t.Fatal("registration granted lease or no challenge")
	}
	nonce := initial["nonce"].(string)
	fresh := heartbeat(t, b, w, nonce, 0, 0)
	if fresh["live"] != true {
		t.Fatal("fresh caught-up response failed lease")
	}
	if fresh["sink_available"] != true || fresh["revoked"] != false || fresh["lease_remaining_ms"] == nil {
		t.Error("lease view lacks bound/state for delayed receiver")
	}
	delayed := heartbeat(t, b, w, "", 0, 0)
	b.mu.Lock()
	b.control.issued = time.Now().Add(-2 * time.Second)
	b.mu.Unlock()
	if view := heartbeat(t, b, w, delayed["nonce"].(string), 0, 0); view["live"] != true {
		t.Fatal("valid delayed echo not accepted")
	}
	b.mu.Lock()
	remaining := time.Until(b.control.leaseUntil)
	b.mu.Unlock()
	if remaining > 1100*time.Millisecond {
		t.Fatal("delayed echo rebased lease on receipt time", remaining)
	}
	if replay := heartbeat(t, b, w, nonce, 0, 0); replay["live"] != false {
		t.Fatal("replayed nonce renewed lease")
	}
	latest := heartbeat(t, b, w, "", 0, 0)
	b.mu.Lock()
	b.control.issued = time.Now().Add(-4 * time.Second)
	b.mu.Unlock()
	if late := heartbeat(t, b, w, latest["nonce"].(string), 0, 0); late["live"] != false {
		t.Fatal("first delayed echo renewed lease")
	}
	queued := heartbeat(t, b, w, "", 1, 1)
	if next := heartbeat(t, b, w, queued["nonce"].(string), 1, 1); next["live"] != false {
		t.Fatal("queued undurable fact granted lease")
	}
	if r := controlHTTP(b, "/control", reportBytes(b, w, 1)); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	caught := heartbeat(t, b, w, "", 1, 0)
	if next := heartbeat(t, b, w, caught["nonce"].(string), 1, 0); next["live"] != true {
		t.Fatal("fresh caught-up feed not live")
	}
	b.mu.Lock()
	b.control.leaseUntil = time.Now().Add(-time.Second)
	b.mu.Unlock()
	raw, _ := json.Marshal(map[string]string{"generation": b.Descriptor().Generation})
	read := controlHTTP(b, "/observation", raw)
	var view ObservationLease
	_ = json.Unmarshal(read.Body.Bytes(), &view)
	if view.Live {
		t.Fatal("expired ephemeral lease still live")
	}
	b.Revoke()
	if r := controlHTTP(b, "/guard", mustJSON(map[string]any{"generation": b.Descriptor().Generation, "worker": w, "nonce": caught["nonce"], "captured_sequence": 1, "queued": 0, "lost": false})); r.Code == 200 {
		t.Fatal("Stop late echo rearmed epoch")
	}
}
func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

func TestNativeControlSequenceConflict(t *testing.T) {
	for _, kind := range []string{"gap", "conflict", "wrong-terminal", "changed-witness"} {
		t.Run(kind, func(t *testing.T) {
			b, w := controlBroker(t)
			var calls atomic.Int32
			_ = b.SetControlSink(func(ControlEnvelope) (uint64, error) { return uint64(calls.Add(1)), nil })
			first := reportBytes(b, w, 1)
			if r := controlHTTP(b, "/control", first); r.Code != 200 {
				t.Fatal(r.Code, r.Body.String())
			}
			var body map[string]any
			_ = json.Unmarshal(first, &body)
			switch kind {
			case "gap":
				body["sequence"] = 3
				body["invocation"] = 3
			case "conflict":
				body["context"].(map[string]any)["message_count"] = 2
			case "wrong-terminal":
				body["sequence"] = 2
				body["phase"] = "threw"
				body["context"].(map[string]any)["turn_id"] = "foreign"
			case "changed-witness":
				body["sequence"] = 2
				body["phase"] = "returned"
				body["data"] = map[string]any{"turn_id": "foreign", "index": 0, "stop_reason": nil, "tool_uses": []any{}}
			}
			if r := controlHTTP(b, "/control", mustJSON(body)); r.Code == 200 {
				t.Fatal("conflict ACKed")
			}
			if calls.Load() != 1 {
				t.Fatal("conflict reached storage", calls.Load())
			}
		})
	}
}
func TestNativeControlReportValidation(t *testing.T) {
	for _, kind := range []string{"case", "duplicate", "null", "unknown", "bad-json", "trailing", "wrong-generation", "wrong-native", "wrong-epoch", "body", "missing-origin", "false-main", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			b, w := controlBroker(t)
			var calls atomic.Int32
			_ = b.SetControlSink(func(ControlEnvelope) (uint64, error) { calls.Add(1); return 1, nil })
			raw := reportBytes(b, w, 1)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			switch kind {
			case "case":
				raw = bytes.Replace(raw, []byte(`"sequence"`), []byte(`"Sequence"`), 1)
			case "duplicate":
				raw = append([]byte(`{"sequence":1,`), raw[1:]...)
			case "null":
				body["context"].(map[string]any)["agent_id"] = nil
				raw = mustJSON(body)
			case "unknown":
				body["extra"] = true
				raw = mustJSON(body)
			case "bad-json":
				raw = []byte("{")
			case "trailing":
				raw = append(raw, []byte("{}")...)
			case "wrong-generation":
				body["generation"] = "foreign"
				raw = mustJSON(body)
			case "wrong-native":
				body["worker"].(map[string]any)["native_session_id"] = "foreign"
				raw = mustJSON(body)
			case "wrong-epoch":
				body["worker"].(map[string]any)["generation"] = "foreign"
				raw = mustJSON(body)
			case "body":
				body["data"].(map[string]any)["answer"] = "SECRET"
				raw = mustJSON(body)
			case "missing-origin":
				delete(body["context"].(map[string]any), "origin")
				raw = mustJSON(body)
			case "false-main":
				body["context"].(map[string]any)["agent_id"] = ""
				raw = mustJSON(body)
			case "oversize":
				raw = bytes.Repeat([]byte(" "), maxControlBytes+1)
			}
			if r := controlHTTP(b, "/control", raw); r.Code == 200 {
				t.Fatal("invalid metadata ACKed")
			}
			if calls.Load() != 0 {
				t.Fatal("invalid metadata reached spool")
			}
		})
	}
}

type blockedControlWriter struct {
	header           http.Header
	entered, release chan struct{}
}

func (w *blockedControlWriter) Header() http.Header         { return w.header }
func (w *blockedControlWriter) WriteHeader(int)             { close(w.entered); <-w.release }
func (w *blockedControlWriter) Write(p []byte) (int, error) { return len(p), nil }
func TestNativeControlRevocationDuringResponse(t *testing.T) {
	b, w := controlBroker(t)
	raw := mustJSON(map[string]any{"generation": b.Descriptor().Generation, "worker": w, "captured_sequence": 0, "queued": 0, "lost": false})
	req := httptest.NewRequest("POST", "http://localhost/guard", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+b.Descriptor().Token)
	writer := &blockedControlWriter{header: http.Header{}, entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() { b.serve(writer, req); close(done) }()
	select {
	case <-writer.entered:
	case <-time.After(time.Second):
		t.Fatal("response write not entered")
	}
	revoked := make(chan struct{})
	go func() { b.Revoke(); close(revoked) }()
	select {
	case <-revoked:
	case <-time.After(time.Second):
		close(writer.release)
		<-done
		t.Fatal("revocation blocked on response write")
	}
	close(writer.release)
	<-done
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.control.nonce != "" || !b.control.leaseUntil.IsZero() {
		t.Fatal("late response rearmed nonce")
	}
}

func TestNativeControlSenderRequiresObservationLease(t *testing.T) {
	b, err := StartNativeControl(shortDir(t), "local-session", "2.1.310", 123, 456, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	guard := Worker{NativeSessionID: "native-session", Generation: "guard", Version: "2.1.310"}
	sender := guard
	sender.Generation = "sender"
	heartbeat(t, b, guard, "", 0, 0)
	client := NewClient(b.Descriptor())
	if _, err := client.Exchange(context.Background(), sender, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Ready(context.Background()); err == nil {
		t.Error("sender ready before existing spool attached")
	}
	_ = b.SetControlSink(func(ControlEnvelope) (uint64, error) { return 1, nil })
	if _, err := client.Ready(context.Background()); err == nil {
		t.Error("sender ready before fresh caught-up observation lease")
	}
	initial := heartbeat(t, b, guard, "", 0, 0)
	heartbeat(t, b, guard, initial["nonce"].(string), 0, 0)
	if _, err := client.Ready(context.Background()); err != nil {
		t.Fatal("caught-up sender unavailable", err)
	}
}

func TestNativeControlSpawnPhaseValidation(t *testing.T) {
	for _, kind := range []string{"entered-actor", "entered-denied", "returned-empty", "returned-both", "returned-denied-false", "question-count"} {
		t.Run(kind, func(t *testing.T) {
			b, w := controlBroker(t)
			var calls atomic.Int32
			_ = b.SetControlSink(func(ControlEnvelope) (uint64, error) { calls.Add(1); return 1, nil })
			qualifiers := map[string]any{"background": true, "fork": false, "subagent_type": "general-purpose", "provider": map[string]string{"plugin": "engine", "tier": "core"}, "is_teammate_present": false, "workflow_present": false, "resume_present": false, "name_present": false}
			report := map[string]any{"generation": b.Descriptor().Generation, "worker": w, "sequence": 1, "invocation": 1, "event": "agent.spawn", "phase": "entered", "context": map[string]any{"tool_use_id": "call", "origin": map[string]string{"plugin": "engine", "tier": "core"}}, "data": qualifiers}
			switch kind {
			case "entered-actor":
				qualifiers["agent_id"] = "invented"
			case "entered-denied":
				qualifiers["denied"] = true
			case "question-count":
				report["event"] = "ui.render"
				report["context"] = map[string]any{"origin": map[string]string{"plugin": "engine", "tier": "core"}}
				report["data"] = map[string]any{"request_id": "call", "question_count": 129, "component": "AskUserQuestion", "surface": "terminal"}
			default:
				if r := controlHTTP(b, "/control", mustJSON(report)); r.Code != 200 {
					t.Fatal("valid spawn entry refused", r.Body.String())
				}
				calls.Store(0)
				report["sequence"] = 2
				report["phase"] = "returned"
				if kind == "returned-both" {
					qualifiers["denied"] = true
					qualifiers["agent_id"] = "invented"
				}
				if kind == "returned-denied-false" {
					qualifiers["denied"] = false
				}
			}
			if r := controlHTTP(b, "/control", mustJSON(report)); r.Code == 200 {
				t.Fatal("invalid phase shape ACKed")
			}
			if calls.Load() != 0 {
				t.Fatal("invalid phase shape reached sink")
			}
		})
	}
}
