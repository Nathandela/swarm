package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/relayv2"
	"github.com/Nathandela/swarm/internal/remotegw"
	"github.com/coder/websocket"
)

func nativeMachineParams(t *testing.T, serverURL string) gatewayParams {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	machineRID := relayv2.RoutingID(pub)
	return gatewayParams{
		RelayAuth: relayv2.Auth{PublicKey: pub, Sign: func(message []byte) ([]byte, error) {
			return ed25519.Sign(priv, message), nil
		}},
		RelayV2Profile: relayv2.Profile{
			RelayURL: serverURL, MachineRID: machineRID, OperatorNamespace: "owner",
			Security: relay.Security{AllowLoopbackCleartext: true},
		},
	}
}

func answerNativeMachineAuth(ctx context.Context, ws *websocket.Conn, p gatewayParams, purpose string) error {
	_, body, err := ws.Read(ctx)
	if err != nil {
		return err
	}
	var init struct {
		RequestID string `json:"request_id"`
		Role      string `json:"role"`
		Purpose   string `json:"purpose"`
		PublicKey string `json:"pub"`
	}
	if err := json.Unmarshal(body, &init); err != nil {
		return err
	}
	if init.Role != "machine" || init.Purpose != purpose || init.PublicKey != base64.RawURLEncoding.EncodeToString(p.RelayAuth.PublicKey) {
		return fmt.Errorf("AUTH_INIT = %+v", init)
	}
	home := relayv2.HomeID("owner", p.RelayV2Profile.MachineRID)
	challenge, _ := json.Marshal(map[string]any{
		"v": 2, "type": "CHALLENGE", "request_id": init.RequestID,
		"nonce": base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "home": home,
		"expires_at": strconv.FormatInt(time.Now().Add(30*time.Second).UnixMilli(), 10),
	})
	if err := ws.Write(ctx, websocket.MessageText, challenge); err != nil {
		return err
	}
	_, body, err = ws.Read(ctx)
	if err != nil {
		return err
	}
	var prove struct {
		RequestID string `json:"request_id"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(body, &prove); err != nil || prove.Signature == "" {
		return fmt.Errorf("AUTH_PROVE: %w", err)
	}
	authed, _ := json.Marshal(map[string]any{
		"v": 2, "type": "AUTHENTICATED", "request_id": prove.RequestID,
		"rid": p.RelayV2Profile.MachineRID, "role": "machine", "purpose": purpose, "home": home,
	})
	return ws.Write(ctx, websocket.MessageText, authed)
}

func TestAuthorizeRelayV2SendsPersistedPhoneAuthorityAndClosesControl(t *testing.T) {
	phonePub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	consent := []byte("persisted-consent")
	serverErr := make(chan error, 1)
	closed := make(chan struct{})
	var p gatewayParams
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			serverErr <- err
			return
		}
		defer ws.CloseNow()
		if err := answerNativeMachineAuth(r.Context(), ws, p, "control"); err != nil {
			serverErr <- err
			return
		}
		_, body, err := ws.Read(r.Context())
		if err != nil {
			serverErr <- err
			return
		}
		var authorize struct {
			RequestID string `json:"request_id"`
			PhonePub  string `json:"phone_pub"`
			Consent   string `json:"consent"`
		}
		if err := json.Unmarshal(body, &authorize); err != nil {
			serverErr <- err
			return
		}
		if authorize.PhonePub != base64.RawURLEncoding.EncodeToString(phonePub) || authorize.Consent != base64.RawURLEncoding.EncodeToString(consent) {
			serverErr <- fmt.Errorf("AUTHORIZE = %+v", authorize)
			return
		}
		response, _ := json.Marshal(map[string]any{
			"v": 2, "type": "AUTHORIZED", "request_id": authorize.RequestID,
			"phone_rid": relayv2.RoutingID(phonePub), "generation": "7",
		})
		if err := ws.Write(r.Context(), websocket.MessageText, response); err != nil {
			serverErr <- err
			return
		}
		_, _, _ = ws.Read(r.Context())
		close(closed)
	}))
	defer server.Close()
	p = nativeMachineParams(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	p.DeviceRelayAuthPub, p.DeviceConsentSig = phonePub, consent
	binding, err := authorizeRelayV2(context.Background(), p)
	if err != nil || binding.PeerRID != relayv2.RoutingID(phonePub) || binding.Generation != 7 {
		t.Fatalf("authorizeRelayV2 = (%+v, %v)", binding, err)
	}
	select {
	case err := <-serverErr:
		t.Fatal(err)
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("control connection remained open after authorization")
	}
}

func TestConnectRelayV2PersistsBindingAndIncarnationBeforeReturningMailbox(t *testing.T) {
	state, err := remotegw.OpenInboundState("", "machine")
	if err != nil {
		t.Fatal(err)
	}
	stream := remotegw.InboundStream{Epoch: 3}
	if err := state.Save(remotegw.InboundCheckpoint{
		Cursor: 9, Incarnation: "0123456789abcdef0123456789abcdef", Highest: map[remotegw.InboundStream]uint64{stream: 4},
	}); err != nil {
		t.Fatal(err)
	}
	requestSeen := make(chan struct {
		after, incarnation string
	}, 1)
	serverErr := make(chan error, 1)
	var p gatewayParams
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			serverErr <- err
			return
		}
		defer ws.CloseNow()
		if err := answerNativeMachineAuth(r.Context(), ws, p, "stream"); err != nil {
			serverErr <- err
			return
		}
		_, body, err := ws.Read(r.Context())
		if err != nil {
			serverErr <- err
			return
		}
		var subscribe struct {
			RequestID   string `json:"request_id"`
			PeerRID     string `json:"peer_rid"`
			Generation  string `json:"generation"`
			After       string `json:"after"`
			Incarnation string `json:"incarnation"`
		}
		if err := json.Unmarshal(body, &subscribe); err != nil {
			serverErr <- err
			return
		}
		requestSeen <- struct{ after, incarnation string }{subscribe.After, subscribe.Incarnation}
		response, _ := json.Marshal(map[string]any{
			"v": 2, "type": "SUBSCRIBED", "request_id": subscribe.RequestID,
			"peer_rid": subscribe.PeerRID, "generation": subscribe.Generation,
			"incarnation": "AAAAAAAAAAAAAAAAAAAAAA", "after": subscribe.After,
		})
		if err := ws.Write(r.Context(), websocket.MessageText, response); err != nil {
			serverErr <- err
			return
		}
		_, _, _ = ws.Read(r.Context())
	}))
	defer server.Close()
	p = nativeMachineParams(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	p.Inbound = state
	binding := relayv2.Binding{MachineRID: p.RelayV2Profile.MachineRID, PeerRID: "6019466df50bcada1f8bcd23f7a9e4ee", Generation: 7}
	mailbox, closeMailbox, err := connectRelayV2(context.Background(), p, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer closeMailbox()
	if mailbox == nil {
		t.Fatal("nil machine mailbox")
	}
	request := <-requestSeen
	if request.after != "0" || request.incarnation != "" {
		t.Fatalf("SUBSCRIBE resumed stale coordinates: %+v", request)
	}
	checkpoint := state.Load()
	wantAuthority := remotegw.RelayAuthority{
		Home: relayv2.HomeID("owner", p.RelayV2Profile.MachineRID), PhoneRID: binding.PeerRID, Generation: 7,
	}
	if checkpoint.Relay != wantAuthority || checkpoint.Cursor != 0 || checkpoint.Incarnation != "AAAAAAAAAAAAAAAAAAAAAA" || checkpoint.Highest[stream] != 4 {
		t.Fatalf("durable native checkpoint = %+v", checkpoint)
	}
	select {
	case err := <-serverErr:
		t.Fatal(err)
	default:
	}
}
