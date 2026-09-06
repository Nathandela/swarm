package relayv2

import "testing"

func TestItemOwnsMailboxDelivery(t *testing.T) {
	item := Item{Cursor: 1, Envelope: []byte("ciphertext")}
	if item.Cursor != 1 || string(item.Envelope) != "ciphertext" {
		t.Fatalf("Item = %+v", item)
	}
}
