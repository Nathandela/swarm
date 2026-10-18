package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

type nativeRequestCLIStub struct {
	*fakeSemanticClient
	card        protocol.NativeRequestView
	decisions   []protocol.NativeDecisionReq
	selectors   []protocol.NativeRequestsReq
	statusReads []string
	statusErr   error
}

func (c *nativeRequestCLIStub) NativeDecisionStatus(session, operation string) (protocol.NativeDecisionReceipt, error) {
	c.statusReads = append(c.statusReads, session+"/"+operation)
	return protocol.NativeDecisionReceipt{Session: session, OperationID: operation, State: "uncertain"}, c.statusErr
}

func (c *nativeRequestCLIStub) NativeRequests(req protocol.NativeRequestsReq) (protocol.NativeRequestsReply, error) {
	c.selectors = append(c.selectors, req)
	return protocol.NativeRequestsReply{Session: req.Session, Request: &c.card}, nil
}
func (c *nativeRequestCLIStub) NativeDecide(id string, req protocol.NativeDecisionReq) (protocol.NativeDecisionReceipt, error) {
	c.decisions = append(c.decisions, req)
	return protocol.NativeDecisionReceipt{OperationID: id, Session: req.Session, RequestID: req.RequestID, State: "admitted"}, nil
}
func nativeCLIStub() *nativeRequestCLIStub {
	expiry := time.Now().UTC().Add(time.Minute)
	return &nativeRequestCLIStub{fakeSemanticClient: newFakeSemanticClient(), card: protocol.NativeRequestView{Session: "local/ami", SessionInstance: "instance", RootID: "ses_root", Generation: "generation", NativeSessionID: "ses_child", RequestID: "req_exact", RequestHash: strings.Repeat("a", 64), ExpiresAt: &expiry, Kind: "question"}}
}

func TestNativeDecisionCLIBindsTheObservedContentAndGroupedAnswers(t *testing.T) {
	c := nativeCLIStub()
	var out, stderr bytes.Buffer
	args := []string{"local/ami", "--request-id", "req_exact", "--native-session-id", "ses_child", "--request-hash", c.card.RequestHash, "--reply", "answer", "--answers", `[["one","custom"],["two"]]`}
	if code := runNativeDecide(args, c, &out, &stderr); code != 0 {
		t.Fatalf("native answer exit%d: %s", code, stderr.String())
	}
	if len(c.decisions) != 1 || c.decisions[0].Kind != "question_reply" || len(c.decisions[0].Answers) != 2 || c.decisions[0].Answers[0][1] != "custom" || c.decisions[0].RequestHash != c.card.RequestHash || len(c.sendCalls()) != 0 {
		t.Fatalf("native answer reached wrong target or path: %+v", c.decisions)
	}
	if !strings.Contains(out.String(), `"state":"admitted"`) && !strings.Contains(out.String(), `"state": "admitted"`) {
		t.Fatalf("no typed receipt: %s", out.String())
	}
}

func TestNativeDecisionCLIRefusesChangedContentBeforeAnyWrite(t *testing.T) {
	c := nativeCLIStub()
	var out, stderr bytes.Buffer
	args := []string{"local/ami", "--request-id", "req_exact", "--native-session-id", "ses_child", "--request-hash", strings.Repeat("b", 64), "--reply", "reject"}
	if code := runNativeDecide(args, c, &out, &stderr); code == 0 || len(c.decisions) != 0 || len(c.sendCalls()) != 0 {
		t.Fatal("a fresh lookup silently approved changed request content")
	}
}

func TestNativeRequestsCLIRequiresBothExactSelectors(t *testing.T) {
	c := nativeCLIStub()
	var out, stderr bytes.Buffer
	if code := runNativeRequests([]string{"local/ami", "--request-id", "req_exact"}, c, &out, &stderr); code != misuseExit || len(c.selectors) != 0 {
		t.Fatal("incomplete request selector reached native reads")
	}
	if code := runNativeRequests([]string{"local/ami", "--request-id", "req_exact", "--native-session-id", "ses_child"}, c, &out, &stderr); code != 0 || len(c.selectors) != 1 {
		t.Fatalf("exact native request read failed: %s", stderr.String())
	}
}

func TestNativeDecisionCLIStatusNeverReadsOrResubmitsTheRequest(t *testing.T) {
	c := nativeCLIStub()
	var out, stderr bytes.Buffer
	if code := runNativeDecide([]string{"local/ami", "--status", "--operation-id", "native:original"}, c, &out, &stderr); code != 0 {
		t.Fatalf("read retained native decision: exit%d %s", code, stderr.String())
	}
	if len(c.statusReads) != 1 || c.statusReads[0] != "local/ami/native:original" || len(c.selectors) != 0 || len(c.decisions) != 0 || len(c.sendCalls()) != 0 || !strings.Contains(out.String(), `"uncertain"`) {
		t.Fatalf("status did not remain an exact receipt read: reads=%v lookups=%v decisions=%v output=%s", c.statusReads, c.selectors, c.decisions, out.String())
	}
	c.statusErr = fmt.Errorf("retained receipt unavailable")
	if code := runNativeDecide([]string{"local/ami", "--status", "--operation-id", "native:original"}, c, &out, &stderr); code == 0 || len(c.decisions) != 0 || len(c.selectors) != 0 {
		t.Fatal("missing receipt fell through to a new decision")
	}
}

func TestNativeDecisionCLISeparatesReceiptReadsFromWrites(t *testing.T) {
	for _, flags := range [][]string{
		{"--status"},
		{"--operation-id", "native:original"},
		{"--status", "--operation-id", "native:original", "--reply", "reject"},
		{"--status", "--operation-id", "native:original", "--answers", "[]"},
		{"--status", "--operation-id", "native:original", "--request-id", "req_exact"},
	} {
		c := nativeCLIStub()
		var out, stderr bytes.Buffer
		if code := runNativeDecide(append([]string{"local/ami"}, flags...), c, &out, &stderr); code != misuseExit || len(c.statusReads) != 0 || len(c.decisions) != 0 || len(c.selectors) != 0 {
			t.Fatalf("ambiguous status flags reached a native operation: flags=%v exit=%d err=%s", flags, code, stderr.String())
		}
	}
}
