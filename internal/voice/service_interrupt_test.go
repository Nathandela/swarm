package voice

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestManagerInterruptControlRejectsStaleBinding(t *testing.T) {
	c, s, _, b := provenControllerFixture(t)
	control := &managerControl{store: s, binding: b, controller: c}
	stale := b
	stale.Generation++
	w := recoveryRequest(t, control, http.MethodPost, "/v1/recovery/interrupt", map[string]any{
		"expected": stale, "native_input_id": strings.Repeat("a", 64), "native_turn_id": "main-turn",
	})
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale owner interruption status = %d", w.Code)
	}
}

type interruptControlRuntime struct {
	*controllerRuntimeFixture
	modes []bool
}

func (r *interruptControlRuntime) ManagerInterrupt(_ context.Context, req protocol.ManagerInterruptReq, statusOnly bool) (protocol.ManagerInterruptReceipt, error) {
	r.modes = append(r.modes, statusOnly)
	return protocol.ManagerInterruptReceipt{OperationID: protocol.ManagerInterruptOperationID(req), NativeInputID: req.NativeInputID,
		NativeTurnID: req.NativeTurnID, State: "pending"}, nil
}

func TestManagerInterruptControlUsesExactOwnerCoordinates(t *testing.T) {
	c, s, runtime, b := provenControllerFixture(t)
	if err := c.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	r := &interruptControlRuntime{controllerRuntimeFixture: runtime}
	c.runtime = r
	control := &managerControl{store: s, binding: b, controller: c}
	status, err := control.status()
	if err != nil || status.NativeInputID != runtime.status.Turn.NativeInputID || status.NativeTurnID != "main-turn" {
		t.Fatalf("missing original interrupt target: %+v %v", status, err)
	}
	body := map[string]any{"expected": status.Binding, "native_input_id": status.NativeInputID, "native_turn_id": status.NativeTurnID}
	for _, path := range []string{"/v1/recovery/interrupt", "/v1/recovery/interrupt-status"} {
		w := recoveryRequest(t, control, http.MethodPost, path, body)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"state":"pending"`) {
			t.Fatalf("%s became an unqualified success: %d %s", path, w.Code, w.Body)
		}
	}
	if len(r.modes) != 2 || r.modes[0] || !r.modes[1] {
		t.Fatalf("status route dispatched another stop: %v", r.modes)
	}
}
