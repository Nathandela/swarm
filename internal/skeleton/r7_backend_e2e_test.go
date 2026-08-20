package skeleton

// FAILING-FIRST (TDD RED, GG-5) -- THE REAL ASSEMBLED PATH, and it is here because of a
// specific, twice-repeated failure. R6's headline verb was broken through the real gateway for
// a full round because every test hand-built the struct the production code assembles; R5 lost
// a whole wave to the same shape. Bead: agents-tracker-hggx.8, Mirror M4.3/M4.4.
//
// Every other R7 test in this package calls d.api.ComposerSend / d.api.ApproveInteraction
// directly. This one does not. It drives:
//
//	phonecore.SignComposerSend -> SealComposerSendEnvelope -> a REAL relay mailbox
//	  -> remotegw.Service's command loop -> OpenRemoteCommand -> protocol Server
//	  -> coreAPI.ComposerSend -> the message-sink resolution -> the backend's turn/start
//
// and the same for the approve. If the composer body is dropped, re-pointed, hashed
// differently, or the sink resolution is only reachable from a test's own call, THIS fails and
// the unit tests do not.

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// TestR7E2E_APhoneComposerSendReachesTheCodexBackendAsTurnStartThroughTheRealGateway is the
// exit-evidence shape of Wave R7 reduced to one machine: the phone drives a Codex session and
// the words arrive as an app-server RPC, not as keystrokes.
func TestR7E2E_APhoneComposerSendReachesTheCodexBackendAsTurnStartThroughTheRealGateway(t *testing.T) {
	rig := newR7E2ERig(t)

	cmd, err := phonecore.SignComposerSend(rig.ks, phonecore.ComposerSendInput{
		Machine:      rig.sk.api.endpointID,
		Session:      rig.namespaced,
		OperationID:  "devA:01JE2ESEND000000000000",
		ExpiresAt:    time.Now().Add(time.Minute),
		ExpectedTurn: "",
		Text:         "ship it",
	})
	if err != nil {
		t.Fatalf("phone sign composer_send: %v", err)
	}
	env, err := phonecore.SealComposerSendEnvelope(rig.key, 1, 100, cmd, &schema.ComposerSendReq{
		Session: rig.namespaced, ExpectedTurn: "", Text: "ship it",
	})
	if err != nil {
		t.Fatalf("phone seal composer_send: %v", err)
	}
	rig.append(t, env)
	rig.awaitOK(t, "composer_send")

	// The RPC, not a keystroke.
	params := r7CallParams(t, rig.backend, "turn/start")
	input, ok := params["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("turn/start input = %v, want a one-element ARRAY of UserInput", params["input"])
	}
	first, _ := input[0].(map[string]any)
	if first["text"] != "ship it" {
		t.Errorf("the text that reached the backend is %v, want \"ship it\". A body dropped or "+
			"altered on the gateway hop is exactly what R6's headline verb did for a whole round "+
			"while every unit test passed", first["text"])
	}
	rig.assertPTYUntouched(t)
}

// TestR7E2E_APhoneApproveResolvesTheCodexApprovalOverTheRealGateway is M4.3 through the same
// composition. It is the half of the exit criterion a unit test cannot reach: the approve op
// carries a content hash, an expiry and an agent-instance tuple that the phone echoes and the
// daemon re-derives, and every one of those is assembled on the way.
func TestR7E2E_APhoneApproveResolvesTheCodexApprovalOverTheRealGateway(t *testing.T) {
	rig := newR7E2ERig(t)
	item := openApprovalFrom(t, rig.sk, rig.local, r7CodexApproval())
	rig.sk.noteServerRequest(rig.local, "exec-29bcdedd-84f6-423c-931d-0f0433cc3328", json.RawMessage(`0`))

	m, ok := rig.sk.core.Get(rig.local)
	if !ok {
		t.Fatalf("session %s is gone", rig.local)
	}
	expires, err := time.Parse(time.RFC3339Nano, itemString(t, item, "expires_at"))
	if err != nil {
		t.Fatalf("parse expires_at: %v", err)
	}
	cmd, err := phonecore.SignApprove(rig.ks, phonecore.ApproveInput{
		Machine:     rig.sk.api.endpointID,
		Session:     rig.namespaced,
		OperationID: "devA:01JE2EAPPROVE000000000",
		ExpiresAt:   time.Now().Add(time.Minute),
		ContentHash: itemString(t, item, "content_hash"),
	})
	if err != nil {
		t.Fatalf("phone sign approve: %v", err)
	}
	env, err := phonecore.SealApproveEnvelope(rig.key, 1, 101, cmd, schema.ApproveReq{
		Session:       rig.namespaced,
		InteractionID: itemString(t, item, "item_id"),
		Decision:      "accept",
		ContentHash:   itemString(t, item, "content_hash"),
		ExpiresAt:     &expires,
		AgentInstance: schema.AgentInstanceRef{ShimPID: m.ShimPID, ShimStartTime: m.ShimStartTime},
	})
	if err != nil {
		t.Fatalf("phone seal approve: %v", err)
	}
	rig.append(t, env)
	rig.awaitOK(t, "approve")

	id, result, ok := rig.backend.lastResponse()
	if !ok {
		t.Fatal("the approve travelled the whole gateway and answered NOTHING; the agent is still " +
			"blocked on a request the phone believes it resolved")
	}
	if string(id) != "0" {
		t.Errorf("the JSON-RPC reply carries id %s, want the id the server sent (0)", id)
	}
	if !json.Valid(result) {
		t.Errorf("the reply body is not JSON: %s", result)
	}
	rig.assertPTYUntouched(t)
}

// ---------------------------------------------------------------------------
// rig: a REAL relay, a REAL gateway service, a REAL daemon, a Codex-shaped adapter, and a
// registered backend double that records the RPCs.
// ---------------------------------------------------------------------------

type r7E2ERig struct {
	sk         *Daemon
	local      string
	namespaced string
	backend    *r7FakeBackend
	ks         crypto.KeyStore
	key        crypto.ContentKey
	phoneRelay *relay.Client
	machineID  string
	ctx        context.Context
	att        *protocol.Attachment
}

func newR7E2ERig(t *testing.T) *r7E2ERig {
	t.Helper()
	rcfg := relay.DefaultConfig()
	rcfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	relaySrv, err := relay.New(rcfg)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := relaySrv.Start(ctx); err != nil {
		t.Fatalf("relay start: %v", err)
	}
	t.Cleanup(func() { _ = relaySrv.Close() })

	sk, rsock := assembleWithRemote(t)
	ks := registerPhone(t, sk, device.CapFull)

	ad := &r7CodexAdapter{Adapter: newPlainAdapter().Adapter}
	sk.adapterFor = func(string) (adapter.Adapter, bool) { return ad, true }
	meta := launchFake(t, sk, r7StdinScript)
	namespaced := protocol.NamespacedID(sk.api.endpointID, meta.ID)

	oc := dialClient(t, sk)
	att, err := oc.Attach(namespaced)
	if err != nil {
		t.Fatalf("owner attach: %v", err)
	}
	t.Cleanup(func() { _ = att.Detach() })

	backend := newR7FakeBackend()
	backend.reply["turn/start"] = json.RawMessage(
		`{"turn":{"id":"01a0033b-d0be-77e1-88e7-584ddeea562d","items":[],"itemsView":"notLoaded","status":"inProgress"}}`)
	sk.registerBackend(meta.ID, "01a00339-a80e-72a0-966f-116427b6b9ce", backend)

	mPub, mPriv, _ := ed25519.GenerateKey(nil)
	pPub, pPriv, _ := ed25519.GenerateKey(nil)
	machineRelay, err := relay.Dial(ctx, relaySrv.URL(), relayAuth(mPub, mPriv))
	if err != nil {
		t.Fatalf("machine dial: %v", err)
	}
	t.Cleanup(func() { _ = machineRelay.Close() })
	phoneRelay, err := relay.Dial(ctx, relaySrv.URL(), relayAuth(pPub, pPriv))
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phoneRelay.Close() })
	if err := machineRelay.AuthorizeDevice(ctx, pPub, e2eConsent(pPriv, relay.RoutingID(mPub))); err != nil {
		t.Fatalf("machine authorize phone: %v", err)
	}
	if err := phoneRelay.AuthorizeDevice(ctx, mPub, e2eConsent(mPriv, relay.RoutingID(pPub))); err != nil {
		t.Fatalf("phone authorize machine: %v", err)
	}

	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 23)
	}
	svc := remotegw.NewService(remotegw.ServiceConfig{
		DaemonSocket:   rsock,
		Relay:          machineRelay,
		PhoneTarget:    phoneRelay.RoutingID(),
		Key:            key,
		EpochID:        1,
		ReconnectDelay: 50 * time.Millisecond,
	})
	svcCtx, svcCancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- svc.Run(svcCtx) }()
	t.Cleanup(func() {
		svcCancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("the gateway service did not stop within 2s of cancel")
		}
	})

	return &r7E2ERig{
		sk: sk, local: meta.ID, namespaced: namespaced, backend: backend,
		ks: ks, key: key, phoneRelay: phoneRelay, machineID: machineRelay.RoutingID(),
		ctx: ctx, att: att,
	}
}

func (r *r7E2ERig) append(t *testing.T, env []byte) {
	t.Helper()
	if _, err := r.phoneRelay.MailboxAppend(r.ctx, r.machineID, env); err != nil {
		t.Fatalf("phone append: %v", err)
	}
}

// awaitOK waits for the gateway's sealed OK reply, failing loudly on a refusal -- a refusal
// arriving here is the exact class of bug this file exists for and its code must be reported.
func (r *r7E2ERig) awaitOK(t *testing.T, what string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		items, err := r.phoneRelay.MailboxRead(r.ctx, 0)
		if err != nil {
			t.Fatalf("phone mailbox read: %v", err)
		}
		for _, it := range items {
			reply, err := phonecore.OpenControlReply(r.key, it.Envelope)
			if err != nil {
				continue
			}
			switch reply.Op {
			case protocol.OpOK:
				return
			case protocol.OpError:
				t.Fatalf("the gateway relayed a refusal for the phone %s: %q / %q", what, reply.Error, reply.ErrorCode)
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("the phone never received a reply for %s; the op never completed the real round trip", what)
}

func (r *r7E2ERig) assertPTYUntouched(t *testing.T) {
	t.Helper()
	if err := r.att.Input([]byte("\n")); err != nil {
		t.Fatalf("flush the session's line discipline: %v", err)
	}
	ok, drained := awaitFrames(r.att, "got:", 20*time.Second)
	if !ok {
		t.Fatalf("the fake CLI never reported its stdin; drained %q", drained)
	}
	if idx := indexOf(drained, "ship it"); idx >= 0 {
		t.Errorf("the phone's text reached the session's PTY through the REAL gateway: %q. This is "+
			"the live defect at HEAD -- injectComposerText is called for every provider with no "+
			"seam and no provider check anywhere on the path", drained)
	}
}

func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
