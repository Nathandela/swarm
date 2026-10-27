package schema

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestMessageOriginHashBindsEveryFieldAndPreservesLegacy(t *testing.T) {
	req := ComposerSendReq{Session: "machine/session", SessionInstance: "instance", Text: "same text"}
	legacy := ComposerSendContentHash(&req)
	origin := MessageOrigin{Kind: "external", InstallationID: "install", RequestID: "request", ActorID: "actor", ActorName: "name", GuildID: "guild", ChannelID: "channel", MessageID: "message", EndorserID: "endorser"}
	req.Origin = &origin
	bound := ComposerSendContentHash(&req)
	if bytes.Equal(legacy, bound) {
		t.Fatal("origin omitted from content binding")
	}
	raw, _ := json.Marshal(origin)
	var fields map[string]string
	_ = json.Unmarshal(raw, &fields)
	for field := range fields {
		changed := map[string]string{}
		for k, v := range fields {
			changed[k] = v
		}
		changed[field] += "changed"
		data, _ := json.Marshal(changed)
		var next MessageOrigin
		_ = json.Unmarshal(data, &next)
		req.Origin = &next
		if bytes.Equal(bound, ComposerSendContentHash(&req)) {
			t.Errorf("%s is not hash bound", field)
		}
	}
	req.Origin = nil
	if !bytes.Equal(legacy, ComposerSendContentHash(&req)) {
		t.Fatal("nil origin changed legacy signature")
	}
}

func TestMessageOriginValidation(t *testing.T) {
	valid := MessageOrigin{Kind: "external", InstallationID: "install", RequestID: "request", ActorID: "actor"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"owner_direct", "owner_endorsed"} {
		next := valid
		next.Kind = kind
		if kind == "owner_endorsed" {
			next.EndorserID = "owner"
		}
		if err := next.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, change := range []func(*MessageOrigin){
		func(o *MessageOrigin) { o.Kind = "unknown" },
		func(o *MessageOrigin) { o.InstallationID = "" },
		func(o *MessageOrigin) { o.RequestID = "" },
		func(o *MessageOrigin) { o.ActorID = "" },
		func(o *MessageOrigin) { o.ActorName = "name\nforged" },
		func(o *MessageOrigin) { o.Kind = "owner_endorsed" },
		func(o *MessageOrigin) { o.EndorserID = "owner" },
	} {
		next := valid
		change(&next)
		if next.Validate() == nil {
			t.Errorf("invalid origin accepted: %+v", next)
		}
	}
}
