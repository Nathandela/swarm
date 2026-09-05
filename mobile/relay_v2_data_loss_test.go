package swarmmobile

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	remotecrypto "github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relayv2"
)

func TestDiagnosePhoneDeliveriesStopsOnlyForRetainedEvidence(t *testing.T) {
	other := errors.New("durable commit failed")
	for _, tc := range []struct {
		name       string
		receipts   map[uint64]phonecore.Receipt
		errors     map[uint64]error
		wantSeen   []uint64
		wantCursor uint64
		wantErr    error
	}{
		{
			name: "discardable malformed head does not hide valid tail",
			receipts: map[uint64]phonecore.Receipt{
				1: {Disposition: phonecore.ReceiptDiscardable},
			},
			errors:   map[uint64]error{1: remotecrypto.ErrTruncated},
			wantSeen: []uint64{1, 2},
		},
		{
			name: "already acked stale replay does not trigger discard",
			receipts: map[uint64]phonecore.Receipt{
				1: {Acked: true, Disposition: phonecore.ReceiptRetained},
			},
			errors:   map[uint64]error{1: remotecrypto.ErrStaleAge},
			wantSeen: []uint64{1, 2},
		},
		{
			name: "retained authenticated stale head requests discard and fences tail",
			receipts: map[uint64]phonecore.Receipt{
				1: {Disposition: phonecore.ReceiptRetained},
			},
			errors:     map[uint64]error{1: remotecrypto.ErrStaleAge},
			wantSeen:   []uint64{1},
			wantCursor: 1,
		},
		{
			name: "failed durable commit fences tail without destructive discard",
			receipts: map[uint64]phonecore.Receipt{
				1: {Disposition: phonecore.ReceiptRetained},
			},
			errors:   map[uint64]error{1: other},
			wantSeen: []uint64{1},
			wantErr:  other,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deliveries := []relayv2.Delivery{{Cursor: 1}, {Cursor: 2}}
			var seen []uint64
			cursor, err := diagnosePhoneDeliveries(context.Background(), deliveries,
				func(_ context.Context, _ []byte, cursor uint64) (phonecore.Receipt, error) {
					seen = append(seen, cursor)
					return tc.receipts[cursor], tc.errors[cursor]
				})
			if cursor != tc.wantCursor || !errors.Is(err, tc.wantErr) {
				t.Fatalf("diagnosis = (cursor=%v, err=%v), want (%v, %v)", cursor, err, tc.wantCursor, tc.wantErr)
			}
			if !reflect.DeepEqual(seen, tc.wantSeen) {
				t.Fatalf("accepted cursors = %v, want %v", seen, tc.wantSeen)
			}
		})
	}
}

func TestBlocksMailboxPageTruthTable(t *testing.T) {
	err := errors.New("refused")
	for _, tc := range []struct {
		name    string
		receipt phonecore.Receipt
		err     error
		want    bool
	}{
		{name: "success", want: false},
		{name: "discardable error", receipt: phonecore.Receipt{Disposition: phonecore.ReceiptDiscardable}, err: err, want: false},
		{name: "acked retained error", receipt: phonecore.Receipt{Acked: true, Disposition: phonecore.ReceiptRetained}, err: err, want: false},
		{name: "unacked retained error", receipt: phonecore.Receipt{Disposition: phonecore.ReceiptRetained}, err: err, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := blocksMailboxPage(tc.receipt, tc.err); got != tc.want {
				t.Fatalf("blocksMailboxPage(%+v, %v) = %v, want %v", tc.receipt, tc.err, got, tc.want)
			}
		})
	}
}
