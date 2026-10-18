package skeleton

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func nativeRequestFixture(t *testing.T, session, id, kind string) OpenCodeRequest {
	t.Helper()
	var raw string
	if kind == "permission" {
		raw = `{"id":"` + id + `","sessionID":"` + session + `","permission":"bash","patterns":["printf marker"],"always":["printf *"],"metadata":{"command":"printf marker"}}`
	} else {
		raw = `{"id":"` + id + `","sessionID":"` + session + `","questions":[{"question":"Choose","header":"Fixture","options":[{"label":"A","description":"First"},{"label":"B","description":"Second"}],"multiple":true}]}`
	}
	request, err := decodeOpenCodeRequest(kind, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestNativeRequestCardsKeepMultipleExactAuthoritiesAndExpiry(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	now := time.Now().UTC()
	var clock atomic.Value
	clock.Store(now)
	f.d.nativeRequests.mu.Lock()
	f.d.nativeRequests.now = func() time.Time { return clock.Load().(time.Time) }
	f.d.nativeRequests.mu.Unlock()
	view := OpenCodeView{Root: f.identity.RootID, Generation: f.identity.BackendGeneration, CurrentScopeKnown: true,
		Pending: []OpenCodeRequest{nativeRequestFixture(t, f.identity.RootID, "per_one", "permission"), nativeRequestFixture(t, "ses_childaaaaaaa", "per_two", "permission"), nativeRequestFixture(t, "ses_childbbbbbbb", "que_one", "question")}}
	cards, err := f.d.openCodeRequestCards(f.binding.WorkerID, view)
	if err != nil || len(cards) != 3 {
		t.Fatalf("cards: %+v %v", cards, err)
	}
	for _, card := range cards {
		if card.Session != f.request.Session || card.SessionInstance != f.request.SessionInstance || card.RootID != view.Root || card.Generation != view.Generation || len(card.RequestHash) != 64 || card.ExpiresAt == nil || !card.ExpiresAt.Equal(now.Add(5*time.Minute)) {
			t.Fatalf("authority: %+v", card)
		}
		if card.DetailsJSON == "" || !json.Valid([]byte(card.DetailsJSON)) {
			t.Fatal("full native metadata context missing")
		}
		if card.Kind == "permission" && (len(card.Decisions) != 3 || !strings.Contains(card.Decisions[2].Label, "session")) {
			t.Fatalf("permission effects hidden: %+v", card)
		}
		if card.Kind == "question" && (!card.Questions[0].Custom || !card.Questions[0].Multiple) {
			t.Fatalf("native question semantics lost: %+v", card)
		}
	}
	oldHash, oldExpiry := cards[0].RequestHash, *cards[0].ExpiresAt
	var reordered map[string]any
	if err := json.Unmarshal(view.Pending[0].Raw, &reordered); err != nil {
		t.Fatal(err)
	}
	view.Pending[0].Raw, _ = json.Marshal(reordered)
	now = now.Add(time.Minute)
	clock.Store(now)
	cards, err = f.d.openCodeRequestCards(f.binding.WorkerID, view)
	if err != nil || cards[0].RequestHash != oldHash || !cards[0].ExpiresAt.Equal(oldExpiry) {
		t.Fatalf("read refreshed or changed authority: %+v %v", cards, err)
	}
	view.Pending[0].Raw = json.RawMessage(strings.ReplaceAll(string(view.Pending[0].Raw), "printf marker", "printf changed"))
	cards, err = f.d.openCodeRequestCards(f.binding.WorkerID, view)
	if err != nil || cards[0].RequestHash == oldHash || !cards[0].ExpiresAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("content change retained authority: %+v %v", cards, err)
	}
	view.Pending = view.Pending[:1]
	if _, err := f.d.openCodeRequestCards(f.binding.WorkerID, view); err != nil {
		t.Fatal(err)
	}
	f.d.nativeRequests.mu.Lock()
	remaining := len(f.d.nativeRequests.sessions[f.binding.WorkerID])
	f.d.nativeRequests.mu.Unlock()
	if remaining != 1 {
		t.Fatal("removed native requests retained authority")
	}
	view.Pending[0].Raw = json.RawMessage(strings.Replace(string(view.Pending[0].Raw), "printf changed", strings.Repeat("x", 13000), 1))
	if cards, err = f.d.openCodeRequestCards(f.binding.WorkerID, view); err == nil || len(cards) != 0 {
		t.Fatal("oversized context retained approval authority")
	}
	view.CurrentScopeKnown = false
	if cards, err = f.d.openCodeRequestCards(f.binding.WorkerID, view); err == nil || len(cards) != 0 {
		t.Fatal("unknown current scope published decision authority")
	}
}
