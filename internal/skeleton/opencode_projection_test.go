package skeleton

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
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
