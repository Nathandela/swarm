package protocol

// FAILING-FIRST suite for ADR-010 Phase 2 PIECE 1: spawn lineage on the wire
// (D4 — "Launch gains an optional spawned-from link recorded in session meta,
// mirroring the ResumedFrom pattern, with an intent tag (handoff|delegate); the
// roster/SessionView exposes it").
//
// FROZEN API the implementer must provide (schema.go, mirroring the ResumedFrom
// precedent at every hop):
//
//	type LaunchReq struct {
//	    ...
//	    SpawnedFrom string `json:"spawned_from,omitempty"`
//	    SpawnIntent string `json:"spawn_intent,omitempty"`
//	}
//	type SessionView struct {
//	    ...
//	    SpawnedFrom string `json:"spawned_from,omitempty"`
//	    SpawnIntent string `json:"spawn_intent,omitempty"`
//	}
//
//   - SpawnedFrom is the LOCAL id of the spawning session — the value the daemon
//     injects as SWARM_SESSION_ID (daemon/launch.go:413) and exactly what
//     Meta.ResumedFrom already holds. It is carried VERBATIM: the server does not
//     resolve, namespace or rewrite it.
//   - SpawnIntent is one of "handoff" | "delegate", and only when SpawnedFrom is set.
//   - Both are omitempty on BOTH types: a launch/roster message that uses neither
//     serializes byte-identically to today's shape (the additive-field discipline
//     every remote-tier field already follows).
//
// Server (handleLaunch): a SpawnIntent outside {"", "handoff", "delegate"} and a
// SpawnIntent carried WITHOUT a SpawnedFrom are both refused CodeInvalidField, with
// NO daemon side effect. daemonLaunchSpec copies both onto daemon.LaunchSpec, and
// stampView copies both out of persist.Meta onto every SessionView (OpList and
// OpSubscribe alike), so the lineage survives the crash-safe meta round-trip and
// reaches the roster.
//
// RED today: the fields do not exist, so this file fails to compile on them; the
// protocol.md row test fails on the missing spec rows (GG-7 lockstep).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// ---------------------------------------------------------------------------
// Schema round-trip + omitempty
// ---------------------------------------------------------------------------

// TestSpawnLineage_LaunchReqRoundTrip pins both directions on LaunchReq: the two
// fields survive marshal/unmarshal under their snake_case keys, and a request that
// sets NEITHER emits NEITHER key (omitempty), so an un-lineaged launch is byte-wise
// the shape an older peer already parses.
func TestSpawnLineage_LaunchReqRoundTrip(t *testing.T) {
	t.Run("with lineage", func(t *testing.T) {
		in := LaunchReq{
			Agent: "claude", Cwd: "/tmp", Cols: 80, Rows: 24,
			SpawnedFrom: "sess-parent-1",
			SpawnIntent: "handoff",
		}
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("marshal LaunchReq: %v", err)
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(b, &keys); err != nil {
			t.Fatalf("LaunchReq is not a JSON object: %v", err)
		}
		for _, k := range []string{"spawned_from", "spawn_intent"} {
			if _, ok := keys[k]; !ok {
				t.Errorf("LaunchReq JSON missing snake_case key %q; got %s", k, b)
			}
		}
		var got LaunchReq
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal LaunchReq: %v", err)
		}
		if got.SpawnedFrom != in.SpawnedFrom || got.SpawnIntent != in.SpawnIntent {
			t.Errorf("round-trip lineage = (%q, %q), want (%q, %q)",
				got.SpawnedFrom, got.SpawnIntent, in.SpawnedFrom, in.SpawnIntent)
		}
	})

	t.Run("without lineage the keys are absent", func(t *testing.T) {
		b, err := json.Marshal(LaunchReq{Agent: "claude", Cwd: "/tmp", Cols: 80, Rows: 24})
		if err != nil {
			t.Fatalf("marshal LaunchReq: %v", err)
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(b, &keys); err != nil {
			t.Fatalf("LaunchReq is not a JSON object: %v", err)
		}
		for _, k := range []string{"spawned_from", "spawn_intent"} {
			if _, ok := keys[k]; ok {
				t.Errorf("LaunchReq without lineage still emitted %q (%s); both fields must be omitempty", k, b)
			}
		}
	})
}

// TestSpawnLineage_SessionViewRoundTrip is the same pin on the roster row: a view
// carrying lineage round-trips it, and a view without lineage emits neither key, so
// every existing roster consumer sees an unchanged object.
func TestSpawnLineage_SessionViewRoundTrip(t *testing.T) {
	t.Run("with lineage", func(t *testing.T) {
		in := SessionView{
			EndpointID: "ep1", ID: "ep1/child", Agent: "codex", Cwd: "/tmp",
			Group:       status.GroupWorking,
			SpawnedFrom: "parent1",
			SpawnIntent: "delegate",
		}
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("marshal SessionView: %v", err)
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(b, &keys); err != nil {
			t.Fatalf("SessionView is not a JSON object: %v", err)
		}
		for _, k := range []string{"spawned_from", "spawn_intent"} {
			if _, ok := keys[k]; !ok {
				t.Errorf("SessionView JSON missing snake_case key %q; got %s", k, b)
			}
		}
		var got SessionView
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal SessionView: %v", err)
		}
		if got.SpawnedFrom != in.SpawnedFrom || got.SpawnIntent != in.SpawnIntent {
			t.Errorf("round-trip lineage = (%q, %q), want (%q, %q)",
				got.SpawnedFrom, got.SpawnIntent, in.SpawnedFrom, in.SpawnIntent)
		}
	})

	t.Run("without lineage the keys are absent", func(t *testing.T) {
		b, err := json.Marshal(SessionView{EndpointID: "ep1", ID: "ep1/a", Agent: "claude"})
		if err != nil {
			t.Fatalf("marshal SessionView: %v", err)
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(b, &keys); err != nil {
			t.Fatalf("SessionView is not a JSON object: %v", err)
		}
		for _, k := range []string{"spawned_from", "spawn_intent"} {
			if _, ok := keys[k]; ok {
				t.Errorf("SessionView without lineage still emitted %q (%s); both fields must be omitempty", k, b)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Server validation (handleLaunch)
// ---------------------------------------------------------------------------

// lineageLaunchReq is a launch valid in every OTHER respect, so each validation case
// isolates the lineage fields as the only possible reason to refuse.
func lineageLaunchReq(t *testing.T) LaunchReq {
	t.Helper()
	return LaunchReq{Agent: "claude", Cwd: t.TempDir(), Cols: 80, Rows: 24}
}

// TestSpawnLineage_IntentVocabularyEnforced pins the closed intent vocabulary on the
// OWNER tier (where every `swarm spawn` lands): "", "handoff" and "delegate" are
// accepted and forwarded; anything else — including a near-miss of case or a
// plausible-but-unlisted word — is refused CodeInvalidField with no daemon side
// effect. An unvalidated intent would persist into meta and out to every roster.
func TestSpawnLineage_IntentVocabularyEnforced(t *testing.T) {
	accepted := []struct {
		name        string
		spawnedFrom string
		intent      string
	}{
		{"no lineage at all", "", ""},
		{"handoff", "sess-parent-1", "handoff"},
		{"delegate", "sess-parent-1", "delegate"},
	}
	for _, tc := range accepted {
		t.Run("accepted/"+tc.name, func(t *testing.T) {
			stub := newStubDaemon()
			rc := rawDial(t, serveStub(t, stub))
			rep := rc.hello(Version, nil)

			req := lineageLaunchReq(t)
			req.SpawnedFrom, req.SpawnIntent = tc.spawnedFrom, tc.intent
			rc.writeControl(Control{Op: OpLaunch, EndpointID: rep.EndpointID, Launch: &req})

			if got := rc.readControl(); got.Op == OpError {
				t.Fatalf("launch with spawn_intent %q refused: %q / %q; the vocabulary must accept it",
					tc.intent, got.Error, got.ErrorCode)
			}
			specs := stub.launchSpecs()
			if len(specs) != 1 {
				t.Fatalf("DaemonAPI.Launch called %d times, want 1", len(specs))
			}
			if specs[0].SpawnedFrom != tc.spawnedFrom || specs[0].SpawnIntent != tc.intent {
				t.Errorf("daemon LaunchSpec lineage = (%q, %q), want (%q, %q) carried verbatim",
					specs[0].SpawnedFrom, specs[0].SpawnIntent, tc.spawnedFrom, tc.intent)
			}
		})
	}

	refused := []struct {
		name        string
		spawnedFrom string
		intent      string
	}{
		{"junk word", "sess-parent-1", "junk"},
		{"wrong case", "sess-parent-1", "Handoff"},
		{"unlisted but plausible", "sess-parent-1", "resume"},
		{"whitespace padded", "sess-parent-1", " handoff"},
	}
	for _, tc := range refused {
		t.Run("refused/"+tc.name, func(t *testing.T) {
			stub := newStubDaemon()
			rc := rawDial(t, serveStub(t, stub))
			rep := rc.hello(Version, nil)

			req := lineageLaunchReq(t)
			req.SpawnedFrom, req.SpawnIntent = tc.spawnedFrom, tc.intent
			rc.writeControl(Control{Op: OpLaunch, EndpointID: rep.EndpointID, Launch: &req})

			got := rc.readControl()
			if got.Op != OpError || got.ErrorCode != CodeInvalidField {
				t.Fatalf("launch with spawn_intent %q = op %q code %q; want error/invalid_field "+
					"(the intent vocabulary is closed: handoff|delegate)", tc.intent, got.Op, got.ErrorCode)
			}
			if n := len(stub.launchSpecs()); n != 0 {
				t.Fatalf("daemon launched %d sessions for an invalid spawn_intent; want 0 (refused before any side effect)", n)
			}
		})
	}
}

// TestSpawnLineage_IntentWithoutSpawnedFromRefused pins the pairing rule: an intent
// describes a LINK, so it is meaningless — and unrenderable in the roster — without
// the session it links to. Refused CodeInvalidField, nothing launched.
func TestSpawnLineage_IntentWithoutSpawnedFromRefused(t *testing.T) {
	for _, intent := range []string{"handoff", "delegate"} {
		t.Run(intent, func(t *testing.T) {
			stub := newStubDaemon()
			rc := rawDial(t, serveStub(t, stub))
			rep := rc.hello(Version, nil)

			req := lineageLaunchReq(t)
			req.SpawnIntent = intent // SpawnedFrom deliberately left empty

			rc.writeControl(Control{Op: OpLaunch, EndpointID: rep.EndpointID, Launch: &req})
			got := rc.readControl()
			if got.Op != OpError || got.ErrorCode != CodeInvalidField {
				t.Fatalf("launch with spawn_intent %q and no spawned_from = op %q code %q; "+
					"want error/invalid_field", intent, got.Op, got.ErrorCode)
			}
			if n := len(stub.launchSpecs()); n != 0 {
				t.Fatalf("daemon launched %d sessions for an unpaired spawn_intent; want 0", n)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Roster exposure (OpList + OpSubscribe)
// ---------------------------------------------------------------------------

// TestSpawnLineage_RosterViewsCarryLineage pins the last hop: stampView copies the
// persisted lineage onto every SessionView, so both the list snapshot and the
// subscribe stream carry it (D4's roster visibility, and what `swarm ls --json` and
// the TUI badge both read). A session with no lineage keeps both fields empty.
func TestSpawnLineage_RosterViewsCarryLineage(t *testing.T) {
	child := persist.Meta{
		ID: "child9", AgentType: "codex", Cwd: "/tmp",
		Status:      status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone},
		SpawnedFrom: "parent1",
		SpawnIntent: "handoff",
	}
	parent := persist.Meta{
		ID: "parent1", AgentType: "claude", Cwd: "/tmp",
		Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone},
	}

	stub := newStubDaemon()
	stub.setMetas(parent, child)
	sock := serveStub(t, stub)
	c := dialClient(t, sock, []string{"subscribe"})

	views, err := c.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byLocal := map[string]SessionView{}
	for _, v := range views {
		_, local, ok := ParseID(v.ID)
		if !ok {
			t.Fatalf("roster row id %q is not namespaced", v.ID)
		}
		byLocal[local] = v
	}
	if got := byLocal["child9"]; got.SpawnedFrom != "parent1" || got.SpawnIntent != "handoff" {
		t.Errorf("list view lineage = (%q, %q), want (%q, %q) carried from the persisted meta",
			got.SpawnedFrom, got.SpawnIntent, "parent1", "handoff")
	}
	if got := byLocal["parent1"]; got.SpawnedFrom != "" || got.SpawnIntent != "" {
		t.Errorf("an un-lineaged session's view = (%q, %q), want both empty", got.SpawnedFrom, got.SpawnIntent)
	}

	events, err := c.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	stub.pushStatus(child)
	select {
	case ev := <-events:
		if ev.Session.SpawnedFrom != "parent1" || ev.Session.SpawnIntent != "handoff" {
			t.Errorf("subscribe view lineage = (%q, %q), want (%q, %q)",
				ev.Session.SpawnedFrom, ev.Session.SpawnIntent, "parent1", "handoff")
		}
	case <-time.After(recvTimeout):
		t.Fatal("no subscribe event within the deadline")
	}
}

// ---------------------------------------------------------------------------
// GG-7 lockstep: the spec rows land with the fields
// ---------------------------------------------------------------------------

// TestSpawnLineage_ProtocolMDDocumentsFields pins the lockstep rule directly for
// this change: protocol.md's LaunchReq and SessionView field tables must carry a row
// for each new key. (The generic reflection drift check in protocolmd_test.go goes
// RED on the same omission once the Go fields exist; this test names the two keys so
// the missing deliverable is legible before that.)
func TestSpawnLineage_ProtocolMDDocumentsFields(t *testing.T) {
	path := filepath.Join(repoRoot(t), "docs", "specifications", "protocol.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("protocol.md not found at %s: %v", path, err)
	}
	doc := string(data)
	for _, key := range []string{"spawned_from", "spawn_intent"} {
		if !strings.Contains(doc, key) {
			t.Errorf("protocol.md documents no %q row (GG-7: a wire field and its spec row land together)", key)
		}
	}
	// The closed vocabulary is part of the contract, and belongs ON the spawn_intent
	// row: a reader looking the field up must find its legal values there. (Checked on
	// the row itself because "handoff" already appears elsewhere in the document.)
	var intentRow string
	for _, line := range strings.Split(doc, "\n") {
		if strings.Contains(line, "spawn_intent") {
			intentRow = line
			break
		}
	}
	for _, word := range []string{"handoff", "delegate"} {
		if !strings.Contains(intentRow, word) {
			t.Errorf("the protocol.md spawn_intent row does not state the value %q; row = %q", word, intentRow)
		}
	}
}
