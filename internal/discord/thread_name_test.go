package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestTeammateThreadNameFromOpener(t *testing.T) {
	for _, tc := range []struct{ content, want string }{
		{"<@99> check why staging fund page is slow", "check why staging fund page is slow"},
		{"<@!99>   look at\n<@&5> the <#20> logs  ", "look at the logs"},
		{"<@99> one two three four five six seven eight nine ten", "one two three four five six seven eight"},
		{"<@99>", "Claudio discussion"},
		{"   ", "Claudio discussion"},
		{"<@99> " + strings.Repeat("x", 120), strings.Repeat("x", 95)},
	} {
		if got := teammateThreadName("Claudio", tc.content); got != tc.want {
			t.Errorf("teammateThreadName(%q) = %q, want %q", tc.content, got, tc.want)
		}
	}
}

func TestTeammateTopLevelThreadIsNamedFromOpener(t *testing.T) {
	var name string
	opener := teammateTestMessage("201", "20", "77", "<@99> check why staging fund page is slow", true)
	bridge, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/20/messages/201":
			_ = json.NewEncoder(w).Encode(opener)
		case "/channels/20/messages/201/threads":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			name = body["name"]
			_, _ = fmt.Fprint(w, teammatePublic)
		case "/channels/201":
			if name == "" {
				w.WriteHeader(404)
			} else {
				_, _ = fmt.Fprint(w, teammatePublic)
			}
		default:
			w.WriteHeader(404)
		}
	})
	teammateTestEvent(t, bridge, 1, "MESSAGE_CREATE", opener)
	pending, err := bridge.service.Store.teammatePending("install")
	if err != nil || len(pending) != 1 {
		t.Fatalf("%d candidates: %v", len(pending), err)
	}
	if err = bridge.process(context.Background(), pending[0]); err != nil {
		t.Fatal(err)
	}
	if name != "check why staging fund page is slow" {
		t.Fatalf("thread name %q", name)
	}
}

// Discord can reject a name derived from user text. The thread still opens with the fallback name.
func TestTeammateRejectedThreadNameFallsBackToTheDefault(t *testing.T) {
	var names []string
	opener := teammateTestMessage("201", "20", "77", "<@99> \u200b", true)
	bridge, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/20/messages/201":
			_ = json.NewEncoder(w).Encode(opener)
		case "/channels/20/messages/201/threads":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			names = append(names, body["name"])
			if body["name"] != " discussion" {
				w.WriteHeader(400)
				return
			}
			_, _ = fmt.Fprint(w, teammatePublic)
		case "/channels/201":
			if len(names) < 2 {
				w.WriteHeader(404)
			} else {
				_, _ = fmt.Fprint(w, teammatePublic)
			}
		default:
			w.WriteHeader(404)
		}
	})
	teammateTestEvent(t, bridge, 1, "MESSAGE_CREATE", opener)
	pending, err := bridge.service.Store.teammatePending("install")
	if err != nil || len(pending) != 1 {
		t.Fatalf("%d candidates: %v", len(pending), err)
	}
	if err = bridge.process(context.Background(), pending[0]); err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[1] != " discussion" {
		t.Fatalf("thread names tried: %q", names)
	}
}
