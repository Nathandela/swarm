package shim

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestClaudeChatValidJSONWithoutLFRemainsTornEOF(t *testing.T) {
	c, _, outW := claudeMessagePipeUnit(t)
	raw := []byte(`{"type":"result","subtype":"success","session_id":"8e4c6620-bfdd-43c3-93de-e69a2f97a01f","uuid":"1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a","is_error":false,"result":"complete-looking JSON is not a complete stream frame"}`)
	if !json.Valid(raw) {
		t.Fatal("fixture is not valid JSON")
	}
	if _, err := outW.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := outW.Close(); err != nil {
		t.Fatal(err)
	}
	c.readStdout(claudePipeReader{file: c.stdout})
	deadline := time.Now().Add(time.Second)
	for {
		p := c.primarySnapshot()
		if p != nil && p.DurableSequence == 2 {
			if p.Healthy || !p.StdoutEOF {
				t.Fatal("valid JSON fragment gained complete-frame health")
			}
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("torn valid JSON bytes were not retained")
		}
		time.Sleep(time.Millisecond)
	}
	records, _, _, err := c.spool.ReadFrom(0)
	if err != nil || len(records) != 2 {
		t.Fatal("retained fragment record missing", err)
	}
	var envelope claudeChatEnvelope
	if json.Unmarshal(records[1].Body, &envelope) != nil || !bytes.Equal(envelope.Raw, raw) || !json.Valid(envelope.Raw) {
		t.Fatal("raw fragment changed or falsely acquired LF")
	}
	c.mu.Lock()
	loss, eof := c.lost, c.eof[0]
	c.mu.Unlock()
	if !loss || !eof {
		t.Fatal("torn EOF did not retain loss separately from true EOF")
	}
}
