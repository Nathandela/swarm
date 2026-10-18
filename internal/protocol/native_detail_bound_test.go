package protocol

import (
	"encoding/json"
	"github.com/Nathandela/swarm/internal/wire"
	"strings"
	"testing"
	"time"
)

func TestInteractionDetailRefusesAnUnsendableWholeFrame(t *testing.T) {
	for _, body := range []json.RawMessage{
		json.RawMessage(`{"text":"` + strings.Repeat("x", wire.MaxFrame-100) + `"}`),
		json.RawMessage(`{"text":"` + strings.Repeat("<", 200000) + `"}`),
		json.RawMessage(`{"text":`),
	} {
		b := newR6HistoryBackend()
		b.detail = body
		rc := rawDial(t, serveRemoteAPI(t, b))
		hello := rc.hello(Version, []string{CapRemoteGateway, CapJournal})
		sid := hello.EndpointID + "/sess1"
		rc.writeControl(Control{Op: OpInteractionDetail, EndpointID: hello.EndpointID, SessionID: sid, Detail: &InteractionDetailReq{Session: sid, ItemID: "native"}})
		_ = rc.conn.SetReadDeadline(time.Now().Add(time.Second))
		_, raw, err := wire.ReadFrame(rc.conn)
		if err != nil {
			t.Fatalf("oversized/malformed detail silently dropped instead of named refusal: %v", err)
		}
		got, err := DecodeControl(raw)
		if err != nil || got.Op != OpError || got.ErrorCode != CodeUnavailable || len(got.Journal) != 0 {
			t.Fatalf("whole detail refusal: %+v %v", got, err)
		}
	}
}
