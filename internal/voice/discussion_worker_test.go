package voice

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestDiscussionNativeTurnRequiresExactSendReceipt(t *testing.T) {
	worker := WorkerAssociation{Session: "local/worker", SessionInstance: "instance", JobID: "job", AttemptID: "attempt", ActionID: "action"}
	request := controllerActionRequest{Tool: "swarm_send", Origin: &schema.MessageOrigin{}, Send: &protocol.ComposerSendReq{Session: worker.Session, SessionInstance: worker.SessionInstance, Text: "authorized"}}
	raw, _ := json.Marshal(request)
	action := Action{ID: worker.ActionID, OperationID: "operation", State: "settled", Request: raw}
	receipt := schema.SemanticMessageReceipt{OperationID: action.OperationID, Session: worker.Session, SessionInstance: worker.SessionInstance, NativeTurnID: "turn"}
	receipt.State = "submitted"
	action.Receipt, _ = json.Marshal(receipt)
	c := &Controller{}
	if _, err := c.discussionWorkerEvidence(context.Background(), worker, action); !errors.Is(err, ErrControllerNativeUnavailable) {
		t.Fatalf("submission without native admission: %v", err)
	}
	receipt.State, receipt.NativeInputID, receipt.SessionInstance = "admitted", "native-input", "private"
	action.Receipt, _ = json.Marshal(receipt)
	if _, err := c.discussionWorkerEvidence(context.Background(), worker, action); !errors.Is(err, ErrControllerNativeUnavailable) {
		t.Fatalf("foreign instance: %v", err)
	}
}
