package schema

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

func TestComposerSendContentHash_EmptyInstanceRetainsLegacyThreeFieldSignature(t *testing.T) {
	req := &ComposerSendReq{Session: "machine/session", ExpectedTurn: "turn-a", Text: "hello"}
	h := sha256.New()
	writeHashField(h, []byte(req.Session))
	writeHashField(h, []byte(req.ExpectedTurn))
	writeHashField(h, []byte(req.Text))
	legacy := h.Sum(nil)
	if got := ComposerSendContentHash(req); !bytes.Equal(got, legacy) {
		t.Fatalf("empty-instance hash = %x, want legacy three-field hash %x", got, legacy)
	}
}

func TestComposerSendContentHash_NonEmptyInstanceIsBound(t *testing.T) {
	base := ComposerSendReq{Session: "machine/session", ExpectedTurn: "turn-a", Text: "hello"}
	first := base
	first.SessionInstance = "instance-a"
	second := base
	second.SessionInstance = "instance-b"
	legacy := ComposerSendContentHash(&base)
	firstHash := ComposerSendContentHash(&first)
	secondHash := ComposerSendContentHash(&second)
	if bytes.Equal(firstHash, legacy) {
		t.Fatal("non-empty session_instance did not change the signed content hash")
	}
	if bytes.Equal(firstHash, secondHash) {
		t.Fatal("two session incarnations produced the same signed content hash")
	}
}
