package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	bolt "go.etcd.io/bbolt"
)

// waitNoticeManager answers the send-time check for routine wait notices.
type waitNoticeManager struct {
	*teammateTestManager
	mu       sync.Mutex
	obsolete bool
}

func (m *waitNoticeManager) ValidatePublicationForSend(context.Context, TeammateDiscussion, TeammatePublication) (schema.MessageOrigin, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.obsolete {
		return schema.MessageOrigin{}, ErrReplyWaitObsolete
	}
	return schema.MessageOrigin{}, nil
}

func waitNoticeConnector(t *testing.T, status int) (*teammateConnector, *waitNoticeManager, TeammateDiscussion, *[]string, *time.Time) {
	t.Helper()
	var mu sync.Mutex
	posts := &[]string{}
	bridge, base := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/201":
			_, _ = fmt.Fprint(w, teammatePublic)
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/201/messages":
			var in struct{ Content, Nonce string }
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			mu.Lock()
			*posts = append(*posts, in.Content)
			n := len(*posts)
			mu.Unlock()
			if status != http.StatusOK {
				w.WriteHeader(status)
				return
			}
			_, _ = fmt.Fprintf(w, `{"id":"%d","channel_id":"201","guild_id":"10","content":%q,"author":{"id":"99"}}`, 500+n, in.Content)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	mgr := &waitNoticeManager{teammateTestManager: base}
	bridge.manager = mgr
	now := time.Now()
	bridge.service.Now = func() time.Time { return now }
	d := TeammateDiscussion{ID: "d1", InstallationID: "install", Route: Route{GuildID: "10", ChannelID: "201", ParentID: "20"}}
	if err := bridge.service.Store.teammateBind(teammateBinding{TeammateDiscussion: d}); err != nil {
		t.Fatal(err)
	}
	return bridge, mgr, d, posts, &now
}

func queueWaitNotice(t *testing.T, c *teammateConnector, d TeammateDiscussion, now time.Time) TeammatePublication {
	t.Helper()
	p := TeammatePublication{ID: teammateReplyWaitPrefix + "1", AdmissionID: "input", Sequence: 1, Body: "I’m waiting for the worker’s result."}
	if err := c.service.Store.teammateQueueOutput(d, p, now); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWaitNoticeSendsAfterGenuineOutput(t *testing.T) {
	c, _, d, posts, now := waitNoticeConnector(t, http.StatusOK)
	wait := queueWaitNotice(t, c, d, *now)
	if err := c.service.Store.teammateQueueOutput(d, TeammatePublication{ID: "result", AdmissionID: "input", Sequence: 2, Body: "verified result"}, *now); err != nil {
		t.Fatal(err)
	}
	if err := c.sendPending(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if len(*posts) != 2 || (*posts)[0] != "verified result" || (*posts)[1] != wait.Body {
		t.Fatalf("genuine output must go first: %q", *posts)
	}
}

func TestObsoleteUnattemptedWaitNoticeCompactsWithoutPosting(t *testing.T) {
	c, mgr, d, posts, now := waitNoticeConnector(t, http.StatusOK)
	mgr.obsolete = true
	wait := queueWaitNotice(t, c, d, *now)
	if err := c.sendPending(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if len(*posts) != 0 {
		t.Fatalf("obsolete notice posted: %q", *posts)
	}
	pending, err := c.service.Store.teammateOutputs("install", d.ID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("obsolete notice still pending: %+v %v", pending, err)
	}
	var receipt teammateReceipt
	if err = c.service.Store.db.View(func(tx *bolt.Tx) error {
		return teammateGet(tx.Bucket([]byte(teammateReceiptBucket)), teammateOutputKey("install", d.ID, wait.ID), &receipt)
	}); err != nil || len(receipt.DiscordMessageIDs) != 0 {
		t.Fatalf("want an unconfirmed receipt: %+v %v", receipt, err)
	}
}

func TestObsoleteAttemptedWaitNoticeNeverRepostsAndKeepsLateReceipt(t *testing.T) {
	c, mgr, d, posts, now := waitNoticeConnector(t, http.StatusInternalServerError)
	wait := queueWaitNotice(t, c, d, *now)
	if err := c.sendPending(t.Context(), d); !teammateAPIStatus(err, http.StatusInternalServerError) || len(*posts) != 1 {
		t.Fatalf("first attempt: %d %v", len(*posts), err)
	}
	mgr.obsolete = true
	for range 2 {
		if err := c.sendPending(t.Context(), d); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(teammateNonceWindow)
	}
	if len(*posts) != 1 {
		t.Fatalf("obsolete attempted notice re-posted: %d posts", len(*posts))
	}
	pending, err := c.service.Store.teammateOutputs("install", d.ID)
	if err != nil || len(pending) != 1 || pending[0].SettledAt.IsZero() || pending[0].Chunks[0].State != StateUncertain || pending[0].Error != teammateReplyWaitPassive {
		t.Fatalf("want a passive uncertain row: %+v %v", pending, err)
	}
	if err = c.service.Store.teammateObserve("install", d.Route, "333", pending[0].Chunks[0].ID, wait.Body, *now); err != nil {
		t.Fatal(err)
	}
	if pending, err = c.service.Store.teammateOutputs("install", d.ID); err != nil || len(pending) != 0 {
		t.Fatalf("late receipt not accepted: %+v %v", pending, err)
	}
}
