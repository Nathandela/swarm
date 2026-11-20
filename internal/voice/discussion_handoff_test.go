package voice

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestDiscussionHandoffKeepsScopedReceiptReferences(t *testing.T) {
	st := State{Actions: map[string]Action{}, Admissions: map[string]Admission{}}
	current := Admission{ConversationID: "d", Project: "/work", Sequence: 4}
	for i, project := range []string{"/work", "/withdrawn"} {
		seq := uint64(i + 1)
		a := Action{ID: project, ConversationID: "d", Admission: seq, State: "executing"}
		st.Actions[project] = a
		st.Admissions[admissionKey("d", seq)] = Admission{ConversationID: "d", Project: project}
	}
	got := discussionHandoff(st, current, "Use gcloud", "codex", "")
	if !strings.Contains(got, `"action":"/work"`) || strings.Contains(got, "withdrawn") || !strings.HasPrefix(got, "Use gcloud") {
		t.Fatal(got)
	}
}

func TestHandoffPreservesNativeInputBudget(t *testing.T) {
	nonce := strings.Repeat("a", 64)
	for _, provider := range []string{"claude", "codex"} {
		for n := 0; n <= protocol.MaxSendInputText; n += 7 {
			input := recoveryInputNotice + "local instructions\n" + strings.Repeat("界", n/3)
			got := discussionHandoff(State{}, Admission{}, input, provider, nonce)
			if !strings.HasPrefix(got, input) {
				t.Fatal("changed input")
			}
			if got == input {
				continue
			}
			if len(got) > protocol.MaxSendInputText {
				t.Fatal("oversize")
			}
			if provider == "claude" {
				if _, err := claudeinput.ManagerEnvelope(nonce, got); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
