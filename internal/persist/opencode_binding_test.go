package persist

import (
	"encoding/json"
	"strings"
	"testing"
)

func plannedOpenCode() OpenCodeBinding {
	return OpenCodeBinding{Revision: 1, Mode: OpenCodeModeCreate, Phase: OpenCodePhasePlanned,
		Generation: strings.Repeat("a", 32), ProfileEnv: []string{"XDG_DATA_HOME=/native/data"}}
}

func TestOpenCodeBindingRejectsAmbiguousCustody(t *testing.T) {
	valid := plannedOpenCode()
	if !ValidOpenCodeBinding(valid) {
		t.Fatal("fresh preallocated root binding rejected")
	}
	for name, change := range map[string]func(*OpenCodeBinding){
		"missing generation":      func(b *OpenCodeBinding) { b.Generation = "" },
		"zero revision":           func(b *OpenCodeBinding) { b.Revision = 0 },
		"bound without creation":  func(b *OpenCodeBinding) { b.Phase = OpenCodePhaseBound },
		"resume without creation": func(b *OpenCodeBinding) { b.Mode = OpenCodeModeResume },
		"arbitrary environment":   func(b *OpenCodeBinding) { b.ProfileEnv = []string{"EDITOR=/bin/untrusted"} },
		"credential environment":  func(b *OpenCodeBinding) { b.ProfileEnv = []string{"OPENCODE_SERVER_PASSWORD=/secret"} },
		"duplicate origin":        func(b *OpenCodeBinding) { b.ProfileEnv = []string{"XDG_DATA_HOME=/one", "XDG_DATA_HOME=/two"} },
		"equal duplicate origin":  func(b *OpenCodeBinding) { b.ProfileEnv = []string{"XDG_DATA_HOME=/one", "XDG_DATA_HOME=/one"} },
		"relative origin":         func(b *OpenCodeBinding) { b.ProfileEnv = []string{"OPENCODE_CONFIG=relative.json"} },
		"control origin":          func(b *OpenCodeBinding) { b.ProfileEnv = []string{"OPENCODE_CONFIG=/x\tfile"} },
		"noncanonical origin":     func(b *OpenCodeBinding) { b.ProfileEnv = []string{"OPENCODE_CONFIG=/x/../file"} },
	} {
		t.Run(name, func(t *testing.T) {
			b := plannedOpenCode()
			change(&b)
			if ValidOpenCodeBinding(b) {
				t.Fatal("invalid custody accepted")
			}
		})
	}
}

func TestOpenCodeInitialHasNoInventedMessageBeforeAdmission(t *testing.T) {
	b := plannedOpenCode()
	b.Initial = &OpenCodeInitial{OperationID: strings.Repeat("b", 32), RequestHash: strings.Repeat("c", 64), Phase: OpenCodeInitialPrepared}
	if !ValidOpenCodeBinding(b) {
		t.Fatal("prepared native-generated message must not require a fabricated ID")
	}
	b.Phase, b.NativeCreatedAtMS = OpenCodePhaseReady, 123
	b.Initial.Phase = OpenCodeInitialStarted
	if !ValidOpenCodeBinding(b) {
		t.Fatal("started without native receipt rejected")
	}
	b.Initial.Phase = OpenCodeInitialAdmitted
	if ValidOpenCodeBinding(b) {
		t.Fatal("admitted without native ID and tuple accepted")
	}
	b.Initial.MessageID = "msg_14f676829001tSGU02Ja2YsFVP"
	b.Initial.Agent, b.Initial.Model, b.Initial.Variant = "plan", "opencode/deepseek-v4.1-flash-free", "max"
	if !ValidOpenCodeBinding(b) {
		t.Fatal("qualified native admission rejected")
	}
	b.Initial.MessageID = "arbitrary-id"
	if ValidOpenCodeBinding(b) {
		t.Fatal("uncharacterized native message ID accepted")
	}
}

func TestOpenCodeRuntimeUsesExecutionCriticalSchemaOnly(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := plannedOpenCode()
	m := Meta{ID: "native", AgentType: "opencode", Cwd: "/project", ConversationID: "ses_" + strings.Repeat("d", 32),
		RequiredRuntime: RequiredRuntimeOpenCode, OpenCodeBinding: &b}
	if err := s.Save(m); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load(m.ID)
	if err != nil || loaded.SchemaVersion != OpenCodeSchemaVersion {
		t.Fatalf("managed schema: %+v, %v", loaded, err)
	}
	for _, version := range []int{1, 2, 4} {
		loaded.SchemaVersion = version
		raw, _ := json.Marshal(loaded)
		if _, err := decodeMeta(raw, m.ID); err == nil {
			t.Fatalf("execution-critical state accepted with schema %d", version)
		}
	}
	for name, change := range map[string]func(*Meta){
		"missing binding": func(m *Meta) { m.OpenCodeBinding = nil },
		"missing kind":    func(m *Meta) { m.RequiredRuntime = "" },
		"unknown kind":    func(m *Meta) { m.RequiredRuntime = "other" },
		"wrong provider":  func(m *Meta) { m.AgentType = "codex" },
		"wrong native ID": func(m *Meta) { m.ConversationID = "newest-by-cwd" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := m
			change(&bad)
			if s.Save(bad) == nil {
				t.Fatal("malformed runtime state accepted")
			}
		})
	}
	if err := s.Save(Meta{ID: "ordinary", AgentType: "opencode"}); err != nil {
		t.Fatal(err)
	}
	ordinary, err := s.Load("ordinary")
	if err != nil || ordinary.SchemaVersion != SchemaVersion {
		t.Fatalf("ordinary schema changed: %+v %v", ordinary, err)
	}
}
