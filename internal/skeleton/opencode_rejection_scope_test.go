package skeleton

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestOpenCodePromptRejectionRequiresExactDispatchCoordinates(t *testing.T) {
	for _, phone := range []bool{false, true} {
		source := "owner"
		if phone {
			source = "phone"
		}
		for _, field := range []string{"exact", "kind", "target", "message", "missing_message", "request"} {
			t.Run(source+"/"+field, func(t *testing.T) {
				f := newOpenCodeSendFixture(t, func(f *openCodeSendFixture) {
					f.promptReceipt = func(_ opencodebridge.DispatchRequest, r opencodebridge.Receipt) opencodebridge.Receipt {
						r.Status, r.Reason = "rejected", "native_busy"
						switch field {
						case "kind":
							r.Kind = "permission_reply"
						case "target":
							r.TargetID = "ses_other"
						case "message":
							r.MessageID = "msg_other"
						case "missing_message":
							r.MessageID = ""
						case "request":
							r.RequestID = "per_other"
						}
						return r
					}
				})
				send := func() (string, protocol.ErrorCode, error) {
					id, err := protocol.NewSemanticOperationID()
					if err != nil {
						t.Fatal(err)
					}
					var code protocol.ErrorCode
					if phone {
						f.enablePhoneComposer(t)
						code, err = f.phoneSend(t, id)
					} else {
						err = f.client.SemanticSend(id, f.request)
					}
					return id, code, err
				}
				id, code, err := send()
				want := protocol.CodeOutcomeUnknown
				if field == "exact" {
					want = protocol.CodeInputBusy
				}
				if err == nil || phone && code != want || !phone && !strings.Contains(err.Error(), string(want)) {
					t.Fatalf("rejection scope became %s %v; want %s", code, err, want)
				}
				if field == "exact" {
					return
				}
				record, _ := f.d.core.ComposerOperation(id)
				if record.Phase != idempotency.PhaseExecuting || len(record.Request) == 0 || len(record.Evidence) != 0 {
					t.Fatalf("mismatched rejection released native execution custody: %+v", record)
				}
				if _, _, err := send(); err == nil || f.calls.Load() != 1 {
					t.Fatalf("replacement operation overtook unconfirmed rejection: err=%v writes=%d", err, f.calls.Load())
				}
			})
		}
	}
}
