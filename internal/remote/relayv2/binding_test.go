package relayv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/coder/websocket"
)

func TestPhoneBindingRejectsMachineConnection(t *testing.T) {
	if binding, err := (&Conn{role: RoleMachine}).PhoneBinding(); err == nil || binding != (Binding{}) {
		t.Fatalf("machine PhoneBinding = (%+v, %v), want zero binding and error", binding, err)
	}
	if binding, err := (&Conn{role: RolePhone}).PhoneBinding(); err == nil || binding != (Binding{}) {
		t.Fatalf("uninitialized phone PhoneBinding = (%+v, %v), want zero binding and error", binding, err)
	}
}

func TestPhoneConnectionRejectsDifferentGeneration(t *testing.T) {
	want := Binding{MachineRID: strings.Repeat("a", 32), PeerRID: strings.Repeat("b", 32), Generation: 2}
	c := &Conn{role: RolePhone, machineRID: want.MachineRID, rid: want.PeerRID, phoneBinding: want}
	if got, err := c.PhoneBinding(); err != nil || got != want {
		t.Fatalf("PhoneBinding = (%+v, %v), want %+v", got, err, want)
	}
	stale := want
	stale.Generation--
	if _, err := c.bindingPeer(stale); err == nil {
		t.Fatal("phone connection accepted a binding from an older generation")
	}
}

func TestInvalidAuthenticatedGenerationReturnsNoConnection(t *testing.T) {
	pub, priv := deterministicKey(32)
	machinePub, _ := deterministicKey(0)
	machineRID := RoutingID(machinePub)
	home := HomeID("local-test", machineRID)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		for _, response := range []map[string]any{
			{"v": 2, "type": "CHALLENGE", "nonce": encode64(make([]byte, 32)), "home": home, "expires_at": formatUint64(uint64(time.Now().Add(time.Minute).UnixMilli()))},
			{"v": 2, "type": "AUTHENTICATED", "rid": RoutingID(pub), "role": "phone", "purpose": "stream", "home": home, "generation": "0"},
		} {
			_, body, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			var request struct {
				RequestID string `json:"request_id"`
			}
			if json.Unmarshal(body, &request) != nil {
				return
			}
			response["request_id"] = request.RequestID
			body, _ = json.Marshal(response)
			if ws.Write(r.Context(), websocket.MessageText, body) != nil {
				return
			}
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := Dial(ctx, Profile{
		RelayURL: "ws" + strings.TrimPrefix(server.URL, "http"), MachineRID: machineRID,
		OperatorNamespace: "local-test", Security: relay.Security{AllowLoopbackCleartext: true},
	}, privateAuth(pub, priv, RolePhone, PurposeStream))
	if err == nil || conn != nil || !strings.Contains(err.Error(), "invalid authenticated generation") {
		t.Fatalf("Dial with zero authenticated generation = (%v, %v), want nil connection and generation error", conn, err)
	}
}

func TestSubscribeRejectsIncarnationSubstitution(t *testing.T) {
	want := "AAAAAAAAAAAAAAAAAAAAAA"
	other := "AQAAAAAAAAAAAAAAAAAAAA"
	for name, tc := range map[string]struct {
		incarnation string
		wantErr     bool
	}{
		"matching":    {want, false},
		"substituted": {other, true},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer ws.CloseNow()
				_, body, err := ws.Read(r.Context())
				if err != nil {
					return
				}
				var request struct {
					RequestID   string `json:"request_id"`
					Incarnation string `json:"incarnation"`
				}
				if json.Unmarshal(body, &request) != nil || request.Incarnation != want {
					return
				}
				response, _ := json.Marshal(map[string]any{
					"v": 2, "type": "SUBSCRIBED", "request_id": request.RequestID,
					"peer_rid": testPhoneRID, "generation": "7", "incarnation": tc.incarnation, "after": "4",
				})
				_ = ws.Write(r.Context(), websocket.MessageText, response)
			}))
			defer server.Close()
			conn, err := dialRaw(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.role, conn.purpose, conn.machineRID, conn.rid = RoleMachine, PurposeStream, testMachineRID, testMachineRID
			binding := Binding{MachineRID: testMachineRID, PeerRID: testPhoneRID, Generation: 7}
			_, err = conn.Subscribe(context.Background(), binding, Checkpoint{Incarnation: want, Cursor: 4})
			if (err != nil) != tc.wantErr {
				t.Fatalf("Subscribe response incarnation %q = %v, wantErr=%v", tc.incarnation, err, tc.wantErr)
			}
			if tc.wantErr && err.Error() != "relay v2: invalid subscription response" {
				t.Fatalf("substituted incarnation error = %q", err)
			}
		})
	}
}

func TestSubscriptionRejectsZeroAckBeforeCallingConnection(t *testing.T) {
	if err := (&Subscription{}).Ack(context.Background(), 0); err == nil {
		t.Fatal("zero ACK reached the connection")
	}
}
