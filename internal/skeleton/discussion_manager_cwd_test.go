package skeleton

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
)

func TestParkedManagerUsesOnlyVerifiedNativeWorkspace(t *testing.T) {
	const thread = "019c0000-0000-7000-8000-000000000001"
	m := persist.Meta{Cwd: "/requested", AgentCwd: "/work/manager", ConversationID: thread}
	args := []string{"resume", thread, "-c", "check_for_update_on_startup=false", "--cd", "/work/manager", "--model", "gpt"}
	valid := []string{"resume", thread, "-c", "check_for_update_on_startup=false", "--model", "gpt"}
	for _, tc := range []struct {
		name, id, cwd   string
		args            []string
		readFails, want bool
	}{
		{"same native workspace", thread, m.AgentCwd, args, false, true},
		{"requested path differs from effective directory", thread, m.Cwd, args, false, false},
		{"unavailable native directory", thread, "", args, false, false},
		{"unreadable native thread", thread, m.AgentCwd, args, true, false},
		{"wrong thread receipt", "different", m.AgentCwd, args, false, false},
		{"different requested directory", thread, m.AgentCwd, []string{"resume", thread, "--cd", "/other"}, false, false},
		{"second cd ambiguity", thread, m.AgentCwd, append(append([]string{}, args...), "--cd", m.AgentCwd), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := &codexManagerTestConn{call: func(_ context.Context, method string, params, out any) error {
				if method != "thread/read" || !reflect.DeepEqual(params, map[string]any{"threadId": thread, "includeTurns": false}) {
					t.Fatal("unexpected native workspace request", method, params)
				}
				if tc.readFails {
					return errors.New("unavailable")
				}
				raw, err := json.Marshal(map[string]any{"thread": map[string]any{"id": tc.id, "cwd": tc.cwd}})
				if err != nil {
					return err
				}
				return json.Unmarshal(raw, out)
			}}
			got, ok := verifiedManagerResumeArgs(conn, m, tc.args)
			if ok != tc.want || ok && !reflect.DeepEqual(got, valid) {
				t.Fatal(got, ok)
			}
		})
	}
}

func TestParkedManagerRequiresEffectiveWorkspaceAfterAttach(t *testing.T) {
	const thread = "019c0000-0000-7000-8000-000000000001"
	const cwd = "/work/manager"
	for _, effective := range []string{"", "/different", cwd} {
		conn := &codexManagerTestConn{call: func(_ context.Context, method string, params, out any) error {
			if method != "thread/resume" {
				t.Fatal(method)
			}
			raw, _ := json.Marshal(map[string]any{"thread": map[string]any{"id": thread, "cwd": cwd}, "cwd": effective})
			return json.Unmarshal(raw, out)
		}}
		ok, err := new(Daemon).resumeThreadOnce(conn, thread, &persist.Meta{Cwd: cwd})
		if (err == nil) != (effective == cwd) || ok != (effective == cwd) {
			t.Fatalf("effective cwd %q accepted %t, %v", effective, ok, err)
		}
	}
}

func TestManagerWorkspaceInheritanceIsQualifiedAndScoped(t *testing.T) {
	m := persist.Meta{AgentType: "codex", Role: "manager", ResumedFrom: "parked", ManagerConversationID: "discord-teammate-one"}
	for _, version := range []string{"0.169.0", "0.170.0", "0.171.0", "unknown"} {
		if got := managerInheritsResumeWorkspace(m, "swarm/"+version+" (Linux)"); got != (version == "0.170.0" || version == "0.171.0") {
			t.Fatalf("version %s: %t", version, got)
		}
	}
	for _, edit := range []func(*persist.Meta){func(v *persist.Meta) { v.Role = "worker" }, func(v *persist.Meta) { v.ResumedFrom = "" }, func(v *persist.Meta) { v.ManagerConversationID = "owner-direct" }, func(v *persist.Meta) { v.AgentType = "claude" }} {
		v := m
		edit(&v)
		if managerInheritsResumeWorkspace(v, "swarm/0.170.0 (Linux)") {
			t.Fatal("unexpected scope", v.Role, v.ManagerConversationID)
		}
	}
}
