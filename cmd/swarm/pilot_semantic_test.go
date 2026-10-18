package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestPilotSemanticUncertainSendPreservesExactRetryCustody(t *testing.T) {
	c := newFakeSemanticClient()
	path := enterPilotForTest(t, c)
	c.messageErr = errors.New("reply lost after submission")
	code, out, stderr := pilotCall(c, "--context", path, "send", "local/ami", "--text", "exact payload")
	if code == 0 || out != "" || len(c.messages) != 1 || len(c.sendCalls()) != 0 {
		t.Fatalf("uncertain send bypassed semantic boundary: exit%d output%q err%q messages%+v", code, out, stderr, c.messages)
	}
	data, err := os.ReadFile(pilotPendingPath(path, "local/ami"))
	if err != nil { t.Fatal(err) }
	var pending map[string]any
	if json.Unmarshal(data, &pending) != nil || pending["operation_id"] != c.messages[0].operation || pending["uncertain"] != true {
		t.Fatalf("lost custody after ambiguous submission: %s", data)
	}
	if code, _, _ := pilotCall(c, "--context", path, "send", "local/ami", "--text", "different payload"); code == 0 || len(c.messages) != 1 {
		t.Fatal("a new payload overtook the unresolved operation")
	}
	c.messageErr = nil
	code, out, stderr = pilotCall(c, "--context", path, "send", "local/ami", "--text", "exact payload")
	if code != 0 || !strings.Contains(out, `"receipt":"submitted"`) || len(c.messages) != 2 || c.messages[0] != c.messages[1] {
		t.Fatalf("retry changed operation/payload: exit%d output%q err%q messages%+v", code, out, stderr, c.messages)
	}
}

func TestPilotSemanticMarkerFailurePreventsNativeWrite(t *testing.T) {
	c := newFakeSemanticClient()
	path := enterPilotForTest(t, c)
	if err := os.Mkdir(pilotPendingPath(path, "local/ami"), 0o700); err != nil { t.Fatal(err) }
	if code, out, _ := pilotCall(c, "--context", path, "send", "local/ami", "--text", "do it"); code == 0 || out != "" || len(c.messages) != 0 || len(c.sendCalls()) != 0 {
		t.Fatal("native effect preceded durable pilot custody")
	}
}
