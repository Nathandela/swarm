package skeleton

// FAILING-FIRST (TDD RED, GG-5) for ADR-010 §6's carriage, layer 4: the daemon hands the
// PRODUCER the CLI's own event body instead of the callback envelope it was wrapped in.
//
// THIS IS THE HOP a1b-claude-producer.md §10 measured as broken, in as many words: "the
// producer of §1-§6 shapes NOTHING in production today", because `serveHookInteractions` hands
// the shaper the callback ENVELOPE and the flattener has already dropped `tool_input` and
// `tool_response` on the way. The chain test (interaction_chain_e2e_test.go) proves everything
// BELOW captureInteractions and enters there deliberately; this file enters one hop higher, at
// the socket, so the entry point itself is what is under test.
//
// The bodies are the RECORDED S-B corpus, byte for byte, referenced from the producer's own
// testdata rather than copied (see claudeCorpusDir). The shaper is the shipped
// internal/adapter/claude. Nothing here runs a CLI.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/persist"
)

// recordedBody returns the raw body of the first hook payload for event in a recorded fixture.
func recordedBody(t *testing.T, fixture, event string) []byte {
	t.Helper()
	fx, err := fixtureio.LoadFixture(filepath.Join(claudeCorpusDir, fixture))
	if err != nil {
		t.Fatalf("LoadFixture(%s): %v", fixture, err)
	}
	for _, hp := range fx.HookPayloads {
		if hp.Event == event {
			return hp.Raw
		}
	}
	t.Fatalf("%s records no %s payload", fixture, event)
	return nil
}

// TestComposeLaunchSpec_CarriesTheAdaptersCaptureRows: the assembly is the only layer that
// holds both the registry and the launch spec, so it is where the adapter's declaration is read
// and handed to the daemon for injection. Reading it anywhere else would mean either an adapter
// import in internal/daemon (a layering break) or a hook process guessing at rows it cannot
// know.
func TestComposeLaunchSpec_CarriesTheAdaptersCaptureRows(t *testing.T) {
	got, err := composeLaunchSpec(daemon.LaunchSpec{AgentType: "claude", Cwd: "/work"},
		testEndpoint, "", srcGetter("", persist.Meta{}), stubLookPath)
	if err != nil {
		t.Fatalf("compose a claude launch: %v", err)
	}
	want := adapter.CaptureEvents(claude.New())
	if len(want) == 0 {
		t.Fatal("the claude adapter declares no capture rows; ADR-010 §5 names five")
	}
	if len(got.CaptureEvents) != len(want) {
		t.Fatalf("the composed spec carries capture rows %v; want the adapter's own %v -- these are what "+
			"reach the hook process, and a row missing here is a body flattened away", got.CaptureEvents, want)
	}
	for i, row := range want {
		if got.CaptureEvents[i] != row {
			t.Errorf("capture row %d = %q; want %q", i, got.CaptureEvents[i], row)
		}
	}
}

// TestComposeLaunchSpec_AnAdapterWithNoCaptureExtensionDeclaresNoRows. The reserved dev/test
// "fake" agent resolves to no adapter at all, which is the same answer every shipped adapter but
// claude gives today: no rows, and a session that captures nothing (ADR-010 §5 -- an upgrade,
// never a precondition).
func TestComposeLaunchSpec_AnAdapterWithNoCaptureExtensionDeclaresNoRows(t *testing.T) {
	got, err := composeLaunchSpec(daemon.LaunchSpec{AgentType: "fake", Cwd: "/work"},
		testEndpoint, "/bin/fake-agent", srcGetter("", persist.Meta{}), stubLookPath)
	if err != nil {
		t.Fatalf("compose a fake launch: %v", err)
	}
	if len(got.CaptureEvents) != 0 {
		t.Errorf("the fake agent's spec carries capture rows %v; want none", got.CaptureEvents)
	}
}

// TestCarriage_AnAuthenticatedHookPostShapesTheCLIsOwnBody is the slice's acceptance test: a
// hook callback posted over the PRODUCTION transport, carrying a REAL recorded body, produces
// the golden items in the journal.
//
// The two bodies are chosen for what the old path destroyed. UserPromptSubmit's `prompt` is a
// top-level string, so the flattener kept it -- but nested under `payload` in the envelope,
// where the shaper's top-level read found nothing. PreToolUse's `tool_input` is an OBJECT: the
// flattener dropped it outright, so the tool card had no path to name and the shaper returned
// nothing at all (measured, a1b-claude-producer.md §10).
func TestCarriage_AnAuthenticatedHookPostShapesTheCLIsOwnBody(t *testing.T) {
	sk := assemble(t)
	m := launchFake(t, sk, "print CARRIAGE\nidle 60s\n")
	token := hookTokenFor(t, sk.stateDir, m.ID)
	sk.setAdapterForTest(func(string) (adapter.Adapter, bool) { return claude.New(), true })

	const fixture = "claude-edit-permissionrequest-run1.json"
	for i, ev := range []string{"UserPromptSubmit", "PreToolUse"} {
		if err := hookclient.Post(sk.SocketPath(), engine.Callback{
			SessionID: m.ID, Token: token, Sequence: uint64(i + 1), Event: ev,
			Raw: recordedBody(t, fixture, ev),
		}); err != nil {
			t.Fatalf("post the recorded %s: %v", ev, err)
		}
	}

	got := awaitItems(t, sk, m.ID, 2)
	byKind := map[string]map[string]any{}
	for _, item := range got {
		byKind[itemString(t, item, "kind")] = item
	}

	msg, ok := byKind[adapter.KindUserMessage]
	if !ok {
		t.Fatalf("the journal holds no user_message; the recorded UserPromptSubmit body reached the "+
			"producer as %v", got)
	}
	const prompt = "Using the Edit tool, change the text 'line two' to 'line TWO EDITED' in edit-target3.txt"
	if txt := itemString(t, msg, "text"); txt != prompt {
		t.Errorf("the journalled user_message text = %q; want the prompt the owner actually typed %q", txt, prompt)
	}

	run, ok := byKind[adapter.KindToolRun]
	if !ok {
		t.Fatalf("the journal holds no tool_run: PreToolUse's `tool_input` is a nested OBJECT, and a "+
			"carriage that flattens it away leaves the shaper nothing to shape. items: %v", got)
	}
	action, _ := run["action"].(map[string]any)
	if action["path"] != "/Users/Nathan/spike-sb-work/edit-target3.txt" {
		t.Errorf("the tool_run's action = %v; want the path read out of the recorded `tool_input` -- the "+
			"one field the flattened payload could never carry", action)
	}
}

// TestCarriage_AHookPostWithNoCapturedBodyShapesNothing. Every non-capture row -- and every
// session whose adapter implements no capture extension at all -- posts a callback with no raw
// body, and that must remain a pure status post. An item shaped out of an envelope would be
// content the CLI never emitted.
func TestCarriage_AHookPostWithNoCapturedBodyShapesNothing(t *testing.T) {
	sk := assemble(t)
	m := launchFake(t, sk, "print CARRIAGE-NONE\nidle 60s\n")
	token := hookTokenFor(t, sk.stateDir, m.ID)
	sk.setAdapterForTest(func(string) (adapter.Adapter, bool) { return claude.New(), true })

	if err := hookclient.Post(sk.SocketPath(), engine.Callback{
		SessionID: m.ID, Token: token, Sequence: 1, Event: "Notification",
		Payload: map[string]string{"notification_type": "idle"},
	}); err != nil {
		t.Fatalf("post a bodyless Notification: %v", err)
	}
	// Well past one admission window: an item offered to the floor would have been released by
	// now (remotegw.DefaultAppendWindow, driven by releaseInteractions).
	time.Sleep(500 * time.Millisecond)
	if items := interactionItems(t, sk, m.ID); len(items) != 0 {
		t.Fatalf("a hook post carrying no captured body produced %d interaction record(s): %v", len(items), items)
	}
}
