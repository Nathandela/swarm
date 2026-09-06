package verify_test

// PB-NET-7: every relay-v2 network request has the section 6.0 ten-second
// ceiling. The retired relay-v1 client had caller-owned long polls and raw
// rendezvous dials; relay-v2 has neither. Its two choke points own the bound:
// dialRaw for websocket setup and Conn.call for every request/reply exchange.
//
// This intentionally audits the native production client, not compatibility
// method names such as MachineMailbox.MailboxWait. Those methods only drain an
// already-authenticated relay-v2 subscription and must remain caller-cancellable.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pbnet7RelayV2Client = "internal/remote/relayv2/client.go"

type pbnet7Func struct {
	name string
	recv string
	body *ast.BlockStmt
}

func pbnet7Client(t *testing.T) (string, map[string]pbnet7Func) {
	t.Helper()
	path := filepath.Join(repoRoot(t), pbnet7RelayV2Client)
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PB-NET-7: read %s: %v", pbnet7RelayV2Client, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatalf("PB-NET-7: parse %s: %v", pbnet7RelayV2Client, err)
	}
	funcs := map[string]pbnet7Func{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		recv := ""
		if fn.Recv != nil && len(fn.Recv.List) == 1 {
			switch typ := fn.Recv.List[0].Type.(type) {
			case *ast.StarExpr:
				if id, ok := typ.X.(*ast.Ident); ok {
					recv = "*" + id.Name
				}
			case *ast.Ident:
				recv = typ.Name
			}
		}
		key := fn.Name.Name
		if recv != "" {
			key = "(" + recv + ")." + key
		}
		funcs[key] = pbnet7Func{name: fn.Name.Name, recv: recv, body: fn.Body}
	}
	if len(funcs) < 25 {
		t.Fatalf("PB-NET-7 VACUOUS: parsed only %d relay-v2 functions; %s is not the native client", len(funcs), pbnet7RelayV2Client)
	}
	return string(src), funcs
}

func pbnet7Calls(body *ast.BlockStmt, qualifier, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if qualifier == "" {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == name {
				found = true
				return false
			}
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != name {
			return true
		}
		if qualifier == "" {
			found = true
			return false
		}
		id, ok := sel.X.(*ast.Ident)
		if ok && id.Name == qualifier {
			found = true
			return false
		}
		return true
	})
	return found
}

func pbnet7Body(src string, body *ast.BlockStmt) string {
	return src[int(body.Pos())-1 : int(body.End())-1]
}

// TestPBNET7_NativeRelayV2RequestsHaveSection60Bounds is deliberately a source
// fence: it proves every current request verb still passes through Conn.call,
// rather than merely testing a representative request while a future verb can
// bypass the deadline.
func TestPBNET7_NativeRelayV2RequestsHaveSection60Bounds(t *testing.T) {
	src, funcs := pbnet7Client(t)
	if !strings.Contains(src, "defaultCallTimeout  = 10 * time.Second") ||
		!strings.Contains(src, "DefaultDialTimeout = 10 * time.Second") {
		t.Fatal("PB-NET-7: relay-v2 no longer declares the section 6.0 ten-second call and dial ceilings")
	}

	require := func(key string) pbnet7Func {
		t.Helper()
		fn, ok := funcs[key]
		if !ok {
			t.Fatalf("PB-NET-7 VACUOUS: native relay-v2 verb %s disappeared from the audit", key)
		}
		return fn
	}

	dialRaw := require("dialRaw")
	if !pbnet7Calls(dialRaw.body, "context", "WithTimeout") ||
		!strings.Contains(pbnet7Body(src, dialRaw.body), "DefaultDialTimeout") ||
		!pbnet7Calls(dialRaw.body, "websocket", "Dial") {
		t.Fatal("PB-NET-7: relay-v2 dialRaw no longer bounds websocket setup with DefaultDialTimeout")
	}

	call := require("(*Conn).call")
	callBody := pbnet7Body(src, call.body)
	if !pbnet7Calls(call.body, "context", "WithTimeout") || !strings.Contains(callBody, "defaultCallTimeout") ||
		strings.Count(callBody, "callCtx.Done()") < 2 {
		t.Fatal("PB-NET-7: relay-v2 Conn.call no longer bounds both request write and reply wait with defaultCallTimeout")
	}

	// These are every production RPC method in client.go. Dial and DialPair are
	// the only setup verbs; all others issue exactly one request through Conn.call.
	for _, key := range []string{
		"Dial", "DialPair",
		"(*Conn).Authorize", "(*Conn).Append", "(*Conn).Subscribe", "(*Conn).Revoke", "(*Conn).Discard",
		"(*Subscription).Probe", "(*Subscription).Ack",
		"(*PairTransport).Create", "(*PairTransport).Claim", "(*PairTransport).Send", "(*PairTransport).Complete",
	} {
		fn := require(key)
		if key == "Dial" || key == "DialPair" {
			if !pbnet7Calls(fn.body, "", "dialRaw") {
				t.Errorf("PB-NET-7: %s no longer reaches bounded dialRaw", key)
			}
			continue
		}
		if !pbnet7Calls(fn.body, "", "call") {
			t.Errorf("PB-NET-7: %s no longer reaches bounded Conn.call", key)
		}
	}

	// Recv is intentionally not an RPC: it waits for a pushed frame and honors
	// only the caller's context. Keeping it out of Conn.call is the non-vacuous
	// distinction between bounded requests and the live stream's receive loop.
	subRecv := require("(*Subscription).Recv")
	take := require("(*Subscription).take")
	if pbnet7Calls(subRecv.body, "", "call") || !pbnet7Calls(subRecv.body, "", "take") ||
		!strings.Contains(pbnet7Body(src, take.body), "ctx.Done()") {
		t.Error("PB-NET-7: Subscription.Recv is no longer an explicitly caller-cancellable pushed receive")
	}
	pairRecv := require("(*PairTransport).Recv")
	if pbnet7Calls(pairRecv.body, "", "call") || !strings.Contains(pbnet7Body(src, pairRecv.body), "ctx.Done()") {
		t.Error("PB-NET-7: PairTransport.Recv is no longer an explicitly caller-cancellable pushed receive")
	}
}
