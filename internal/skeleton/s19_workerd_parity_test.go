package skeleton

// Relay-v2 parity checks for behavior that used to be exercised only through
// the deleted in-process relay-v1 E2Es. These deliberately reuse the S19 rig:
// real Workerd, mobile facade, gateway binary, daemon, PTY and durable state.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

func TestS19Workerd_ComposerSendReachesCodexBackendAndNotPTY(t *testing.T) {
	rig := newS19Rig(t)
	rig.Pair()
	rig.StartGateway()
	rig.Eventually("the machine's reconcile record reached the phone", func() bool {
		return rig.Summary().Reconciled
	})

	ad := &r7CodexAdapter{Adapter: newPlainAdapter().Adapter}
	rig.sk.setAdapterForTest(func(string) (adapter.Adapter, bool) { return ad, true })
	session := rig.LaunchOnMachine(r7StdinScript)
	_, local, ok := protocol.ParseID(session)
	if !ok {
		t.Fatalf("owner Launch returned non-namespaced id %q", session)
	}

	backend := newR7FakeBackend()
	backend.reply["turn/start"] = json.RawMessage(
		`{"turn":{"id":"01a0033b-d0be-77e1-88e7-584ddeea562d","items":[],"itemsView":"notLoaded","status":"inProgress"}}`)
	rig.sk.registerBackend(local, "01a00339-a80e-72a0-966f-116427b6b9ce", backend)
	rig.Eventually("the Codex session reached the phone", func() bool { return rig.RosterHas(session) })
	rig.Eventually("the phone adopted the Codex session's current incarnation", func() bool {
		current, err := rig.App().Session(session)
		return err == nil && current.SessionInstance != ""
	})
	att, err := rig.Owner().Attach(session)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	t.Cleanup(func() { _ = att.Detach() })

	op, err := rig.App().ComposerSend(session, "", "ship it")
	if err != nil {
		t.Fatalf("App.ComposerSend: %v%s", err, rig.gatewayTail())
	}
	rig.Eventually("the composer operation resolved", func() bool {
		out, err := rig.App().Outcome(op.OperationID)
		if err != nil || !out.Resolved {
			return false
		}
		if out.Code != protocol.OpOK {
			t.Fatalf("composer_send refused: code=%q message=%q%s", out.Code, out.Message, rig.gatewayTail())
		}
		return true
	})

	params := r7CallParams(t, backend, "turn/start")
	input, ok := params["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("turn/start input = %#v, want one UserInput", params["input"])
	}
	first, _ := input[0].(map[string]any)
	if first["type"] != "text" || first["text"] != "ship it" {
		t.Fatalf("turn/start input[0] = %#v, want text %q", first, "ship it")
	}

	if err := att.Input([]byte("\n")); err != nil {
		t.Fatalf("flush PTY: %v", err)
	}
	if ok, drained := awaitFrames(att, "got:", 20*time.Second); !ok {
		t.Fatalf("the fake CLI never reported its stdin; drained %q", drained)
	} else if strings.Contains(drained, "ship it") {
		t.Fatalf("composer text reached the PTY instead of only the Codex backend: %q", drained)
	}
}

func TestS19Workerd_DisallowedLaunchIsRefusedWithoutSpawning(t *testing.T) {
	rig := newS19Rig(t)
	rig.Pair()
	rig.StartGateway()
	rig.Eventually("the machine's reconcile record reached the phone", func() bool {
		return rig.Summary().Reconciled
	})

	before := len(rig.sk.Core().List())
	op, err := rig.App().Launch(&swarmmobile.LaunchSpec{
		Agent: "fake", Cwd: "/", Options: "script=" + rig.script("print MUST_NOT_RUN\nidle 600s\n"),
	})
	if err != nil {
		t.Fatalf("App.Launch: %v", err)
	}
	rig.Eventually("the disallowed launch was answered", func() bool {
		out, err := rig.App().Outcome(op.OperationID)
		if err != nil || !out.Resolved {
			return false
		}
		if out.Code != string(protocol.CodePolicy) {
			t.Fatalf("disallowed launch = code %q message %q, want %q%s",
				out.Code, out.Message, protocol.CodePolicy, rig.gatewayTail())
		}
		return true
	})

	time.Sleep(time.Second)
	if after := len(rig.sk.Core().List()); after != before {
		t.Fatalf("disallowed launch changed session count from %d to %d", before, after)
	}
}

func TestS19Workerd_TerminalPeekBlanksOffAndRecoversOn(t *testing.T) {
	rig := newS19Rig(t)
	rig.Pair()
	rig.StartGateway()
	rig.Eventually("the machine's reconcile record reached the phone", func() bool {
		return rig.Summary().Reconciled
	})

	const marker = "S19_PEEK_RECOVERY"
	session := rig.LaunchOnMachine("print " + marker + "\nidle 600s\n")
	rig.Eventually("the terminal session reached the phone", func() bool { return rig.RosterHas(session) })
	if err := rig.App().TerminalWatch(session); err != nil {
		t.Fatalf("TerminalWatch: %v", err)
	}
	rig.Eventually("the initial terminal snapshot reached the phone", func() bool {
		snap, err := rig.App().Peek(session)
		return err == nil && strings.Contains(snap.Text, marker)
	})

	if err := rig.sk.api.SetRemoteControl(false); err != nil {
		t.Fatalf("SetRemoteControl(false): %v", err)
	}
	rig.Eventually("the phone blanked the terminal after remote off", func() bool {
		snap, err := rig.App().Peek(session)
		return err == nil && !strings.Contains(snap.Text, marker)
	})

	if err := rig.sk.api.SetRemoteControl(true); err != nil {
		t.Fatalf("SetRemoteControl(true): %v", err)
	}
	rig.Eventually("the terminal watch recovered after remote on", func() bool {
		snap, err := rig.App().Peek(session)
		return err == nil && strings.Contains(snap.Text, marker)
	})
}
