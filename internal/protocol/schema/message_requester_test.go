package schema

import (
	"bytes"
	"testing"
)

func TestQuotedAuthorAndAuthenticatedRequesterAreSeparatelyBound(t *testing.T) {
	o := MessageOrigin{Kind: "external", InstallationID: "place", RequestID: "request", ActorID: "quoted-owner", RequesterID: "colleague"}
	r := ComposerSendReq{Session: "local/agent", SessionInstance: "instance", Text: "context reference", Origin: &o}
	before := ComposerSendContentHash(&r)
	o.RequesterID = "another-colleague"
	if bytes.Equal(before, ComposerSendContentHash(&r)) {
		t.Fatal("forwarding requester was not bound")
	}
	o.RequesterID = "bad\nrequester"
	if o.Validate() == nil {
		t.Fatal("requester control characters accepted")
	}
}
