package protocol

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/idempotency"
)

type capacityControlBackend struct{ *stubDaemon }

func (capacityControlBackend) ClaimOperation(string, string, string) (bool, error) {
	return false, idempotency.ErrCapacity
}

func TestTakeControlCapacityRefusalOpensNoLeaseAndNamesCapacity(t *testing.T) {
	backend := capacityControlBackend{newStubDaemon()}
	rc := rawDial(t, serveRemoteAPI(t, backend))
	hello := rc.hello(Version, []string{CapRemoteGateway})
	expires := time.Now().Add(time.Minute)
	rc.writeControl(Control{
		Op: OpTakeControl, EndpointID: hello.EndpointID, SessionID: hello.EndpointID + "/session",
		OperationID: "device:capacity", DeviceID: "device", DeviceSig: "signature", ExpiresAt: &expires,
		GateToken: "gate-token",
	})
	reply := nextControl(t, rc)
	if reply.Op != OpError || !strings.Contains(reply.Error, idempotency.ErrCapacity.Error()) {
		t.Fatalf("capacity refusal misreported as a consumed operation: %+v", reply)
	}
	if backend.streamCount() != 0 {
		t.Fatal("capacity refusal opened an upstream control stream")
	}
}
