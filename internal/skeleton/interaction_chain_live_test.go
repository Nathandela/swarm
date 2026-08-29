package skeleton

// THE LIVE-PATH CHAIN TEST: the recorded corpus entering where a LIVE Claude Code hook enters --
// `swarm hook <event>`, the real binary, the CLI's own body on its stdin -- and coming out on the
// phone as the transcript the owner reads, the agent's prose included.
//
// WHAT WAS NOT COVERED, AND WHY NOTHING IN-PROCESS COULD COVER IT.
// interaction_chain_e2e_test.go proves every hop BELOW captureInteractions and says so in its
// header: it hands the shaper a HookPayload the TEST built. interaction_carriage_test.go enters
// one hop higher, at hookclient.Post over the daemon socket, but still composes the
// engine.Callback itself. Between the two sits the hop a live CLI actually takes -- the hook
// PROCESS: readHookBody's one bounded read, parseHookStdin's flattening, hookclient.FromEnv's
// sequence file, CapturesRaw's env gate, Post -- and that hop is unreachable from any in-package
// test, because cmd/swarm is package main and Go cannot import it.
//
// So this file SPAWNS it. The binary is the one the rig already builds (swarmBin -- the same
// `swarm` the daemon launches its shims with), the environment is the one the DAEMON injected at
// this session's spawn (read back out of the 0600 shim-launch.json: session id, token, socket,
// sequence file), and stdin is the recorded body byte for byte. From that process onward every
// hop is production:
//
//	swarm hook   REAL, A SEPARATE PROCESS. cmd/swarm's runHook, unmocked and unmodified.
//	daemon       REAL. serveHook -> HandleCallback (S6 token auth) -> serveHookInteractions.
//	shaper       REAL. internal/adapter/claude, reached through the daemon's own adapterFor seam.
//	producer     REAL. §2's envelope, the turn, §5's caps, ADR-010 §7's append floor, journal.
//	gateway      REAL, a SEPARATE PROCESS (cmd/swarm-remote), over a real relay to a real phone.
//
// The sequence file is READ AND INCREMENTED by each spawned hook, as G5 intends, but its VALUE is
// not under test here -- see the dressing note below, and i1-carriage.md §9.1.
//
// THE TIMESTAMP IS THE DAEMON'S BY CONSTRUCTION HERE. The chain test has to overwrite each
// fixture's `received_at_ms` before replaying it (its own header explains why: a 2026-07-18
// instant replayed as `now` opens and closes an approval's window three weeks in the past). This
// file cannot make that mistake: the recorded field never leaves the fixture, because the hook
// process does not carry one and serveHookInteractions stamps time.Now() itself. ADR-010 §3's
// "timestamps are daemon-authoritative" is not asserted here, it is structural.
//
// THE TWO PLACES THE SESSION IS DRESSED AS A CLAUDE SESSION, stated plainly. The rig launches the
// scripted FAKE agent, because launching agent `claude` would start a real CLI against Anthropic,
// which this program does not do. A fake session's registry lookup yields no adapter and its
// injected SWARM_HOOK_CAPTURE is therefore empty, so the two values the registry would have
// supplied for a claude session are supplied here -- and BOTH are read off the shipped adapter
// rather than written by hand: adapter.CaptureEvents(claude.New()) for the hook process's
// environment, claude.New() for the daemon's adapterFor seam. Nothing else is substituted, and
// the registry lookup itself (`claude` -> the claude adapter) remains covered where it lives.
//
// WHAT THAT DRESSING COSTS, MEASURED AND NOT GUESSED. A fake session registers no SignalSources
// either, so `deriveDims` maps every one of these events to no status dimension and
// HandleCallback returns EARLY -- an unmapped event is a benign no-op (engine.go) -- which is
// before applyTyped, where the G5 replay guard lives. The engine's TOKEN check runs before that
// early return and is fully exercised (the forged post below is what says so), but the sequence
// VALUE is not: a `swarm hook` that reused sequence 1 for every record left this test green, and
// that mutation is recorded as uncaught rather than quietly dropped (i1-carriage.md §9.1). The
// consequence for the STATUS plane is the same and equally deliberate: no turn/interaction
// dimension moves on this session, so IS-LIFE-2's answered_locally -- which a real claude
// session's Stop would trigger on a pending card -- is not reached here.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/protocol"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// TestClaudeLiveChainE2E_TheRecordedBodiesEnterThroughSwarmHookAndThePhoneRendersTheProse.
func TestClaudeLiveChainE2E_TheRecordedBodiesEnterThroughSwarmHookAndThePhoneRendersTheProse(t *testing.T) {
	rig := newS19Rig(t)
	rig.Pair()
	rig.StartGateway()
	rig.Eventually("the machine's reconcile record reached the phone", func() bool {
		return rig.Summary().Reconciled
	})

	sessionID := rig.LaunchOnMachine("print E2E_CLAUDE_LIVE\nidle 600s\n")
	rig.Eventually("the phone's roster shows the session the machine launched", func() bool {
		return rig.RosterHas(sessionID)
	})
	_, localID, ok := protocol.ParseID(sessionID)
	if !ok {
		t.Fatalf("owner Launch returned %q, which is not a namespaced id", sessionID)
	}
	dressAsClaudeSession(rig.sk)

	const fixture = "claude-edit-permissionrequest-run1.json"
	env := claudeHookEnv(t, rig.stateDir, localID)

	// A FORGED post FIRST: another local process that found the socket and the event name but not
	// the session's token, replaying the recording's own Stop body. serveHook runs the engine's
	// token check BEFORE the interaction plane exactly so this cannot reach the owner's
	// transcript (conn.go: "a capture that ran before it would be a second, unauthenticated write
	// path into the journal"), and it is sent first so the counts below are what refute it -- a
	// SECOND agent_message on the phone would be an unauthenticated one.
	runHookBinary(t, withEnv(env, hookclient.EnvToken, "not-the-sessions-token"),
		"Stop", recordedBody(t, fixture, "Stop"))

	// Then the whole recorded Edit turn, one `swarm hook` PROCESS per record, in recording order:
	// the prompt, a Read, an Edit that escalated to a permission dialog, the applied change, and
	// the agent's reply.
	replayCorpusThroughHookBinary(t, rig.sk, env, fixture)

	// EIGHT records, SIX items. The two tool calls fold open+close into one row each
	// (IS-DELTA-3), and the recording's Notification is not one of the adapter's capture rows --
	// the HOOK PROCESS drops that body itself, on the env gate, before the daemon ever sees it.
	//
	// Waited for in TWO stages, and the second one is why: the reply is the last thing the
	// recording produces, so a single wait on a count would report "six items never arrived" for
	// a carriage that dropped the prompt just as readily as for one that dropped the prose. The
	// second label names the reply, so the failure names it too.
	var transcript []swarmmobile.TranscriptItem
	rig.Eventually("the five items the recorded turn produces before its reply reached the phone", func() bool {
		transcript = readTranscript(t, rig, sessionID)
		return len(transcript) >= 5
	})
	rig.Eventually("the agent's own reply -- Stop's last_assistant_message, shaped into an agent_message -- reached the phone", func() bool {
		transcript = readTranscript(t, rig, sessionID)
		return len(transcriptByKind(transcript)["agent_message"]) == 1
	})
	byKind := transcriptByKind(transcript)
	for _, want := range []struct {
		kind string
		n    int
	}{
		{"user_message", 1}, {"tool_run", 2}, {"approval_request", 1},
		{"file_change", 1}, {"agent_message", 1},
	} {
		if got := len(byKind[want.kind]); got != want.n {
			t.Errorf("the phone holds %d %s item(s); want %d. items: %v%s",
				got, want.kind, want.n, transcriptKinds(transcript), rig.gatewayTail())
		}
	}
	if len(transcript) != 6 {
		t.Fatalf("the phone holds %d item(s) %v; the recording's EIGHT records make exactly six. A "+
			"SEVENTH is content this turn does not contain -- and a second agent_message is the FORGED "+
			"Stop, which means the interaction plane wrote an item the engine refused%s",
			len(transcript), transcriptKinds(transcript), rig.gatewayTail())
	}

	// -- the prompt the owner typed, from the CLI's stdin to the phone's screen --
	const prompt = "Using the Edit tool, change the text 'line two' to 'line TWO EDITED' in edit-target3.txt"
	if got := byKind["user_message"][0].Text; got != prompt {
		t.Errorf("user_message text = %q; want the recorded prompt %q", got, prompt)
	}

	// -- the tool runs -------------------------------------------------------
	tools := map[string]swarmmobile.TranscriptItem{}
	for _, it := range byKind["tool_run"] {
		if it.Status != "completed" {
			t.Errorf("a tool_run reached the phone with status %q; both recorded calls returned, and an "+
				"item left `in_progress` is a row that spins forever (IS-ST-1)", it.Status)
		}
		tool, _ := itemBody(t, it)["tool"].(string)
		tools[tool] = it
	}
	read, hasRead := tools["Read"]
	if _, hasEdit := tools["Edit"]; !hasRead || !hasEdit {
		t.Fatalf("the phone's tool_run rows name %v; the recording ran Read then Edit", mapKeys(tools))
	}
	// The two fields that exist ONLY inside the nested `tool_input`/`tool_response` objects: the
	// flattened status payload cannot carry either, so both are proof the CLI's own body made the
	// whole trip through the hook process (a1b-claude-producer.md §10's measured zero).
	if act, _ := itemBody(t, read)["action"].(map[string]any); act["type"] != "read" ||
		act["path"] != "/Users/Nathan/spike-sb-work/edit-target3.txt" {
		t.Errorf("the Read tool_run's action = %v; want {type:read, path:/Users/Nathan/spike-sb-work/edit-target3.txt}", act)
	}
	if got, _ := itemBody(t, read)["output_excerpt"].(string); got != "line one\nline two\nline three\n" {
		t.Errorf("the Read tool_run's output_excerpt = %q; want the file the CLI actually read", got)
	}

	// -- the applied change (IS-FC-1) ----------------------------------------
	fc := itemBody(t, byKind["file_change"][0])
	if fc["path"] != "/Users/Nathan/spike-sb-work/edit-target3.txt" || fc["change"] != "modify" {
		t.Errorf("the file_change names %v/%v; want the edited path, modified", fc["path"], fc["change"])
	}
	if diff, _ := fc["diff_excerpt"].(string); diff != "@@ -1,3 +1,3 @@\n line one\n-line two\n+line TWO EDITED\n line three" {
		t.Errorf("the file_change's diff_excerpt = %q; want the hunk rendered from the recorded "+
			"structuredPatch -- it is the whole of what the owner is shown about the edit", diff)
	}

	// -- the card ------------------------------------------------------------
	card := itemBody(t, byKind["approval_request"][0])
	if card["summary"] != "Edit /Users/Nathan/spike-sb-work/edit-target3.txt" {
		t.Errorf("the card's summary = %v; want the LITERAL action the owner is authorizing", card["summary"])
	}
	if card["mode"] != "card" {
		t.Errorf("the Edit card's mode = %v; want `card` (S-C measured Edit's PermissionRequest hook "+
			"resolving natively)", card["mode"])
	}
	assertDecisions(t, card, map[string]string{"allow": "Yes", "deny": "No"})
	assertNoKeystrokeLeak(t, card)

	// -- THE AGENT'S PROSE, which is what this leg is for ---------------------
	// Stop is the fifth capture row and the only recorded event carrying prose the agent wrote
	// (ADR-010's 2026-08-07 amendment). Its value is also the one shaped field a FLATTENED payload
	// could structurally have carried -- `last_assistant_message` is a top-level string, unlike
	// `tool_input` -- so what makes this an end-to-end assertion is not that the text survived, but
	// that it arrives as a shaped agent_message ITEM, inside the turn the prompt opened, carrying a
	// terminal status: a transcript row, not a status dimension.
	msg := byKind["agent_message"][0]
	if got, want := itemBody(t, msg)["text"], recordedProse(t, fixture); got != want {
		t.Errorf("the agent_message's text = %v; want the recorded last_assistant_message %q -- the "+
			"agent's own reply is the half of a transcript a human actually reads", got, want)
	}
	if msg.Status != "completed" {
		t.Errorf("the agent_message reached the phone with status %q; the recorded Stop is the end of "+
			"the reply, and a non-terminal status there leaves the turn open forever (IS-ENV-1)", msg.Status)
	}

	// -- one turn, holding all six -------------------------------------------
	// IS-ENV-1: the turn OPENS on the user_message and CLOSES on the terminal agent_message --
	// which must still carry the id of the turn it closes, or the reply would hang outside the
	// exchange that produced it.
	turn := byKind["user_message"][0].TurnID
	if turn == "" {
		t.Fatalf("the user_message that opened the turn carries no turn_id (IS-ENV-1)")
	}
	for _, it := range transcript {
		if it.TurnID != turn {
			t.Errorf("the %s item carries turn_id %q; the turn the recorded prompt opened is %q (IS-ENV-1)",
				it.Kind, it.TurnID, turn)
		}
	}
}

// ---- the live path ---------------------------------------------------------

// replayCorpusThroughHookBinary runs the REAL `swarm hook <event>` binary once per recorded
// payload, in recording order, with the CLI's own body on stdin -- the exact invocation Claude
// Code's settings hook makes (claude.go's hookSettingsJSON emits `swarm hook <event>`).
//
// Serially, and in recording order: a CLI fires its hooks one at a time, and the transcript this
// asserts on is an ordered exchange rather than a bag of items.
//
// AND THE PROCESS EXITING IS NOT WHAT MAKES IT ORDERED -- awaitHookIngested is. See its comment:
// each record is waited INTO the daemon before the next one is posted, because a `swarm hook`
// that has exited has only handed its callback over, not had it applied.
func replayCorpusThroughHookBinary(t *testing.T, sk *Daemon, env []string, fixture string) {
	t.Helper()
	fx, err := fixtureio.LoadFixture(filepath.Join(claudeCorpusDir, fixture))
	if err != nil {
		t.Fatalf("LoadFixture(%s): %v", fixture, err)
	}
	for _, hp := range fx.HookPayloads {
		runHookBinary(t, env, hp.Event, hp.Raw)
		awaitHookIngested(t, sk, env, hp.Event)
	}
}

// awaitHookIngested blocks until the daemon has INGESTED the callback the `swarm hook` process
// that just exited posted. It is the barrier that makes "in recording order" true of the daemon
// and not merely of the spawns.
//
// WHY THE PROCESS EXITING IS NOT A SYNC POINT. `swarm hook` returns once its callback has been
// handed over -- written to the daemon socket (hookclient.Post returns after the write; there is
// no reply to wait for) or durably spooled by the shim (PostToShim's ack byte). The daemon APPLIES
// it afterwards, on whichever goroutine picks it up: one per connection for a direct post
// (daemon.Config.ConnHandler -- "each connection is handed to ConnHandler in its own goroutine"),
// or HookDrainer's 250ms spool tick for a shim-carried one. Two records posted in order can
// therefore be SHAPED out of order, and the turn is what breaks first: IS-ENV-1 closes the turn on
// the terminal agent_message, so a Stop that overtakes the Edit's PostToolUse leaves the
// file_change behind it with no turn_id at all -- the exact CI failure this rig produced
// (docs/verification/r0-flake-rootcause.md).
//
// THE OBSERVABLE IS THE DAEMON'S OWN INGEST BOOKKEEPING, not a sleep and not an item count (only
// some records shape items at all, and the floor may still be holding the ones that do):
// markHookSeqIngested records a callback's sequence only AFTER serveHookInteractions has shaped
// and offered its items (hookdrain.go), so a sequence that reads back as ingested means this
// record's items already carry the turn they belong to.
func awaitHookIngested(t *testing.T, sk *Daemon, env []string, event string) {
	t.Helper()
	session := envValue(env, hookclient.EnvSessionID)
	seq := hookSequenceUsed(t, env)
	deadline := time.Now().Add(s19Deadline)
	for time.Now().Before(deadline) {
		if sk.hookSeqDuplicate(session, seq) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("`swarm hook %s` (callback sequence %d for session %s) never reached the daemon's "+
		"interaction plane within %s: it was posted and the process exited 0, so either the shim's "+
		"spool never carried it or the drain never applied it", event, seq, session, s19Deadline)
}

// hookSequenceUsed reads the sequence the hook process that just exited consumed: the daemon-
// injected per-session counter FILE holds it, because nextSequence writes the incremented value
// under its flock before the post (hookclient). It is the id the daemon's dedup set is keyed by.
func hookSequenceUsed(t *testing.T, env []string) uint64 {
	t.Helper()
	path := envValue(env, hookclient.EnvSequenceFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the session's hook sequence file %s: %v", path, err)
	}
	seq, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		t.Fatalf("the hook sequence file %s holds %q, which is not a counter: %v", path, data, err)
	}
	return seq
}

// runHookBinary is the one exec: `swarm hook <event>` with body on stdin, exactly as the CLI
// spawns it (claude.go's hookSettingsJSON emits that command line for every hook row).
//
// A NON-ZERO EXIT IS THE FAILURE, NOT A REFUSED CALLBACK. runHook exits 0 whenever the post
// reached the socket -- the daemon's verdict travels back over no channel a hook can see -- so
// the forged call above is expected to succeed here and to change nothing on the phone.
func runHookBinary(t *testing.T, env []string, event string, body []byte) {
	t.Helper()
	cmd := exec.Command(swarmBin, "hook", event)
	cmd.Env, cmd.Stdin = env, bytes.NewReader(body)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("`swarm hook %s` failed: %v\n%s", event, err, out)
	}
}

// claudeHookEnv is the environment the DAEMON injected into this session's agent at spawn, read
// back out of the 0600 shim-launch.json the daemon wrote there -- the only place the per-session
// hook token lives besides the agent's own environment (ADR-004). Session id, token, daemon
// socket and the monotonic sequence FILE all come from it verbatim, so the spawned hook
// authenticates exactly as a live one does.
//
// SWARM_HOOK_CAPTURE is the one variable substituted, with the shipped claude adapter's own rows:
// this session's agent is the fake one, whose registry lookup declares no capture at all. See the
// file header.
func claudeHookEnv(t *testing.T, stateDir, id string) []string {
	t.Helper()
	path := filepath.Join(stateDir, id, "shim-launch.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the session's injected hook environment from %s: %v", path, err)
	}
	var cfg struct {
		Env []string `json:"env"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return withEnv(cfg.Env, hookclient.EnvCapture,
		hookclient.CaptureEnv(adapter.CaptureEvents(claude.New())))
}

// withEnv returns env with key set to value, replacing any existing binding.
func withEnv(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return append(out, key+"="+value)
}

// dressAsClaudeSession points the daemon's adapter seam at the shipped claude producer through
// the synchronized test seam. The rig runs under -race, and a bare assignment can race the
// daemon's background capture and capability-authoring goroutines.
func dressAsClaudeSession(sk *Daemon) {
	sk.setAdapterForTest(func(string) (adapter.Adapter, bool) { return claude.New(), true })
}

// recordedProse is the `last_assistant_message` of a fixture's Stop body: the agent's own reply,
// as the CLI wrote it. Read from the corpus rather than pasted, so a re-recorded fixture cannot
// leave this test asserting a sentence no recording holds.
func recordedProse(t *testing.T, fixture string) string {
	t.Helper()
	var body struct {
		LastAssistantMessage string `json:"last_assistant_message"`
	}
	if err := json.Unmarshal(recordedBody(t, fixture, "Stop"), &body); err != nil {
		t.Fatalf("decode the recorded Stop body of %s: %v", fixture, err)
	}
	if body.LastAssistantMessage == "" {
		t.Fatalf("%s records a Stop with an empty last_assistant_message; the prose assertion below "+
			"would pass against a producer that shaped nothing at all", fixture)
	}
	return body.LastAssistantMessage
}
