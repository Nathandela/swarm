package skeleton

import (
	"encoding/json"
	"fmt"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/Nathandela/swarm/internal/persist"
)

func TestOpenCodeProjectionBoundsAndSanitizesWithoutExposingRequestAuthority(t *testing.T) {
	meta := persist.Meta{ConversationID: "ses_root", OpenCodeBinding: &persist.OpenCodeBinding{Generation: "generation", Phase: persist.OpenCodePhaseReady}}
	view := OpenCodeView{Root: "ses_root", Generation: "generation", Busy: true, BusyKnown: true, CurrentScopeKnown: true, Attention: true}
	for i := 0; i < 128; i++ {
		view.Pending = append(view.Pending, OpenCodeRequest{Kind: "permission", ID: fmt.Sprintf("per_%d", i), SessionID: "ses_root", Permission: strings.Repeat("<\x1b\n\u202e", 1000), Raw: json.RawMessage(`{"credential":"never-expose-this"}`)})
		view.Descendants = append(view.Descendants, OpenCodeDescendant{SessionID: fmt.Sprintf("ses_%d", i), ParentID: "ses_root", Title: strings.Repeat("title", 1000)})
		view.Inputs = append(view.Inputs, OpenCodeInput{MessageID: fmt.Sprintf("msg_%d", i), Text: strings.Repeat("user", 1000), Outcome: OpenCodeOutcomeCompleted, OutcomeSource: OpenCodeOutcomeSourceImported})
	}
	projection := openCodeProjection(meta, view, true)
	raw, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > maxOpenCodeProjectionBytes || !projection.Truncated || projection.PendingCount != 128 || projection.DescendantCount != 128 {
		t.Fatalf("native projection bound/counts: bytes=%d %+v", len(raw), projection)
	}
	if strings.Contains(string(raw), "never-expose-this") || strings.Contains(string(raw), "credential") {
		t.Fatal("raw authorization metadata escaped into roster")
	}
	for _, request := range projection.Pending {
		for _, r := range request.Summary {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				t.Fatal("native summary can inject terminal formatting")
			}
		}
	}
	for _, input := range projection.Inputs {
		if input.OutcomeSource != "imported" {
			t.Fatal("historical outcome lost its source label")
		}
	}
}

func TestOpenCodeProjectionPublishesEarlyIdentityWithoutInventingReadiness(t *testing.T) {
	meta := persist.Meta{ConversationID: "ses_reserved", OpenCodeBinding: &persist.OpenCodeBinding{Generation: "generation", Phase: persist.OpenCodePhasePlanned}}
	projection := openCodeProjection(meta, OpenCodeView{}, false)
	if projection.RootID != "ses_reserved" || projection.Generation != "generation" || projection.Phase != "planned" || projection.BusyKnown || projection.CurrentScopeKnown {
		t.Fatalf("planned binding invented native state: %+v", projection)
	}
}

func TestOpenCodeProjectionMarksClippedSingleInput(t *testing.T) {
	meta := persist.Meta{ConversationID: "ses_root", OpenCodeBinding: &persist.OpenCodeBinding{Generation: "generation"}}
	view := OpenCodeView{Inputs: []OpenCodeInput{{MessageID: "msg_user", Text: strings.Repeat("x", 300)}}}
	got := openCodeProjection(meta, view, true)
	if !got.Truncated || len(got.Inputs) != 1 || len(got.Inputs[0].Text) > 256 {
		t.Fatal("clipped input was presented as complete")
	}
}

func TestOpenCodeObservationOnlyChangePublishesRosterEvent(t *testing.T) {
	dir, err := os.MkdirTemp("", "ocview")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	store, err := persist.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(persist.Meta{ID: "native", AgentType: "opencode", Status: status.Status{Process: status.ProcessExited}}); err != nil {
		t.Fatal(err)
	}
	core, err := daemon.Open(daemon.Config{StateDir: dir, SocketPath: filepath.Join(dir, "d.sock"), LockPath: filepath.Join(dir, "d.lock"), LogPath: filepath.Join(dir, "d.log")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	var attention atomic.Bool
	api := newCoreAPI(core, "", "ep", nil)
	t.Cleanup(api.close)
	source := openCodeObservationFunc(func(string) (protocol.OpenCodeObservationView, bool) {
		return protocol.OpenCodeObservationView{Version: 1, RootID: "ses_root", Attention: attention.Load()}, true
	})
	api.openCodeObservationFn.Store(&source)
	api.pokeWatch()
	select {
	case <-api.events:
	case <-time.After(time.Second):
		t.Fatal("initial roster event missing")
	}
	attention.Store(true)
	api.pokeWatch()
	select {
	case got := <-api.events:
		if got.ID != "native" {
			t.Fatal("wrong session")
		}
	case <-time.After(time.Second):
		t.Fatal("native observation changed without a roster event")
	}
}
