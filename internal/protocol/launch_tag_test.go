package protocol

// A tag given on the new-session form must reach the session's meta the same way
// its name does: carried ON the launch request, re-validated server-side, and
// stamped by the daemon at reservation -- never applied by a follow-up set_tag,
// which would leave the session briefly untagged and could fail on its own.
//
// The field is additive and omitempty, so an untagged launch is byte-identical
// to the shape every older peer already parses.

import (
	"encoding/json"
	"testing"
)

// TestLaunchTag_ReqRoundTrip pins both directions: the field survives
// marshal/unmarshal under its snake_case key, and an untagged request emits no
// key at all.
func TestLaunchTag_ReqRoundTrip(t *testing.T) {
	t.Run("tagged", func(t *testing.T) {
		in := LaunchReq{Agent: "claude", Cwd: "/tmp", Cols: 80, Rows: 24, Tag: "release"}
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("marshal LaunchReq: %v", err)
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(b, &keys); err != nil {
			t.Fatalf("LaunchReq is not a JSON object: %v", err)
		}
		if _, ok := keys["tag"]; !ok {
			t.Fatalf("LaunchReq JSON missing snake_case key \"tag\"; got %s", b)
		}
		var got LaunchReq
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal LaunchReq: %v", err)
		}
		if got.Tag != in.Tag {
			t.Fatalf("round-tripped tag = %q, want %q", got.Tag, in.Tag)
		}
	})

	t.Run("untagged emits no key", func(t *testing.T) {
		b, err := json.Marshal(LaunchReq{Agent: "claude", Cwd: "/tmp", Cols: 80, Rows: 24})
		if err != nil {
			t.Fatalf("marshal LaunchReq: %v", err)
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(b, &keys); err != nil {
			t.Fatalf("LaunchReq is not a JSON object: %v", err)
		}
		if _, ok := keys["tag"]; ok {
			t.Fatalf("an untagged LaunchReq emitted a \"tag\" key: %s", b)
		}
	})
}

// TestLaunchTag_SpecCarriesSanitizedTag: the server re-validates the tag exactly
// as handleSetTag does (E6.6 -- a client is never trusted to have sanitized it),
// then hands it to the daemon.
func TestLaunchTag_SpecCarriesSanitizedTag(t *testing.T) {
	req := LaunchReq{Agent: "claude", Cwd: "/tmp", Cols: 80, Rows: 24, Tag: "  front\x00end\n  "}
	if got := daemonLaunchSpec(&req, false, "").Tag; got != "frontend" {
		t.Fatalf("spec tag = %q, want the sanitized frontend", got)
	}
}

// TestLaunchTag_BlankTagIsNoTag: whitespace is a typing artefact, never a group
// name -- the same rule handleSetTag already holds to.
func TestLaunchTag_BlankTagIsNoTag(t *testing.T) {
	req := LaunchReq{Agent: "claude", Cwd: "/tmp", Cols: 80, Rows: 24, Tag: "   "}
	if got := daemonLaunchSpec(&req, false, "").Tag; got != "" {
		t.Fatalf("spec tag = %q, want the empty tag", got)
	}
}
