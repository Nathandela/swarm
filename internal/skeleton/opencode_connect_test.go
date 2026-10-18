package skeleton

import (
	"context"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func TestOpenCodeManagedRegistrationCannotAcceptHeuristicOrHookStatus(t *testing.T) {
	for _, phase := range []string{persist.OpenCodePhasePlanned, persist.OpenCodePhaseReady} {
		t.Run(phase, func(t *testing.T) {
			d := &Daemon{eng: engine.New(engine.Config{})}
			d.registerSession(persist.Meta{ID: "native", AgentType: "opencode", RequiredRuntime: persist.RequiredRuntimeOpenCode, OpenCodeBinding: &persist.OpenCodeBinding{Phase: phase}, Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown}}, "native-token")
			if err := d.eng.AuthenticateCallback(engine.Callback{SessionID: "native", Token: "native-token", Sequence: 1}); err == nil {
				t.Fatal("managed native runtime retained another status producer")
			}
		})
	}
}

func TestOpenCodeReadyWaitsForAssemblyWithoutReadingCore(t *testing.T) {
	d := &Daemon{ready: make(chan struct{}), closing: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := d.openCodeReady(ctx, persist.Meta{ID: "native"}); err == nil {
		t.Fatal("observer passed before core assembly")
	}
}

func TestOpenCodeViewCopiesRetainedProjection(t *testing.T) {
	retained := OpenCodeView{Epoch: 1, Root: "ses_root", Generation: "generation", Inputs: []OpenCodeInput{{MessageID: "msg_native", Text: "original"}}, Current: &OpenCodeInput{MessageID: "msg_native", Text: "original"}}
	first := cloneOpenCodeView(retained)
	first.Inputs[0].Text = "changed"
	first.Current.Text = "changed"
	second := cloneOpenCodeView(retained)
	if second.Inputs[0].Text != "original" || second.Current.Text != "original" {
		t.Fatal("view mutation changed retained native facts")
	}
}
