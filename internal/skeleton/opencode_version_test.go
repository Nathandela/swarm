package skeleton

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestOpenCodeHistoricalVersionMinimum(t *testing.T) {
	intent, _ := nativeReceiptFixture()
	for _, tc := range []struct {
		version string
		allowed bool
	}{{"1.19.3", true}, {"1.19.4", true}, {"1.20.0", true}, {"2.0.0", true}, {"1.19.2", false}, {"1.19.3-rc.1", false}, {"1.20.0+build", false}, {"01.20.0", false}, {"1.20", false}, {"invalid", false}} {
		t.Run(tc.version, func(t *testing.T) {
			page := ocHistoryPage(intent.Identity, "", "")
			page.Session = json.RawMessage(strings.Replace(string(page.Session), "1.19.3", tc.version, 1))
			_, historyErr := collectOpenCodeHistory(context.Background(), intent.Identity, func(context.Context, int, string) (opencodebridge.Snapshot, error) { return page, nil })
			_, selectionErr := openCodeSelectionFromInfo(page.Session, intent.Identity)
			if (historyErr == nil) != tc.allowed || (selectionErr == nil) != tc.allowed {
				t.Fatalf("historical version %q history=%v selection=%v, allowed=%v", tc.version, historyErr, selectionErr, tc.allowed)
			}
		})
	}
}

func TestOpenCodeLostAdmissionAcceptsNewerRuntimeWithoutReplay(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	f.dropReply.Store(true)
	id, err := protocol.NewSemanticOperationID()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.client.SemanticSend(id, f.request); err == nil {
		t.Fatal("lost response became confirmed admission")
	}
	f.rowAvailable.Store(true)
	for _, version := range []string{"1.19.2", "2.0.0-rc.1", "2.0.0+build"} {
		f.nativeVersion.Store(version)
		receipt, err := f.client.SemanticOperation(f.request.Session, id)
		if err != nil || receipt.State != "uncertain" || f.calls.Load() != 1 {
			t.Fatalf("unsupported native %q recovered admission or replayed: %+v %v", version, receipt, err)
		}
	}
	f.nativeVersion.Store("2.0.0")
	receipt, err := f.client.SemanticOperation(f.request.Session, id)
	if err != nil || receipt.State != "admitted" || f.calls.Load() != 1 {
		t.Fatalf("newer native runtime lost exact admission or replayed: %+v %v calls=%d", receipt, err, f.calls.Load())
	}
}

func TestOpenCodeLiveHistoryProvenanceUsesObservedRuntimeVersion(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	meta, _ := f.d.core.Get(f.binding.WorkerID)
	resolver := &openCodeHistoryResolver{core: f.d.core}
	for _, version := range []string{"1.19.2", "2.0.0-rc.1", "2.0.0+build"} {
		f.nativeVersion.Store(version)
		if _, outcome := resolver.LocateTranscript(meta, meta.ConversationID); outcome == resumeHistoryFound {
			t.Fatalf("unsupported native runtime %q exported history", version)
		}
	}
	f.nativeVersion.Store("2.0.0")
	path, outcome := resolver.LocateTranscript(meta, meta.ConversationID)
	if outcome != resumeHistoryFound {
		t.Fatalf("newer runtime history refused: %v", outcome)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var exported openCodeHistoryExport
	if err := json.Unmarshal(raw, &exported); err != nil {
		t.Fatal(err)
	}
	var info struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(exported.Info, &info); err != nil {
		t.Fatal(err)
	}
	if exported.Provenance.NativeVersion != "2.0.0" || info.Version != "1.19.3" {
		t.Fatalf("runtime and historical versions conflated: %+v historical=%s", exported.Provenance, info.Version)
	}
}
