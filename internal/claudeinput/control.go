package claudeinput

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const maxControlBytes = 8 << 10
const maxControlContexts = 128

// ControlEnvelope is stamped by the authenticated shim broker, not the mod.
type ControlEnvelope struct {
	Event                string          `json:"event"`
	RequiredRuntime      string          `json:"required_runtime"`
	Version              int             `json:"version"`
	SessionID            string          `json:"session_id"`
	Token                string          `json:"token"`
	DescriptorGeneration string          `json:"descriptor_generation"`
	ShimPID              int             `json:"shim_pid"`
	ShimStartTime        int64           `json:"shim_start_time"`
	Worker               Worker          `json:"worker"`
	Sequence             uint64          `json:"sequence"`
	ReportHash           string          `json:"report_hash"`
	Invocation           uint64          `json:"invocation"`
	NativeEvent          string          `json:"native_event"`
	Phase                string          `json:"phase"`
	Context              json.RawMessage `json:"context"`
	Data                 json.RawMessage `json:"data"`
}

type controlReport struct {
	Generation string          `json:"generation"`
	Worker     Worker          `json:"worker"`
	Sequence   uint64          `json:"sequence"`
	Invocation uint64          `json:"invocation"`
	Event      string          `json:"event"`
	Phase      string          `json:"phase"`
	Context    json.RawMessage `json:"context"`
	Data       json.RawMessage `json:"data"`
}
type controlAck struct {
	Generation       string `json:"generation"`
	WorkerGeneration string `json:"worker_generation"`
	Sequence         uint64 `json:"sequence"`
	ReportHash       string `json:"report_hash"`
	SpoolSequence    uint64 `json:"spool_sequence"`
}
type controlState struct {
	sink       func(ControlEnvelope) (uint64, error)
	last       controlAck
	open       map[uint64]controlReport
	captured   uint64
	queued     int
	nonce      string
	issued     time.Time
	leaseUntil time.Time
}
type controlHeartbeat struct {
	Generation string `json:"generation"`
	Worker     Worker `json:"worker"`
	State      string `json:"state,omitempty"`
	Nonce      string `json:"nonce,omitempty"`
	Captured   uint64 `json:"captured_sequence"`
	Queued     int    `json:"queued"`
	Lost       bool   `json:"lost"`
}

// ObservationLease is ephemeral evidence only, never input or effect authority.
type ObservationLease struct {
	Generation    string `json:"generation"`
	Worker        Worker `json:"worker"`
	Captured      uint64 `json:"captured_sequence"`
	Durable       uint64 `json:"durable_sequence"`
	Live          bool   `json:"live"`
	Nonce         string `json:"nonce,omitempty"`
	Revoked       bool   `json:"revoked"`
	SinkAvailable bool   `json:"sink_available"`
	Queued        int    `json:"queued"`
	GuardAgeMS    int64  `json:"guard_age_ms"`
	// The receiver subtracts its full round trip; never rebase expiry on receipt.
	LeaseRemainingMS int64 `json:"lease_remaining_ms"`
}

// SetControlSink attaches the existing post-PTY durable spool once.
func (b *Broker) SetControlSink(sink func(ControlEnvelope) (uint64, error)) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.descriptor.RequiredRuntime != NativeControlRuntime || b.revoked || sink == nil || b.control.sink != nil {
		return errors.New("claude control sink unavailable")
	}
	b.control.sink = sink
	return nil
}

// Revoke retires authority without waiting behind storage or network writes.
func (b *Broker) Revoke() { b.mu.Lock(); defer b.mu.Unlock(); b.revokeLocked() }

func (b *Broker) controlWorker(w Worker) bool {
	return w.NativeSessionID != "" && len(w.NativeSessionID) <= 128 && w.Generation != "" && len(w.Generation) <= 128 && w.Version == "2.1.310" && (b.guard.Generation == "" || b.guard == w) && (b.worker.Generation == "" || b.worker.NativeSessionID == w.NativeSessionID && b.worker.Version == w.Version)
}

func (b *Broker) observationLocked(now time.Time) ObservationLease {
	s := &b.control
	live := !b.revoked && s.sink != nil && b.guard.Generation != "" && s.captured == s.last.Sequence && s.queued == 0 && now.Before(s.leaseUntil)
	remaining := int64(0)
	if live {
		remaining = s.leaseUntil.Sub(now).Milliseconds()
	}
	age := int64(-1)
	if !b.guardSeen.IsZero() {
		age = now.Sub(b.guardSeen).Milliseconds()
	}
	return ObservationLease{Generation: b.descriptor.Generation, Worker: b.guard, Captured: s.captured, Durable: s.last.Sequence, Live: live && remaining > 0, Revoked: b.revoked, SinkAvailable: s.sink != nil, Queued: s.queued, GuardAgeMS: age, LeaseRemainingMS: remaining}
}
func writeControl(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func (b *Broker) refuseControl(w http.ResponseWriter) {
	b.Revoke()
	http.Error(w, "claude control feed refused", http.StatusConflict)
}

func (b *Broker) serveControl(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxControlBytes+1))
	if err != nil || len(raw) > maxControlBytes {
		b.refuseControl(w)
		return
	}
	if r.URL.Path == "/guard" {
		var h controlHeartbeat
		_, fieldsOK := controlFields(raw, "generation worker state nonce captured_sequence queued lost", "generation worker captured_sequence queued lost")
		if !fieldsOK || strictControlJSON(raw, &h) != nil || h.Generation != b.descriptor.Generation || h.Queued < 0 || h.Queued > 128 || h.Captured > 1<<53-1 {
			b.refuseControl(w)
			return
		}
		b.mu.Lock()
		if b.revoked || !b.controlWorker(h.Worker) || h.Lost || h.State != "" && h.State != "revoked" {
			b.revokeLocked()
			b.mu.Unlock()
			http.Error(w, "claude control guard refused", http.StatusConflict)
			return
		}
		b.guard = h.Worker
		if h.State == "revoked" {
			b.revokeLocked()
			b.mu.Unlock()
			writeControl(w, 200, nil)
			return
		}
		s := &b.control
		now := time.Now()
		s.leaseUntil = time.Time{}
		if h.Nonce != "" && h.Nonce == s.nonce && now.Before(s.issued.Add(3*time.Second)) && h.Captured >= s.captured && h.Captured == s.last.Sequence && h.Queued == 0 && s.sink != nil {
			s.leaseUntil = s.issued.Add(3 * time.Second)
		}
		if h.Captured > s.captured {
			s.captured = h.Captured
		}
		s.queued = h.Queued
		var nonce [16]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			b.revokeLocked()
			b.mu.Unlock()
			http.Error(w, "claude control nonce unavailable", http.StatusServiceUnavailable)
			return
		}
		s.nonce = hex.EncodeToString(nonce[:])
		s.issued = now
		out := b.observationLocked(now)
		out.Nonce = s.nonce
		b.guardSeen = now
		b.mu.Unlock()
		writeControl(w, 200, out)
		return
	}
	if r.URL.Path == "/observation" {
		var req struct {
			Generation string `json:"generation"`
		}
		if strictControlJSON(raw, &req) != nil || req.Generation != b.descriptor.Generation {
			b.refuseControl(w)
			return
		}
		b.mu.Lock()
		out := b.observationLocked(time.Now())
		b.mu.Unlock()
		writeControl(w, 200, out)
		return
	}
	var report controlReport
	if strictControlJSON(raw, &report) != nil || report.Generation != b.descriptor.Generation || !validControlReport(report) {
		b.refuseControl(w)
		return
	}
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	b.controlAppend.Lock()
	defer b.controlAppend.Unlock()
	b.mu.Lock()
	s := &b.control
	if b.revoked || !b.controlWorker(report.Worker) || b.guard != report.Worker {
		b.revokeLocked()
		b.mu.Unlock()
		http.Error(w, "claude control identity retired", http.StatusConflict)
		return
	}
	if report.Sequence == s.last.Sequence && hash == s.last.ReportHash {
		out := s.last
		b.mu.Unlock()
		writeControl(w, 200, out)
		return
	}
	if report.Sequence != s.last.Sequence+1 {
		b.revokeLocked()
		b.mu.Unlock()
		http.Error(w, "claude control sequence conflict", http.StatusConflict)
		return
	}
	if report.Phase == "entered" {
		if len(s.open) >= maxControlContexts {
			b.revokeLocked()
			b.mu.Unlock()
			http.Error(w, "claude control context capacity", http.StatusConflict)
			return
		}
	} else {
		entry, ok := s.open[report.Invocation]
		if !ok || entry.Event != report.Event || !bytes.Equal(entry.Context, report.Context) || !sameControlWitness(entry, report) {
			b.revokeLocked()
			b.mu.Unlock()
			http.Error(w, "claude control invocation conflict", http.StatusConflict)
			return
		}
	}
	sink := s.sink
	if sink == nil {
		b.mu.Unlock()
		http.Error(w, "claude control spool unavailable", http.StatusServiceUnavailable)
		return
	}
	// This exact live witness survives a later Stop solely to retain its fact.
	s.leaseUntil = time.Time{}
	if report.Sequence > s.captured {
		s.captured = report.Sequence
	}
	envelope := ControlEnvelope{Event: NativeControlRuntime, RequiredRuntime: NativeControlRuntime, Version: 1, SessionID: b.descriptor.SessionID, DescriptorGeneration: b.descriptor.Generation, ShimPID: b.descriptor.ShimPID, ShimStartTime: b.descriptor.ShimStartTime, Worker: report.Worker, Sequence: report.Sequence, ReportHash: hash, Invocation: report.Invocation, NativeEvent: report.Event, Phase: report.Phase, Context: report.Context, Data: report.Data}
	b.mu.Unlock()
	seq, err := sink(envelope)
	b.mu.Lock()
	if err != nil || seq == 0 {
		b.revokeLocked()
		b.mu.Unlock()
		http.Error(w, "claude control spool failed", http.StatusConflict)
		return
	}
	out := controlAck{Generation: b.descriptor.Generation, WorkerGeneration: report.Worker.Generation, Sequence: report.Sequence, ReportHash: hash, SpoolSequence: seq}
	s.last = out
	if report.Phase == "entered" {
		if s.open == nil {
			s.open = map[uint64]controlReport{}
		}
		s.open[report.Invocation] = report
	} else {
		delete(s.open, report.Invocation)
	}
	b.mu.Unlock()
	writeControl(w, 200, out)
}

// Reject ambiguous keys at every nesting level before typed decoding. Null is
// meaningful only for a returned native stop_reason; everything else is absent.
func strictControlJSON(raw []byte, out any) error {
	if !utf8.Valid(raw) {
		return errors.New("invalid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(int, string) error
	walk = func(depth int, key string) error {
		if depth > 10 {
			return errors.New("control depth")
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		if t == nil && key != "stop_reason" {
			return errors.New("null control field")
		}
		if delimiter, ok := t.(json.Delim); ok {
			switch delimiter {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					k, e := d.Token()
					if e != nil {
						return e
					}
					name, ok := k.(string)
					if !ok || name != strings.ToLower(name) || seen[name] {
						return errors.New("ambiguous control key")
					}
					seen[name] = true
					if e = walk(depth+1, name); e != nil {
						return e
					}
				}
			case '[':
				for d.More() {
					if e := walk(depth+1, ""); e != nil {
						return e
					}
				}
			default:
				return errors.New("invalid control delimiter")
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := walk(0, ""); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing control JSON")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(out)
}

func controlFields(raw json.RawMessage, allowed, required string) (map[string]json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil || m == nil {
		return nil, false
	}
	for k := range m {
		if !strings.Contains(" "+allowed+" ", " "+k+" ") {
			return nil, false
		}
	}
	for _, k := range strings.Fields(required) {
		if _, ok := m[k]; !ok {
			return nil, false
		}
	}
	return m, true
}
func controlString(raw json.RawMessage, max int) bool {
	var s string
	return json.Unmarshal(raw, &s) == nil && s != "" && len(s) <= max && !strings.ContainsAny(s, "\x00\r\n")
}
func controlInteger(raw json.RawMessage) bool {
	var n uint64
	return json.Unmarshal(raw, &n) == nil && n <= 1<<53-1
}
func controlBool(raw json.RawMessage) bool {
	var b bool
	return json.Unmarshal(raw, &b) == nil && string(raw) != "null"
}
func controlEnum(raw json.RawMessage, values string) bool {
	var s string
	return json.Unmarshal(raw, &s) == nil && strings.Contains(" "+values+" ", " "+s+" ")
}
func controlUses(raw json.RawMessage) bool {
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil || len(entries) > 128 || string(raw) == "null" {
		return false
	}
	for _, v := range entries {
		m, ok := controlFields(v, "id name", "id name")
		if !ok || !controlString(m["id"], 128) || !controlString(m["name"], 256) {
			return false
		}
	}
	return true
}
func controlOrigin(raw json.RawMessage) bool {
	m, ok := controlFields(raw, "plugin tier", "plugin tier")
	return ok && controlString(m["plugin"], 128) && controlEnum(m["tier"], "prepend user append builtin core")
}

func validControlReport(r controlReport) bool {
	if r.Sequence == 0 || r.Sequence > 1<<53-1 || r.Invocation == 0 || r.Invocation > r.Sequence || r.Phase != "entered" && r.Phase != "returned" && r.Phase != "threw" || r.Phase == "entered" && r.Invocation != r.Sequence {
		return false
	}
	c, ok := controlFields(r.Context, "turn_id agent_id index message_count origin origin_unknown tool tool_use_id context_unknown parent_agent_id", " ")
	if !ok {
		return false
	}
	for k, v := range c {
		switch k {
		case "index", "message_count":
			if !controlInteger(v) {
				return false
			}
		case "origin":
			if !controlOrigin(v) {
				return false
			}
		case "origin_unknown", "context_unknown":
			if string(v) != "true" {
				return false
			}
		case "turn_id", "agent_id", "tool_use_id", "parent_agent_id":
			if !controlString(v, 128) {
				return false
			}
		default:
			if !controlString(v, 256) {
				return false
			}
		}
	}
	if _, ok = c["origin"]; ok {
		if _, unknown := c["origin_unknown"]; unknown {
			return false
		}
	} else if string(c["origin_unknown"]) != "true" {
		return false
	}
	contextKeys := "origin origin_unknown"
	if r.Event != "agent.spawn" && r.Event != "ui.render" {
		contextKeys += " agent_id"
	}
	switch r.Event {
	case "turn.start", "turn.complete":
		contextKeys += " turn_id"
	case "turn.step":
		contextKeys += " turn_id index message_count"
	case "tool.call", "tool.check":
		contextKeys += " tool tool_use_id turn_id index context_unknown"
	case "agent.spawn":
		contextKeys += " tool_use_id parent_agent_id"
	}
	for key := range c {
		if !strings.Contains(" "+contextKeys+" ", " "+key+" ") {
			return false
		}
	}
	d, ok := controlFields(r.Data, "turn_id index stop_reason tool_uses server_tool_uses reason is_aborted approver decision background fork subagent_type provider is_teammate_present workflow_present resume_present name_present resume_agent_id agent_id teammate_id denied is_error agent_status agent_result_unknown request_id question_count component surface", "")
	if !ok {
		return false
	}
	allowed := ""
	switch r.Event {
	case "turn.start":
		if !controlString(c["turn_id"], 128) {
			return false
		}
		if r.Phase == "returned" {
			allowed = "turn_id"
			if !bytes.Equal(c["turn_id"], d["turn_id"]) {
				return false
			}
		}
	case "turn.step":
		if !controlString(c["turn_id"], 128) || !controlInteger(c["index"]) || !controlInteger(c["message_count"]) {
			return false
		}
		if r.Phase == "returned" {
			allowed = "turn_id index stop_reason tool_uses server_tool_uses"
			if !bytes.Equal(c["turn_id"], d["turn_id"]) || !bytes.Equal(c["index"], d["index"]) || !controlUses(d["tool_uses"]) || len(d["stop_reason"]) == 0 {
				return false
			}
			if string(d["stop_reason"]) != "null" && !controlEnum(d["stop_reason"], "end_turn max_tokens stop_sequence tool_use pause_turn compaction refusal model_context_window_exceeded") {
				return false
			}
		}
	case "turn.complete":
		if !controlString(c["turn_id"], 128) {
			return false
		}
		if r.Phase != "threw" {
			allowed = "reason is_aborted"
			if !controlEnum(d["reason"], "answer aborted refusal error") || !controlBool(d["is_aborted"]) {
				return false
			}
		}
	case "tool.call", "tool.check":
		if !controlString(c["tool"], 256) {
			return false
		}
		if r.Event == "tool.call" && !controlString(c["tool_use_id"], 128) {
			return false
		}
		if string(c["context_unknown"]) == "true" {
			if len(c["turn_id"]) != 0 || len(c["index"]) != 0 {
				return false
			}
		} else {
			var o struct {
				Plugin string `json:"plugin"`
				Tier   string `json:"tier"`
			}
			_ = json.Unmarshal(c["origin"], &o)
			if !controlString(c["turn_id"], 128) || !controlString(c["tool_use_id"], 128) || o.Plugin != "engine" || o.Tier != "core" {
				return false
			}
		}
		if r.Event == "tool.check" {
			if r.Phase != "threw" {
				allowed = "approver"
			}
			if r.Phase == "returned" {
				allowed += " decision"
			}
			if r.Phase == "returned" && !controlEnum(d["decision"], "allow ask deny") {
				return false
			}
		} else if r.Phase == "returned" {
			allowed = "is_error denied"
			if string(c["tool"]) == `"Agent"` {
				allowed += " agent_status agent_id agent_result_unknown"
			}
		}
	case "agent.spawn":
		if !controlString(c["tool_use_id"], 128) {
			return false
		}
		if r.Phase != "threw" {
			allowed = "background fork subagent_type provider is_teammate_present workflow_present resume_present name_present resume_agent_id"
			if r.Phase == "returned" {
				allowed += " agent_id teammate_id denied"
				_, hasActor := d["agent_id"]
				_, hasDenied := d["denied"]
				if hasActor == hasDenied || hasDenied && string(d["denied"]) != "true" {
					return false
				}
				if hasDenied && len(d["teammate_id"]) != 0 {
					return false
				}
			}
			for _, k := range []string{"background", "fork", "is_teammate_present", "workflow_present", "resume_present", "name_present"} {
				if !controlBool(d[k]) {
					return false
				}
			}
			if !controlString(d["subagent_type"], 256) || !controlOrigin(d["provider"]) {
				return false
			}
			if (string(d["resume_present"]) == "true") != (len(d["resume_agent_id"]) > 0) {
				return false
			}
		}
	case "ui.render":
		allowed = "request_id question_count component surface"
		if !controlString(d["request_id"], 128) || !controlInteger(d["question_count"]) || string(d["component"]) != `"AskUserQuestion"` || string(d["surface"]) != `"terminal"` {
			return false
		}
		var count uint64
		_ = json.Unmarshal(d["question_count"], &count)
		if count < 1 || count > 128 {
			return false
		}
	default:
		return false
	}
	for k, v := range d {
		if !strings.Contains(" "+allowed+" ", " "+k+" ") {
			return false
		}
		switch k {
		case "turn_id", "request_id", "agent_id", "teammate_id", "resume_agent_id":
			if !controlString(v, 128) {
				return false
			}
		case "subagent_type":
			if !controlString(v, 256) {
				return false
			}
		case "approver":
			if !controlEnum(v, "host person") {
				return false
			}
		case "decision":
			if !controlEnum(v, "allow ask deny") {
				return false
			}
		case "agent_status":
			if !controlEnum(v, "completed async_launched remote_launched") {
				return false
			}
		case "index", "question_count":
			if !controlInteger(v) {
				return false
			}
		case "tool_uses", "server_tool_uses":
			if !controlUses(v) {
				return false
			}
		case "is_aborted", "background", "fork", "is_teammate_present", "workflow_present", "resume_present", "name_present", "denied", "is_error", "agent_result_unknown":
			if !controlBool(v) {
				return false
			}
		case "provider":
			if !controlOrigin(v) {
				return false
			}
		}
	}
	return true
}

func sameControlWitness(entry, terminal controlReport) bool {
	if terminal.Phase == "threw" && terminal.Event != "ui.render" {
		return true
	}
	var before, after map[string]json.RawMessage
	_ = json.Unmarshal(entry.Data, &before)
	_ = json.Unmarshal(terminal.Data, &after)
	for key, value := range before {
		if !bytes.Equal(value, after[key]) {
			return false
		}
	}
	return true
}
