package shim

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/shimwire"
	"golang.org/x/sys/unix"
)

type claudeNativeInitializer interface{ initializeNative() error }

type claudeA2Pipe struct {
	c             *claudeChatCapture
	input, output *os.File
}

func claudeA2PipeFixture(t *testing.T) *claudeA2Pipe {
	t.Helper()
	// Same pipe/capture/producer components, with immutable birth-shaped identity
	// installed BEFORE the first captured frame. No managed/native proof claimed.
	spool, _, _ := producerFixture(t)
	in, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, out, err := os.Pipe()
	if err != nil {
		_ = in.Close()
		_ = inW.Close()
		t.Fatal(err)
	}
	ends := []*os.File{in, inW, outR, out}
	t.Cleanup(func() {
		for _, file := range ends {
			_ = file.Close()
		}
	})
	for _, file := range []*os.File{in, inW, outR} {
		if err := claudePipeNonblock(file); err != nil {
			t.Fatal(err)
		}
	}
	started, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	identity := shimwire.ClaudePrimaryIdentity{SessionID: "native-a2-pipe", RequiredRuntime: "claude-chat", NativeSessionID: claudeChatTestUUID, ShimPID: os.Getpid(), ShimStartTime: started, PrimaryPID: os.Getpid(), PrimaryStartTime: started}
	srv := newServer(nil, "", nil, nil, nil, 0, time.Second, &Metrics{})
	c := newClaudeChatCapture(spool, srv, identity)
	c.stdin, c.stdout = inW, outR
	c.native, c.lifetimeLoss = true, &atomic.Bool{}
	t.Cleanup(func() { c.retireSignals(); close(c.queue); <-c.done; c.server.finishEscalation() })
	f := &claudeA2Pipe{c, in, out}
	return f
}
func (f *claudeA2Pipe) initializer(t *testing.T) claudeNativeInitializer {
	t.Helper()
	i, ok := any(f.c).(claudeNativeInitializer)
	if !ok {
		t.Fatal("APIabsence: native initialize writer not implemented")
	}
	return i
}
func (f *claudeA2Pipe) available(t *testing.T) []byte {
	t.Helper()
	var b [128 << 10]byte
	n, err := (claudePipeReader{file: f.input}).Read(b[:])
	if errors.Is(err, unix.EAGAIN) {
		return nil
	}
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	return append([]byte(nil), b[:n]...)
}
func (f *claudeA2Pipe) feed(t *testing.T, raw []byte) {
	t.Helper()
	if _, err := f.output.Write(append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
	var b [32 << 10]byte
	if _, err := f.c.consumeStdout(claudePipeReader{file: f.c.stdout}, b[:]); err != nil {
		t.Fatal(err)
	}
}
func (f *claudeA2Pipe) snapshot(t *testing.T) (*shimwire.ClaudePrimarySnapshot, string, uint64, bool) {
	t.Helper()
	p := f.c.primarySnapshot()
	if p == nil {
		t.Fatal("snapshot unavailable outside reader/write")
	}
	v := reflect.ValueOf(p).Elem()
	for name, tag := range map[string]string{"InitializeRequestID": "initialize_request_id", "InitializeResponseSequence": "initialize_response_sequence", "Initialized": "initialized"} {
		field, ok := v.Type().FieldByName(name)
		if !ok {
			t.Fatal("APIabsence: native initialize snapshot facts missing")
		}
		if field.Tag.Get("json") != tag {
			t.Fatalf("snapshot JSON contract for %s must be %s", name, tag)
		}
	}
	id, seq, ok := v.FieldByName("InitializeRequestID"), v.FieldByName("InitializeResponseSequence"), v.FieldByName("Initialized")
	if !id.IsValid() || id.Kind() != reflect.String || !seq.IsValid() || seq.Kind() != reflect.Uint64 || !ok.IsValid() || ok.Kind() != reflect.Bool {
		t.Fatal("APIabsence: native initialize snapshot facts missing")
	}
	return p, id.String(), seq.Uint(), ok.Bool()
}
func (f *claudeA2Pipe) initialize(t *testing.T) string {
	t.Helper()
	if p := f.c.primarySnapshot(); p == nil || p.ObservedSequence != 0 || p.DurableSequence != 0 || p.ReaderRevision != 0 {
		t.Fatal("positive initialize did not start before ANY stdout")
	}
	if err := f.initializer(t).initializeNative(); err != nil {
		t.Fatal(err)
	}
	line := f.available(t)
	if bytes.Count(line, []byte{'\n'}) != 1 || line[len(line)-1] != '\n' {
		t.Fatal("initialize was not exactly one LF line")
	}
	var got map[string]json.RawMessage
	if json.Unmarshal(line, &got) != nil || len(got) != 3 {
		t.Fatal("initialize body has invented fields")
	}
	var typ, id string
	var request map[string]string
	if json.Unmarshal(got["type"], &typ) != nil || typ != "control_request" || json.Unmarshal(got["request_id"], &id) != nil || id == "" || len(id) > 128 || json.Unmarshal(got["request"], &request) != nil || len(request) != 1 || request["subtype"] != "initialize" {
		t.Fatal("initialize wire shape is not exact")
	}
	_, snapshotID, seq, initialized := f.snapshot(t)
	if snapshotID != id || seq != 0 || initialized {
		t.Fatal("write alone manufactured initialized state")
	}
	return id
}
func (f *claudeA2Pipe) ack(t *testing.T, id, state string, mutate func(map[string]any)) []byte {
	t.Helper()
	v := map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": id, "response": map[string]any{"pid": f.c.identity.PrimaryPID, "claude_code_version": "2.1.310", "session_state": state}}}
	if mutate != nil {
		mutate(v)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func (f *claudeA2Pipe) ready(t *testing.T, state string) {
	t.Helper()
	id := f.initialize(t)
	f.feed(t, f.ack(t, id, state, nil))
	waitClaudePrimaryDurable(t, f.c, 1)
	p, got, seq, initialized := f.snapshot(t)
	if got != id || seq != 1 || !initialized || p.ObservedSequence != 1 || p.DurableSequence != 1 {
		t.Fatal("exact durable acknowledgement did not initialize")
	}
}
func (f *claudeA2Pipe) request(t *testing.T, expected, next uint64) shimwire.ClaudeMessageRequest {
	t.Helper()
	p := f.c.primarySnapshot()
	if p == nil {
		t.Fatal("request snapshot missing")
	}
	return shimwire.ClaudeMessageRequest{Version: 1, OperationID: "native-a2-owner", NativeUserUUID: "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a", Text: "owner\n雪\r\"", Identity: p.Identity, ExpectedOwnerRevision: expected, OwnerRevision: next, ThroughStreamSequence: p.ObservedSequence, ReaderRevision: p.ReaderRevision}
}

func TestClaudeNativeA2SystemInitCannotAuthorizeOwnerWrite(t *testing.T) {
	f := claudeA2PipeFixture(t)
	f.feed(t, []byte(`{"type":"system","subtype":"init"}`))
	waitClaudePrimaryDurable(t, f.c, 1) // Only this negative seeds system/init.
	if r := f.c.writeMessage(f.request(t, 0, 1)); r.Status != "refused" || len(f.available(t)) != 0 {
		t.Fatal("system/init alone allowed native user input")
	}
	if p := f.c.primarySnapshot(); p == nil || p.OwnerRevision != 0 {
		t.Fatal("preinitialize refusal changed delivered revision")
	}
}
func TestClaudeNativeA2OneInitializeNoPromptAndNoRetry(t *testing.T) {
	f := claudeA2PipeFixture(t)
	id := f.initialize(t)
	_ = f.initializer(t).initializeNative()
	if len(f.available(t)) != 0 {
		t.Fatal("initialize resent")
	}
	_, again, seq, initialized := f.snapshot(t)
	if again != id || seq != 0 || initialized {
		t.Fatal("repeat changed initialize identity/state")
	}
}
func TestClaudeNativeA2InitializeRequiresOwnDurableReply(t *testing.T) {
	f := claudeA2PipeFixture(t)
	id := f.initialize(t)
	old := hookSpoolSync
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	hookSpoolSync = func(file *os.File) error { enterOnce.Do(func() { close(entered) }); <-release; return old(file) }
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		closeWait := time.Now().Add(time.Second)
		for {
			f.c.mu.Lock()
			n := f.c.frames
			f.c.mu.Unlock()
			if n == 0 {
				break
			}
			if time.Now().After(closeWait) {
				t.Error("held initialize Append did not join")
				break
			}
			time.Sleep(time.Millisecond)
		}
		hookSpoolSync = old
	})
	f.feed(t, f.ack(t, id, "idle", nil))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("reply did not enter actual Append")
	}
	_, _, seq, initialized := f.snapshot(t)
	if seq != 0 || initialized {
		t.Fatal("initialize preceded durable reply")
	}
	releaseOnce.Do(func() { close(release) })
	waitClaudePrimaryDurable(t, f.c, 1)
	_, got, seq, initialized := f.snapshot(t)
	if got != id || seq != 1 || !initialized {
		t.Fatal("durable reply identity/sequence missing")
	}
}
func TestClaudeNativeA2InitializeRejectsWrongOrAmbiguousReply(t *testing.T) {
	for _, kind := range []string{"request-id", "pid", "version", "duplicate-id", "system-init-only"} {
		t.Run(kind, func(t *testing.T) {
			f := claudeA2PipeFixture(t)
			id := f.initialize(t)
			raw := f.ack(t, id, "idle", func(v map[string]any) {
				r := v["response"].(map[string]any)
				detail := r["response"].(map[string]any)
				switch kind {
				case "request-id":
					r["request_id"] = "foreign"
				case "pid":
					detail["pid"] = f.c.identity.PrimaryPID + 1
				case "version":
					detail["claude_code_version"] = "2.1.311"
				}
			})
			if kind == "duplicate-id" {
				raw = bytes.Replace(raw, []byte(`"request_id":`), []byte(`"request_id":"foreign","request_id":`), 1)
			}
			if kind == "system-init-only" {
				raw = []byte(`{"type":"system","subtype":"init"}`)
			}
			f.feed(t, raw)
			f.settled(t) // Reject after actual Append settlement, not an fsync race.
			_, _, seq, initialized := f.snapshot(t)
			if seq != 0 || initialized {
				t.Fatal("unqualified response initialized native input")
			}
			if r := f.c.writeMessage(f.request(t, 0, 1)); r.Status != "refused" || len(f.available(t)) != 0 {
				t.Fatal("wrong reply permitted user bytes")
			}
		})
	}
}
func TestClaudeNativeA2InitializeRejectsCaseAliases(t *testing.T) {
	for _, key := range []string{"type", "response", "subtype", "request_id", "pid", "claude_code_version", "session_state"} {
		t.Run(key, func(t *testing.T) {
			f := claudeA2PipeFixture(t)
			id := f.initialize(t)
			raw := f.ack(t, id, "idle", nil)
			// A matching alias cannot override an incorrect canonical authority field.
			needle, _ := json.Marshal(key)
			replacement := append(append([]byte(nil), needle...), ':')
			value := []byte(`"foreign"`)
			if key == "pid" {
				value = []byte("0")
			}
			if key == "response" {
				value = []byte("{}")
			}
			alias, _ := json.Marshal(strings.ToUpper(key))
			replacement = append(replacement, value...)
			replacement = append(replacement, ',')
			replacement = append(replacement, alias...)
			replacement = append(replacement, ':')
			raw = bytes.Replace(raw, append(needle, ':'), replacement, 1)
			f.feed(t, raw)
			f.settled(t)
			if _, _, _, initialized := f.snapshot(t); initialized {
				t.Fatal("case-aliased acknowledgement initialized input")
			}
			if r := f.c.writeMessage(f.request(t, 0, 1)); r.Status != "refused" || len(f.available(t)) != 0 {
				t.Fatal("case-aliased acknowledgement permitted owner bytes")
			}
		})
	}
}

func TestClaudeNativeA2BlockedStateIsNotInputReadiness(t *testing.T) {
	for _, state := range []string{"busy", "blocked", "waiting_for_permission", "unknown"} {
		t.Run(state, func(t *testing.T) {
			f := claudeA2PipeFixture(t)
			f.ready(t, state) // Handshake fact may exist while owner delivery stays blocked.
			if r := f.c.writeMessage(f.request(t, 0, 1)); r.Status != "refused" || len(f.available(t)) != 0 {
				t.Fatal("non-idle initialize state gained input authority")
			}
			if p := f.c.primarySnapshot(); p == nil || p.OwnerRevision != 0 {
				t.Fatal("blocked intent gained delivered credit")
			}
		})
	}
}
func TestClaudeNativeA2StringBodyAndGappedDeliveredRevision(t *testing.T) {
	f := claudeA2PipeFixture(t)
	f.ready(t, "idle")
	refused := f.request(t, 0, 1)
	refused.ReaderRevision++
	if r := f.c.writeMessage(refused); r.Status != "refused" || len(f.available(t)) != 0 {
		t.Fatal("wrong custody gained delivery")
	}
	r := f.request(t, 0, 3)
	if got := f.c.writeMessage(r); got.Status != "written" {
		t.Fatal("new durable intent gap was not delivered")
	}
	raw := f.available(t)
	var body map[string]json.RawMessage
	var msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	var typ, uuid string
	if bytes.Count(raw, []byte{'\n'}) != 1 || json.Unmarshal(raw, &body) != nil || len(body) != 3 || json.Unmarshal(body["type"], &typ) != nil || typ != "user" || json.Unmarshal(body["uuid"], &uuid) != nil || uuid != r.NativeUserUUID || json.Unmarshal(body["message"], &msg) != nil || msg.Role != "user" || msg.Content != r.Text {
		t.Fatal("native message is not exact string-content body")
	}
	if p := f.c.primarySnapshot(); p == nil || p.OwnerRevision != 3 {
		t.Fatal("full LF delivery did not advance delivered revision")
	}
	for _, old := range []shimwire.ClaudeMessageRequest{r, f.request(t, 3, 3), f.request(t, 0, 4)} {
		if got := f.c.writeMessage(old); got.Status != "refused" {
			t.Fatal("stale/duplicate delivery resent native input")
		}
	}
	if len(f.available(t)) != 0 {
		t.Fatal("stale delivery appended extra bytes")
	}
}
func TestClaudeNativeA2StopBeforeInitializeOrOwnerWritesNoBytes(t *testing.T) {
	for _, phase := range []string{"initialize", "owner"} {
		t.Run(phase, func(t *testing.T) {
			f := claudeA2PipeFixture(t)
			if phase == "owner" {
				f.ready(t, "idle")
			}
			f.c.server.onSignal(shimwire.SigKill)
			if phase == "initialize" {
				_ = f.initializer(t).initializeNative()
			} else {
				if r := f.c.writeMessage(f.request(t, 0, 1)); r.Status != "refused" {
					t.Fatal("stopped native owner write not refused")
				}
			}
			if len(f.available(t)) != 0 {
				t.Fatal("accepted Stop was overtaken by bytes")
			}
		})
	}
}
func TestClaudeNativeA2InitializeEPIPEHasNoRetry(t *testing.T) {
	f := claudeA2PipeFixture(t)
	i := f.initializer(t)
	_ = f.input.Close()
	if err := i.initializeNative(); err == nil {
		t.Fatal("EPIPE initialize looked delivered")
	}
	if err := i.initializeNative(); err == nil {
		t.Fatal("uncertain initialize was retried/accepted")
	}
	if p := f.c.primarySnapshot(); p == nil || p.Healthy || p.OwnerRevision != 0 {
		t.Fatal("initialize failure gained health/delivery")
	}
}

func TestClaudeNativeA2FailedReplyAppendNeverInitializes(t *testing.T) {
	f := claudeA2PipeFixture(t)
	id := f.initialize(t)
	old := hookSpoolSync
	hookSpoolSync = func(*os.File) error { return syscall.ENOSPC }
	t.Cleanup(func() {
		deadline := time.Now().Add(time.Second)
		for {
			f.c.mu.Lock()
			pending := f.c.frames
			f.c.mu.Unlock()
			if pending == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Error("failed Append did not join")
				break
			}
			time.Sleep(time.Millisecond)
		}
		hookSpoolSync = old
	})
	f.feed(t, f.ack(t, id, "idle", nil))
	deadline := time.Now().Add(time.Second)
	for {
		f.c.mu.Lock()
		pending := f.c.frames
		f.c.mu.Unlock()
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("actual failed Append did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	p, _, seq, initialized := f.snapshot(t)
	if seq != 0 || initialized || p.Healthy {
		t.Fatal("uncommitted initialize response gained authority")
	}
	if got := f.c.writeMessage(f.request(t, 0, 1)); got.Status != "refused" || len(f.available(t)) != 0 {
		t.Fatal("failed reply durability allowed user bytes")
	}
}

func (f *claudeA2Pipe) settled(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		f.c.mu.Lock()
		pending := f.c.frames
		f.c.mu.Unlock()
		if pending == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("primary Append did not settle")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestClaudeNativeA2InitializeResponseSequenceIsNotFixed(t *testing.T) {
	f := claudeA2PipeFixture(t)
	id := f.initialize(t)
	// Native baseline emits this startup hook pair before initialize ACK (seq3),
	// still BEFORE system/init. Feed real rows, never reset stream counters.
	for _, subtype := range []string{"hook_started", "hook_response"} {
		v := map[string]any{"type": "system", "subtype": subtype, "hook_id": "c6298480-f150-42a4-8cac-fe5daf172987", "hook_name": "SessionStart:startup", "hook_event": "SessionStart", "uuid": "9af3d029-622f-4690-a167-b9e950edc7b2", "session_id": f.c.identity.NativeSessionID}
		if subtype == "hook_response" {
			v["output"], v["stdout"], v["stderr"], v["exit_code"], v["outcome"] = "", "", "", 0, "success"
		}
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		f.feed(t, raw)
	}
	f.feed(t, f.ack(t, id, "idle", nil))
	waitClaudePrimaryDurable(t, f.c, 3)
	p, got, seq, initialized := f.snapshot(t)
	if got != id || seq != 3 || !initialized || p.ObservedSequence != 3 || p.DurableSequence != 3 {
		t.Fatal("initialize ACK sequence was hardcoded or startup metadata gained authority")
	}
}

func TestClaudeNativeA2DuplicateOrConflictingReplyBlocksInput(t *testing.T) {
	for _, kind := range []string{"same-reply", "conflicting-reply"} {
		t.Run(kind, func(t *testing.T) {
			f := claudeA2PipeFixture(t)
			f.ready(t, "idle")
			_, id, original, initialized := f.snapshot(t)
			if !initialized || original == 0 {
				t.Fatal("duplicate test lacks first settled valid ACK")
			}
			raw := f.ack(t, id, "idle", func(v map[string]any) {
				if kind == "conflicting-reply" {
					v["response"].(map[string]any)["response"].(map[string]any)["pid"] = f.c.identity.PrimaryPID + 1
				}
			})
			f.feed(t, raw)
			f.settled(t)
			p, got, seq, _ := f.snapshot(t)
			if got != id || seq != 0 && seq != original || p.Healthy {
				t.Fatal("second/conflicting ACK renewed or rebound initialize authority")
			}
			if r := f.c.writeMessage(f.request(t, 0, 1)); r.Status != "refused" || len(f.available(t)) != 0 {
				t.Fatal("duplicate/conflicting ACK permitted owner bytes")
			}
		})
	}
}
