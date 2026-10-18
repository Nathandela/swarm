package skeleton

import (
	"encoding/json"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeCommittedSelectionOverridesStaleInheritedLaunchFlags(t *testing.T) {
	selection := openCodeCommittedSelection{Known: true, Agent: "plan", Model: "opencode/deepseek-v4.1-flash-free", Variant: "max"}
	spec := daemon.LaunchSpec{Options: map[string]string{"resume_from": "machine/source"}}
	if err := applyOpenCodeCommittedSelection(&spec, selection, true); err != nil {
		t.Fatal(err)
	}
	merged := mergeResumeOptions(spec.Options, map[string]string{"model": "opencode/glm-5.3-flash-free", "agent": "build"})
	if merged["model"] != selection.Model || merged["agent"] != "plan" {
		t.Fatal("stale launch selection survived native commitment")
	}
	changed := daemon.LaunchSpec{Options: map[string]string{"model": "opencode/glm-5.3-flash-free", "agent": "build"}}
	if applyOpenCodeCommittedSelection(&changed, selection, true) == nil {
		t.Fatal("native resume silently accepted an ignored model override")
	}
	if err := applyOpenCodeCommittedSelection(&changed, selection, false); err != nil || changed.Options["model"] != "opencode/glm-5.3-flash-free" || changed.Options["agent"] != "build" {
		t.Fatal("fresh handoff lost explicit target selection")
	}
	blank := daemon.LaunchSpec{Options: map[string]string{"model": "", "agent": ""}}
	if err := applyOpenCodeCommittedSelection(&blank, selection, true); err != nil || blank.Options["model"] != "" || blank.Options["agent"] != "" {
		t.Fatal("explicit blank native restoration was rewritten")
	}
}
func TestOpenCodeCommittedSelectionRequiresExactSessionMetadata(t *testing.T) {
	identity, _ := nativeReceiptFixture()
	id := identity.Identity
	raw := json.RawMessage(`{"id":"` + id.RootID + `","directory":"` + id.Directory + `","version":"1.19.3","time":{"created":123},"agent":"plan","model":{"providerID":"opencode","id":"deepseek-v4.1-flash-free","variant":"max"}}`)
	selection, err := openCodeSelectionFromInfo(raw, id)
	if err != nil || !selection.Known || selection.Agent != "plan" || selection.Model != "opencode/deepseek-v4.1-flash-free" || selection.Variant != "max" {
		t.Fatalf("committed native tuple lost: %+v %v", selection, err)
	}
	for _, change := range []struct{ old, new string }{{`"created":123`, `"created":124`}, {`"directory":"/project"`, `"directory":"/other"`}, {`"agent":"plan"`, `"agent":""`}, {`"id":"deepseek-v4.1-flash-free"`, `"id":""`}} {
		if _, err := openCodeSelectionFromInfo(json.RawMessage(strings.Replace(string(raw), change.old, change.new, 1)), id); err == nil {
			t.Fatal("unconfirmed native selection accepted")
		}
	}
	raw = json.RawMessage(`{"id":"` + id.RootID + `","directory":"` + id.Directory + `","version":"1.19.3","time":{"created":123}}`)
	selection, err = openCodeSelectionFromInfo(raw, id)
	if err != nil || selection.Known {
		t.Fatal("blank native selection invented a default")
	}
}

func TestOpenCodeHandsOffInheritsCommittedNativeSelection(t *testing.T) {
	source := handsOffSource("source", t.TempDir(), "ses_"+strings.Repeat("a", 32))
	source.AgentType = "opencode"
	source.RequiredRuntime = persist.RequiredRuntimeOpenCode
	source.OpenCodeBinding = &persist.OpenCodeBinding{Revision: 3, Generation: strings.Repeat("b", 32), Mode: persist.OpenCodeModeCreate, Phase: persist.OpenCodePhaseReady, NativeCreatedAtMS: 123}
	source.LaunchOptions = map[string]string{"model": "opencode/glm-5.3-flash-free", "agent": "build"}
	id := nativeHistoryIdentity(source)
	page := ocHistoryPage(id, "", "")
	page.Session = json.RawMessage(strings.Replace(string(page.Session), `"version":"1.19.3"`, `"version":"1.19.3","agent":"plan","model":{"providerID":"opencode","id":"deepseek-v4.1-flash-free","variant":"max"}`, 1))
	export := openCodeHistoryExport{Info: page.Session, Messages: page.Messages, Provenance: openCodeHistoryProvenance{Identity: id, Source: "live_sdk", NativeVersion: "1.19.3"}}
	raw, _ := json.Marshal(export)
	path := filepath.Join(t.TempDir(), "native-history.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	spec := handsOffSpec(testEndpoint, source.ID, t.TempDir())
	spec.AgentType = "opencode"
	delete(spec.Options, "model")
	delete(spec.Options, "agent")
	resolver := &nativeTranscriptFixture{path: path}
	got, err := composeHandsOffLaunch(spec, testEndpoint, func(id string) (persist.Meta, bool) { return source, id == source.ID }, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if got.Options["model"] != "opencode/deepseek-v4.1-flash-free" || got.Options["agent"] != "plan" || got.SpawnIntent != protocol.SpawnIntentHandoff {
		t.Fatal("handoff did not preserve latest committed native choices")
	}
	export.Provenance.Identity.NativeCreatedAtMS++
	raw, _ = json.Marshal(export)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = composeHandsOffLaunch(spec, testEndpoint, func(id string) (persist.Meta, bool) { return source, id == source.ID }, resolver); err == nil {
		t.Fatal("foreign exported selection accepted")
	}
}
