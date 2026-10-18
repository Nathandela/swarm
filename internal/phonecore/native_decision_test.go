package phonecore

import (
	"bytes"
	"reflect"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
)

func TestNativeDecisionSignsAndSealsTheExactAnswers(t *testing.T) {
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().UTC().Add(time.Minute)
	body := schema.NativeDecisionReq{Session: "m/s", SessionInstance: "instance", RootID: "ses_root", Generation: "generation", NativeSessionID: "ses_child", RequestID: "que_exact", RequestHash: "request-hash", ExpiresAt: &expires, Kind: "question_reply", Answers: [][]string{{"first", "custom"}, {"second"}}}
	cmd, err := SignNativeDecision(ks, NativeDecisionInput{Machine: "m", OperationID: "decision-operation", ExpiresAt: expires, Request: body})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Action != schema.ActionNativeDecide || cmd.Session != body.Session || !bytes.Equal(cmd.ContentHash, schema.NativeDecisionContentHash(&body)) {
		t.Fatal("native decision did not sign its actual complete body")
	}
	var key crypto.ContentKey
	key[0] = 1
	env, err := SealNativeDecisionEnvelope(key, 1, 7, cmd, &body)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := remotegw.OpenRemoteCommand(key, env)
	if err != nil || opened.BodyVersion != 1 || opened.NativeDecision == nil || !reflect.DeepEqual(*opened.NativeDecision, body) || opened.Sig != cmd.Sig {
		t.Fatalf("native decision changed across phone sealing: %+v %v", opened, err)
	}
	changed := body
	changed.Answers = [][]string{{"second"}, {"first", "custom"}}
	if bytes.Equal(schema.NativeDecisionContentHash(&changed), cmd.ContentHash) {
		t.Fatal("answer groups could be reordered without changing the signature binding")
	}
}
