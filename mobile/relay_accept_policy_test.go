package swarmmobile

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"runtime"
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

func TestAcceptMailboxPage_ContinuesDiscardableAndStopsAtRetained(t *testing.T) {
	errFrame := errors.New("injected frame refusal")
	items := []relay.Item{{Cursor: 1}, {Cursor: 2}, {Cursor: 3}}

	tests := []struct {
		name string
		at   map[uint64]phonecore.Receipt
		want []uint64
	}{
		{
			name: "discardable malformed head does not wedge valid tail",
			at: map[uint64]phonecore.Receipt{
				1: {Disposition: phonecore.ReceiptDiscardable},
			},
			want: []uint64{1, 2, 3},
		},
		{
			name: "retained recoverable head fences later cursor",
			at: map[uint64]phonecore.Receipt{
				2: {Disposition: phonecore.ReceiptRetained},
			},
			want: []uint64{1, 2},
		},
		{
			name: "acked replay continues even with fail-closed zero disposition",
			at: map[uint64]phonecore.Receipt{
				1: {Acked: true},
			},
			want: []uint64{1, 2, 3},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []uint64
			acceptMailboxPage(context.Background(), items, func(_ context.Context, _ []byte, cursor uint64) (phonecore.Receipt, error) {
				got = append(got, cursor)
				if receipt, ok := tc.at[cursor]; ok {
					return receipt, errFrame
				}
				return phonecore.Receipt{}, nil
			})
			if len(got) != len(tc.want) {
				t.Fatalf("accepted cursors = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("accepted cursors = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestDiagnoseMailboxPage_SurfacesRetainedErrorsAndOnlyAuthorizesStaleAge(t *testing.T) {
	errRetained := errors.New("injected durable commit failure")
	items := []relay.Item{{Cursor: 1}, {Cursor: 2}}

	t.Run("non-stale retained error is surfaced before the tail", func(t *testing.T) {
		var got []uint64
		stale, err := diagnoseMailboxPage(context.Background(), items, func(_ context.Context, _ []byte, cursor uint64) (phonecore.Receipt, error) {
			got = append(got, cursor)
			return phonecore.Receipt{Disposition: phonecore.ReceiptRetained}, errRetained
		})
		if stale || !errors.Is(err, errRetained) {
			t.Fatalf("diagnosis = (stale=%v, err=%v), want surfaced retained error", stale, err)
		}
		if len(got) != 1 || got[0] != 1 {
			t.Fatalf("accepted cursors = %v, want [1]", got)
		}
	})

	t.Run("stale-age retained error authorizes discard", func(t *testing.T) {
		stale, err := diagnoseMailboxPage(context.Background(), items, func(context.Context, []byte, uint64) (phonecore.Receipt, error) {
			return phonecore.Receipt{Disposition: phonecore.ReceiptRetained}, crypto.ErrStaleAge
		})
		if !stale || err != nil {
			t.Fatalf("diagnosis = (stale=%v, err=%v), want stale authorization", stale, err)
		}
	})
}

func TestDrainWaitAndPollUseTheSharedAcceptPolicy(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate this test")
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file[:len(file)-len("relay_accept_policy_test.go")]+"relay.go", nil, 0)
	if err != nil {
		t.Fatalf("parse relay.go: %v", err)
	}
	for _, name := range []string{"drainWait", "drainPoll"} {
		calls := 0
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != name {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if ok && sel.Sel.Name == "acceptMailboxPage" {
					calls++
				}
				return true
			})
		}
		if calls != 1 {
			t.Errorf("%s calls acceptMailboxPage %d times, want exactly once", name, calls)
		}
	}
}
