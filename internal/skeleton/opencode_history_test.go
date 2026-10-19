package skeleton

import (
	"context"
	"encoding/json"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func ocHistoryPage(identity opencodebridge.Identity, mid, cursor string) opencodebridge.Snapshot {
	info, _ := json.Marshal(map[string]any{"id": identity.RootID, "directory": identity.Directory, "version": "1.19.3", "time": map[string]int64{"created": identity.NativeCreatedAtMS}})
	page := opencodebridge.Snapshot{Identity: identity, Session: info, NextCursor: cursor, Statuses: map[string]json.RawMessage{}, Messages: []json.RawMessage{}, Permissions: []json.RawMessage{}, Questions: []json.RawMessage{}, Children: []json.RawMessage{}}
	if mid != "" {
		row, _ := json.Marshal(map[string]any{"info": map[string]any{"id": mid, "sessionID": identity.RootID, "role": "user", "time": map[string]int64{"created": 1}}, "parts": []any{map[string]any{"id": "prt_" + strings.TrimPrefix(mid, "msg_"), "sessionID": identity.RootID, "messageID": mid, "type": "text", "text": "literal native content"}}})
		page.Messages = []json.RawMessage{row}
	}
	return page
}
func TestOpenCodeHistoryExportUsesExactRootAndOpaquePagination(t *testing.T) {
	id := opencodebridge.Identity{Version: 1, WorkerID: "worker", RootID: "ses_" + strings.Repeat("a", 32), Directory: "/native ü", SessionInstance: strings.Repeat("b", 32), BackendGeneration: strings.Repeat("b", 32), NativeCreatedAtMS: 123}
	pages := []opencodebridge.Snapshot{ocHistoryPage(id, "msg_"+strings.Repeat("b", 26), "opaque-cursor"), ocHistoryPage(id, "msg_"+strings.Repeat("a", 26), "")}
	n := 0
	result, err := collectOpenCodeHistory(context.Background(), id, func(_ context.Context, limit int, before string) (opencodebridge.Snapshot, error) {
		if limit != 100 {
			t.Fatal("unbounded native page")
		}
		if n == 0 && before != "" || n == 1 && before != "opaque-cursor" {
			t.Fatal("cursor invented")
		}
		p := pages[n]
		n++
		return p, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || len(result.Messages) != 2 {
		t.Fatal("history page missing")
	}
	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), "literal native content") || strings.Contains(string(raw), "jsonl") {
		t.Fatal("native history rewritten or mislabeled")
	}
}
func TestOpenCodeHistoryRefusesForeignRowsLoopsAndBounds(t *testing.T) {
	id := opencodebridge.Identity{Version: 1, WorkerID: "worker", RootID: "ses_" + strings.Repeat("a", 32), Directory: "/native", SessionInstance: strings.Repeat("b", 32), BackendGeneration: strings.Repeat("b", 32), NativeCreatedAtMS: 123}
	for _, kind := range []string{"root", "created", "part", "repeat_cursor", "duplicate", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			n := 0
			_, err := collectOpenCodeHistory(context.Background(), id, func(_ context.Context, _ int, _ string) (opencodebridge.Snapshot, error) {
				p := ocHistoryPage(id, "msg_"+strings.Repeat("a", 26), "")
				n++
				switch kind {
				case "root":
					p.RootID = "ses_" + strings.Repeat("c", 32)
				case "created":
					p.NativeCreatedAtMS++
				case "part":
					p.Messages[0] = json.RawMessage(strings.Replace(string(p.Messages[0]), `"messageID":"msg_`, `"messageID":"msg_x`, 1))
				case "repeat_cursor":
					p.Messages = []json.RawMessage{}
					p.NextCursor = "repeat"
				case "duplicate":
					if n == 1 {
						p.NextCursor = "next"
					}
				case "oversize":
					p.Messages[0] = append(p.Messages[0], []byte(strings.Repeat(" ", openCodeHistoryMaxBytes))...)
				}
				return p, nil
			})
			if err == nil {
				t.Fatal("unsafe native history accepted")
			}
		})
	}
}

type nativeTranscriptFixture struct {
	path   string
	called string
}

func (r *nativeTranscriptFixture) Resolve(m persist.Meta) resumeHistoryResult {
	return resumeHistoryResult{Outcome: resumeHistoryFound, ConversationID: m.ConversationID}
}
func (r *nativeTranscriptFixture) LocateTranscript(_ persist.Meta, id string) (string, resumeHistoryOutcome) {
	r.called = id
	return r.path, resumeHistoryFound
}
func TestOpenCodeHandsOffPreservesExactHistoryCheckoutAndSelectedTuple(t *testing.T) {
	for _, providers := range [][2]string{{"opencode", "claude"}, {"claude", "opencode"}, {"opencode", "opencode"}} {
		t.Run(providers[0]+"-to-"+providers[1], func(t *testing.T) {
			cwd := t.TempDir()
			source := handsOffSource("source", t.TempDir(), legacyClaudeID)
			source.AgentCwd = cwd
			source.AgentType = providers[0]
			if source.AgentType == "opencode" {
				source.ConversationID = "ses_" + strings.Repeat("a", 32)
				source.RequiredRuntime = persist.RequiredRuntimeOpenCode
				source.OpenCodeBinding = &persist.OpenCodeBinding{Revision: 3, Generation: strings.Repeat("b", 32), Mode: persist.OpenCodeModeCreate, Phase: persist.OpenCodePhaseReady, NativeCreatedAtMS: 123}
			}
			resolver := &nativeTranscriptFixture{path: filepath.Join(t.TempDir(), "native-history.json")}
			spec := handsOffSpec(testEndpoint, source.ID, t.TempDir())
			spec.AgentType = providers[1]
			spec.Options["model"] = "selected/model"
			spec.Options["agent"] = "plan"
			result, err := composeHandsOffLaunch(spec, testEndpoint, func(id string) (persist.Meta, bool) { return source, id == source.ID }, resolver)
			if err != nil {
				t.Fatal(err)
			}
			if result.Cwd != cwd || result.Options["model"] != "selected/model" || result.Options["agent"] != "plan" || result.SpawnedFrom != source.ID || result.SpawnIntent != protocol.SpawnIntentHandoff || resolver.called != source.ConversationID {
				t.Fatal("handoff changed native identity, checkout or tuple")
			}
			if !strings.Contains(result.InitialPrompt, resolver.path) || !strings.Contains(result.InitialPrompt, source.ConversationID) {
				t.Fatal("exact history missing from handoff")
			}
		})
	}
}

func TestOpenCodeEndedHistoryExportsExactProfileAndRefusesMissingStorage(t *testing.T) {
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(profile, "opencode"), 0700); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(profile, "opencode", "opencode.db")
	if err = os.WriteFile(db, nil, 0600); err != nil {
		t.Fatal(err)
	}
	source := handsOffSource("native", project, "ses_"+strings.Repeat("a", 32))
	source.AgentType = "opencode"
	source.LaunchOptions = map[string]string{"model": "opencode/glm-5.3-flash-free", "agent": "build"}
	source.Status.Process = status.ProcessExited
	source.RequiredRuntime = persist.RequiredRuntimeOpenCode
	source.OpenCodeBinding = &persist.OpenCodeBinding{Revision: 3, Generation: strings.Repeat("b", 32), Mode: persist.OpenCodeModeCreate, Phase: persist.OpenCodePhaseReady, NativeCreatedAtMS: 123, ProfileEnv: []string{"XDG_DATA_HOME=" + profile}}
	id := nativeHistoryIdentity(source)
	page := ocHistoryPage(id, "msg_"+strings.Repeat("m", 26), "")
	page.Session = json.RawMessage(strings.Replace(string(page.Session), `"version":"1.19.3"`, `"version":"1.19.3","agent":"plan","model":{"providerID":"opencode","id":"deepseek-v4.1-flash-free","variant":"max"}`, 1))
	payload, _ := json.Marshal(openCodeHistoryExport{Info: page.Session, Messages: page.Messages})
	bin := filepath.Join(project, "opencode")
	marker := filepath.Join(project, "exported")
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 2.0.0; exit 0; fi\n[ \"$1\" = export ] && [ \"$2\" = " + source.ConversationID + " ] || exit 91\n[ \"$XDG_DATA_HOME\" = " + strconv.Quote(profile) + " ] || exit 92\nprintf x >> " + strconv.Quote(marker) + "\ncat <<'NATIVE_HISTORY_EOF'\n" + string(payload) + "\nNATIVE_HISTORY_EOF\n"
	if err = os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := persist.CLIFingerprint(bin)
	if err != nil {
		t.Fatal(err)
	}
	source.CLIIdentity = &persist.CLIIdentity{Path: bin, Version: "2.0.0", Fingerprint: fingerprint}
	source.Env = []string{"PATH=/usr/bin:/bin"}
	api := continuityAPI(t, source)
	dir, err := filepath.EvalSymlinks(filepath.Join(api.stateDir, source.ID))
	if err != nil {
		t.Fatal(err)
	}
	binding := opencodebridge.Binding{Version: 1, WorkerID: source.ID, RootID: source.ConversationID, Directory: project, SessionInstance: source.OpenCodeBinding.Generation, BackendGeneration: source.OpenCodeBinding.Generation, Mode: "create", SocketPath: filepath.Join(dir, "oc.sock"), Token: strings.Repeat("c", 64)}
	bindingPath := filepath.Join(dir, shim.OpenCodeBindingFile)
	raw, _ := json.Marshal(binding)
	if err = os.WriteFile(bindingPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	launch := map[string]any{"session_id": source.ID, "session_dir": dir, "cwd": project, "argv": []string{bin}, "opencode_env": source.OpenCodeBinding.ProfileEnv, "opencode": shim.OpenCodeConfig{RootID: source.ConversationID, Generation: source.OpenCodeBinding.Generation, BindingFile: bindingPath, EmbargoToken: strings.Repeat("d", 64)}}
	raw, _ = json.Marshal(launch)
	if err = os.WriteFile(filepath.Join(dir, "shim-launch.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	resolver := &openCodeHistoryResolver{core: api.core}
	path, outcome := resolver.LocateTranscript(source, source.ConversationID)
	if outcome != resumeHistoryFound {
		t.Fatalf("exact export refused: %v", outcome)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("export was not private")
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var exported openCodeHistoryExport
	if json.Unmarshal(raw, &exported) != nil || exported.Provenance.Identity != id || exported.Provenance.Source != "native_cli_export" || exported.Provenance.NativeVersion != "2.0.0" || len(exported.Messages) != 1 {
		t.Fatal("export lost native identity or rows")
	}
	var historical struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(exported.Info, &historical) != nil || historical.Version != "1.19.3" {
		t.Fatal("runtime provenance rewrote historical session version")
	}
	changedIdentity := *source.CLIIdentity
	changedIdentity.Fingerprint = "different"
	changedSource := source
	changedSource.CLIIdentity = &changedIdentity
	if _, err := resolver.nativeExportCommand(context.Background(), changedSource); err == nil {
		t.Fatal("newer version bypassed exact executable fingerprint")
	}
	resume := daemon.LaunchSpec{AgentType: "opencode", Cwd: t.TempDir(), Options: map[string]string{protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, source.ID)}}
	if err := api.captureOpenCodeOrigin(&resume); err != nil {
		t.Fatalf("resume from roster/default cwd did not recover native checkout: %v", err)
	}
	if resume.Cwd != project || resume.OpenCodeBinding.NativeCreatedAtMS != 123 {
		t.Fatal("resume lost exact native cwd or creation identity")
	}
	if resume.Options["model"] != "opencode/deepseek-v4.1-flash-free" || resume.Options["agent"] != "plan" {
		t.Fatal("resume reused stale initial launch options")
	}
	changed := daemon.LaunchSpec{AgentType: "opencode", Cwd: project, Options: map[string]string{protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, source.ID), "model": "opencode/glm-5.3-flash-free"}}
	if api.captureOpenCodeOrigin(&changed) == nil {
		t.Fatal("resume silently accepted model override ignored by native history hydration")
	}
	resume.Options[protocol.OptionResumeRelocate] = "true"
	if api.captureOpenCodeOrigin(&resume) == nil {
		t.Fatal("native resume allowed an unqualified relocation")
	}
	if err = os.Remove(db); err != nil {
		t.Fatal(err)
	}
	if _, outcome = resolver.LocateTranscript(source, source.ConversationID); outcome == resumeHistoryFound {
		t.Fatal("missing native profile accepted")
	}
	calls, _ := os.ReadFile(marker)
	if string(calls) != "xxx" {
		t.Fatal("missing profile executed native export")
	}
}
