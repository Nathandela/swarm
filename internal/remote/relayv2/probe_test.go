package relayv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

func TestSubscriptionProbeReturnsDeliveriesOrderedBeforeBarrier(t *testing.T) {
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
			RequestID string `json:"request_id"`
			Type      string `json:"type"`
		}
		if json.Unmarshal(body, &request) != nil || request.Type != "PROBE" {
			return
		}
		for _, response := range []map[string]any{
			{
				"v": 2, "type": "DELIVER", "request_id": "delivery-1", "peer_rid": testPhoneRID,
				"generation": "7", "incarnation": testIncarnation, "cursor": "1", "msg_id": "m1", "ciphertext": encode64([]byte("one")),
			},
			{
				"v": 2, "type": "PROBED", "request_id": request.RequestID, "peer_rid": testPhoneRID,
				"generation": "7", "incarnation": testIncarnation,
			},
		} {
			body, _ := json.Marshal(response)
			if ws.Write(r.Context(), websocket.MessageText, body) != nil {
				return
			}
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	c, sub := testSubscription(t, server)
	defer c.Close()
	deliveries, err := sub.Probe(context.Background())
	if err != nil || len(deliveries) != 1 || deliveries[0].Cursor != 1 || string(deliveries[0].Ciphertext) != "one" {
		t.Fatalf("Probe = (%+v, %v), want ordered delivery", deliveries, err)
	}
}

func TestSubscriptionProbeRejectsSubstitutedResponse(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"peer":        func(response map[string]any) { response["peer_rid"] = testMachineRID },
		"generation":  func(response map[string]any) { response["generation"] = "8" },
		"incarnation": func(response map[string]any) { response["incarnation"] = "AQAAAAAAAAAAAAAAAAAAAA" },
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
					RequestID string `json:"request_id"`
				}
				if json.Unmarshal(body, &request) != nil {
					return
				}
				response := map[string]any{
					"v": 2, "type": "PROBED", "request_id": request.RequestID, "peer_rid": testPhoneRID,
					"generation": "7", "incarnation": testIncarnation,
				}
				mutate(response)
				body, _ = json.Marshal(response)
				if ws.Write(r.Context(), websocket.MessageText, body) == nil {
					<-r.Context().Done()
				}
			}))
			defer server.Close()

			c, sub := testSubscription(t, server)
			defer c.Close()
			if _, err := sub.Probe(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid probe response") {
				t.Fatalf("Probe substituted %s = %v", name, err)
			}
		})
	}
}

func TestConnectionDiscardAcceptsRetiredSubscriptionIncarnation(t *testing.T) {
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
			Type        string `json:"type"`
			Incarnation string `json:"incarnation"`
		}
		if json.Unmarshal(body, &request) != nil || request.Type != "DISCARD" || request.Incarnation != testIncarnation {
			return
		}
		response, _ := json.Marshal(map[string]any{
			"v": 2, "type": "DISCARDED", "request_id": request.RequestID, "peer_rid": testPhoneRID,
			"generation": "7", "incarnation": "AQAAAAAAAAAAAAAAAAAAAA", "cursor": "9",
		})
		if ws.Write(r.Context(), websocket.MessageText, response) == nil {
			<-r.Context().Done()
		}
	}))
	defer server.Close()

	c, sub := testSubscription(t, server)
	defer c.Close()
	want := Checkpoint{Incarnation: "AQAAAAAAAAAAAAAAAAAAAA", Cursor: 9}
	if got, err := c.Discard(context.Background(), sub.binding, sub.incarnation); err != nil || got != want {
		t.Fatalf("Conn.Discard = (%+v, %v), want %+v", got, err, want)
	}
	select {
	case <-c.Done():
	default:
		t.Fatal("successful discard left the old-incarnation connection usable")
	}
}

func testSubscription(t *testing.T, server *httptest.Server) (*Conn, *Subscription) {
	t.Helper()
	c, err := dialRaw(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	c.role, c.purpose, c.machineRID, c.rid = RoleMachine, PurposeStream, testMachineRID, testMachineRID
	binding := Binding{MachineRID: testMachineRID, PeerRID: testPhoneRID, Generation: 7}
	return c, &Subscription{conn: c, binding: binding, peer: testPhoneRID, incarnation: testIncarnation}
}
