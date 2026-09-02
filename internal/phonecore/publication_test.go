package phonecore

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func testComposerPublication(id string) PendingPublication {
	body := &schema.ComposerSendReq{
		Session:         "m1/s1",
		SessionInstance: "instance-1",
		ExpectedTurn:    "turn-1",
		Text:            "ship it",
	}
	return PendingPublication{
		LogicalID:       "logical-" + id,
		OperationID:     id,
		Kind:            PublicationComposer,
		SessionID:       body.Session,
		SessionInstance: body.SessionInstance,
		ExpectedTurn:    body.ExpectedTurn,
		Text:            body.Text,
		Machine:         "m1",
		EpochID:         7,
		Target:          "rid-m1",
		AuthorityPub:    bytes.Repeat([]byte{0x44}, 32),
		Command: schema.DeviceCommandAuth{
			Action:      schema.ActionComposerSend,
			Machine:     "m1",
			Session:     body.Session,
			OperationID: id,
			ExpiresAt:   time.Unix(1_800_000_000, 0),
		},
		Composer:  body,
		Phase:     PublicationPrepared,
		CreatedAt: time.Unix(1_700_000_000, 0),
	}
}

func publicationCore(t *testing.T) *Core {
	t.Helper()
	c, err := Resume(Config{
		Dir:           t.TempDir(),
		Machine:       "m1",
		WakeSealer:    s14aNewSealer(t),
		ContentSealer: s14aNewSealer(t),
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	st := c.State()
	st.Machine = "m1"
	st.MachineRelayAuthPub = bytes.Repeat([]byte{0x44}, 32)
	st.EpochID = 7
	st.RoutingID = "rid-m1"
	st.Keys = s15EpochKeys()
	if err := c.Save(st); err != nil {
		t.Fatalf("seed publication state: %v", err)
	}
	return c
}

func TestPendingPublications_PrepareIsBoundedAndNeverEvictsUnresolved(t *testing.T) {
	c := publicationCore(t)
	for i := 0; i < MaxPendingPublications; i++ {
		id := "op-" + string(rune('A'+i))
		if err := c.PreparePublication(testComposerPublication(id)); err != nil {
			t.Fatalf("PreparePublication %d: %v", i, err)
		}
	}
	want := c.PendingPublications()
	if err := c.PreparePublication(testComposerPublication("overflow")); !errors.Is(err, ErrPublicationQueueFull) {
		t.Fatalf("overflow error = %v, want ErrPublicationQueueFull", err)
	}
	got := c.PendingPublications()
	if len(got) != len(want) {
		t.Fatalf("overflow changed queue length from %d to %d", len(want), len(got))
	}
	for i := range want {
		if got[i].OperationID != want[i].OperationID {
			t.Fatalf("overflow evicted/reordered index %d: got %q want %q", i, got[i].OperationID, want[i].OperationID)
		}
	}
}

func TestPendingPublications_TerminalResultsAreBoundedWithoutConsumingSendCapacity(t *testing.T) {
	c := publicationCore(t)
	st := c.State()
	st.OpOutcomes = map[string]schema.Control{
		"unrelated-operation": {Op: "error", OperationID: "unrelated-operation", ErrorCode: schema.CodePolicy},
	}
	for i := 0; i < maxPublicationResults; i++ {
		p := testComposerPublication(fmt.Sprintf("op-result-%02d", i))
		p.Phase = PublicationTerminal
		p.TerminalCode = PublicationAuthorityChanged
		st.PendingPublications = append(st.PendingPublications, p)
		st.OpOutcomes[p.OperationID] = schema.Control{
			Op: "error", OperationID: p.OperationID, ErrorCode: schema.ErrorCode(PublicationAuthorityChanged),
		}
	}
	if err := c.Save(st); err != nil {
		t.Fatalf("seed terminal results: %v", err)
	}
	if err := c.PreparePublication(testComposerPublication("op-new-intent")); err != nil {
		t.Fatalf("terminal results consumed send capacity: %v", err)
	}
	got := c.PendingPublications()
	if len(got) != maxPublicationResults || got[len(got)-1].OperationID != "op-new-intent" ||
		got[len(got)-1].Phase != PublicationPrepared {
		t.Fatalf("bounded result projection = %+v", got)
	}
	for _, p := range got {
		if p.OperationID == "op-result-00" {
			t.Fatal("oldest terminal result was not pruned")
		}
	}
	outcomes := c.State().OpOutcomes
	if _, ok := outcomes["op-result-00"]; ok {
		t.Fatal("evicted terminal publication left its per-send outcome claimable")
	}
	if _, ok := outcomes["op-result-01"]; !ok {
		t.Fatal("bounding removed a retained publication's outcome")
	}
	if _, ok := outcomes["unrelated-operation"]; !ok {
		t.Fatal("composer result bounding pruned an unrelated operation outcome")
	}
}

func TestPendingPublications_AdmittedWorkStillCountsUntilOutcomeBound(t *testing.T) {
	c := publicationCore(t)
	st := c.State()
	for i := 0; i < MaxPendingPublications; i++ {
		p := testComposerPublication(fmt.Sprintf("op-admitted-%02d", i))
		p.Phase = PublicationAdmitted
		p.Sequence = uint64(i + 1)
		p.Envelope = []byte(fmt.Sprintf("envelope-%02d", i))
		st.PendingPublications = append(st.PendingPublications, p)
	}
	if err := c.Save(st); err != nil {
		t.Fatalf("seed admitted work: %v", err)
	}
	if err := c.PreparePublication(testComposerPublication("op-overflow-admitted")); !errors.Is(err, ErrPublicationQueueFull) {
		t.Fatalf("PreparePublication = %v, want unresolved queue full", err)
	}
}

func terminalInputBusyPublication(t *testing.T, c *Core, id, logicalID, text string) PendingPublication {
	t.Helper()
	p := testComposerPublication(id)
	p.LogicalID = logicalID
	p.Text, p.Composer.Text = text, text
	p.Phase = PublicationTerminal
	p.TerminalCode = string(schema.CodeInputBusy)
	st := c.State()
	p.ResultOrder = uint64(len(st.PendingPublications) + 1)
	st.PendingPublications = append(st.PendingPublications, p)
	if st.OpOutcomes == nil {
		st.OpOutcomes = map[string]schema.Control{}
	}
	st.OpOutcomes[id] = schema.Control{
		Op: controlOpError, OperationID: id, SessionID: p.SessionID,
		ErrorCode: schema.CodeInputBusy,
	}
	if err := c.Save(st); err != nil {
		t.Fatalf("seed terminal input_busy publication: %v", err)
	}
	return p
}

func retryPublication(prior PendingPublication, operationID string) PendingPublication {
	retry := prior.clone()
	retry.OperationID = operationID
	retry.Command.OperationID = operationID
	retry.Command.ExpiresAt = prior.Command.ExpiresAt.Add(time.Minute)
	retry.Phase = PublicationPrepared
	retry.Sequence = 0
	retry.Envelope = nil
	retry.TerminalCode = ""
	retry.ResultOrder = 0
	retry.CreatedAt = prior.CreatedAt.Add(time.Second)
	return retry
}

func TestPendingPublications_RetryInputBusyReplacesOnlyExactPriorLogicalSend(t *testing.T) {
	c := publicationCore(t)
	first := terminalInputBusyPublication(t, c, "op-old-A", "logical-A", "same words")
	second := terminalInputBusyPublication(t, c, "op-old-B", "logical-B", "same words")
	retry := retryPublication(second, "op-new-B")

	if err := c.PreparePublicationRetry(second.OperationID, retry); err != nil {
		t.Fatalf("PreparePublicationRetry: %v", err)
	}
	got := c.PendingPublications()
	if len(got) != 2 || got[0].OperationID != first.OperationID ||
		got[1].OperationID != retry.OperationID || got[1].LogicalID != second.LogicalID ||
		got[1].Phase != PublicationPrepared {
		t.Fatalf("exact logical replacement = %+v", got)
	}
	// A repeated facade completion after the durable transition is idempotent; it may not
	// duplicate the bubble or require the retired operation to still exist.
	if err := c.PreparePublicationRetry(second.OperationID, retry); err != nil {
		t.Fatalf("repeated PreparePublicationRetry: %v", err)
	}
	if got := c.PendingPublications(); len(got) != 2 || got[1].OperationID != retry.OperationID {
		t.Fatalf("repeated retry duplicated/reordered queue: %+v", got)
	}
	if _, ok := c.State().OpOutcomes[second.OperationID]; ok {
		t.Fatal("successful retry left the retired input_busy proof claimable")
	}
}

func TestPendingPublications_RetryRequiresAuthenticatedExactInputBusyProof(t *testing.T) {
	for _, tc := range []struct {
		name         string
		terminalCode string
		outcomeCode  schema.ErrorCode
		mutate       func(*PendingPublication)
	}{
		{name: "accepted", terminalCode: PublicationAccepted},
		{name: "delivery_unknown", terminalCode: PublicationOutcomeUnknown,
			outcomeCode: schema.ErrorCode(PublicationOutcomeUnknown)},
		{name: "expired", terminalCode: PublicationExpired,
			outcomeCode: schema.ErrorCode(PublicationExpired)},
		{name: "other_refusal", terminalCode: "policy", outcomeCode: schema.CodePolicy},
		{name: "missing_authenticated_outcome", terminalCode: string(schema.CodeInputBusy)},
		{name: "wrong_text", terminalCode: string(schema.CodeInputBusy), outcomeCode: schema.CodeInputBusy,
			mutate: func(p *PendingPublication) { p.Text, p.Composer.Text = "different", "different" }},
		{name: "wrong_logical_id", terminalCode: string(schema.CodeInputBusy), outcomeCode: schema.CodeInputBusy,
			mutate: func(p *PendingPublication) { p.LogicalID = "another-logical-send" }},
		{name: "caller_phase_bypass", terminalCode: string(schema.CodeInputBusy), outcomeCode: schema.CodeInputBusy,
			mutate: func(p *PendingPublication) {
				p.Phase, p.Sequence, p.Envelope = PublicationSealed, 99, []byte("caller-envelope")
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := publicationCore(t)
			prior := testComposerPublication("op-prior")
			prior.Phase, prior.TerminalCode = PublicationTerminal, tc.terminalCode
			st := c.State()
			st.PendingPublications = []PendingPublication{prior}
			if tc.outcomeCode != "" {
				st.OpOutcomes = map[string]schema.Control{prior.OperationID: {
					Op: controlOpError, OperationID: prior.OperationID,
					SessionID: prior.SessionID, ErrorCode: tc.outcomeCode,
				}}
			}
			if err := c.Save(st); err != nil {
				t.Fatalf("seed prior: %v", err)
			}
			retry := retryPublication(prior, "op-retry")
			if tc.mutate != nil {
				tc.mutate(&retry)
			}
			if err := c.PreparePublicationRetry(prior.OperationID, retry); !errors.Is(err, ErrPublicationState) {
				t.Fatalf("PreparePublicationRetry = %v, want ErrPublicationState", err)
			}
			got := c.PendingPublications()
			if len(got) != 1 || got[0].OperationID != prior.OperationID {
				t.Fatalf("refused retry changed prior: %+v", got)
			}
		})
	}
}

func TestPendingPublications_ExpiryNeverReplaysAndLateAuthorityMayRefineIt(t *testing.T) {
	c := publicationCore(t)
	prepared := testComposerPublication("op-expired-before-append")
	prepared.Command.ExpiresAt = prepared.CreatedAt.Add(time.Minute)
	if err := c.PreparePublication(prepared); err != nil {
		t.Fatalf("prepare expiring: %v", err)
	}
	admitted := testComposerPublication("op-outcome-timeout")
	if err := c.PreparePublication(admitted); err != nil {
		t.Fatalf("prepare admitted: %v", err)
	}
	if err := c.SealPublication(admitted.OperationID, 91, []byte("exact")); err != nil {
		t.Fatalf("seal admitted: %v", err)
	}
	if err := c.MarkPublicationAdmitted(admitted.OperationID); err != nil {
		t.Fatalf("admit: %v", err)
	}
	if err := c.ExpirePublications(admitted.CreatedAt.Add(PublicationOutcomeTimeout)); err != nil {
		t.Fatalf("ExpirePublications: %v", err)
	}
	got := c.PendingPublications()
	if got[0].Phase != PublicationTerminal || got[0].TerminalCode != PublicationExpired {
		t.Fatalf("prepared expiry = %+v", got[0])
	}
	if got[1].Phase != PublicationTerminal || got[1].TerminalCode != PublicationOutcomeUnknown {
		t.Fatalf("admitted timeout = %+v", got[1])
	}
}

func TestPendingPublications_ProjectionAndStateCopiesOwnEnvelopeBytes(t *testing.T) {
	c := publicationCore(t)
	p := testComposerPublication("op-copy")
	if err := c.PreparePublication(p); err != nil {
		t.Fatalf("PreparePublication: %v", err)
	}
	envelope := []byte("exact sealed envelope")
	if err := c.SealPublication(p.OperationID, 41, envelope); err != nil {
		t.Fatalf("SealPublication: %v", err)
	}

	first := c.PendingPublications()
	if len(first) != 1 || first[0].Phase != PublicationSealed || first[0].Sequence != 41 {
		t.Fatalf("projection = %+v", first)
	}
	first[0].Envelope[0] ^= 0xff
	first[0].Composer.Text = "mutated"

	second := c.PendingPublications()
	if !bytes.Equal(second[0].Envelope, envelope) {
		t.Fatalf("caller mutated core envelope: %q", second[0].Envelope)
	}
	if second[0].Composer.Text != "ship it" {
		t.Fatalf("caller mutated core composer body: %q", second[0].Composer.Text)
	}
}

func TestPendingPublications_ExactPrepareAndSealAreIdempotentButConflictFails(t *testing.T) {
	c := publicationCore(t)
	p := testComposerPublication("op-idempotent")
	if err := c.PreparePublication(p); err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	if err := c.PreparePublication(p); err != nil {
		t.Fatalf("exact repeated prepare: %v", err)
	}
	conflict := testComposerPublication(p.OperationID)
	conflict.Text = "different"
	conflict.Composer.Text = "different"
	if err := c.PreparePublication(conflict); !errors.Is(err, ErrPublicationConflict) {
		t.Fatalf("conflicting prepare = %v, want ErrPublicationConflict", err)
	}
	if err := c.SealPublication(p.OperationID, 9, []byte("same")); err != nil {
		t.Fatalf("first seal: %v", err)
	}
	if err := c.SealPublication(p.OperationID, 9, []byte("same")); err != nil {
		t.Fatalf("exact repeated seal: %v", err)
	}
	if err := c.SealPublication(p.OperationID, 10, []byte("rival")); !errors.Is(err, ErrPublicationConflict) {
		t.Fatalf("conflicting seal = %v, want ErrPublicationConflict", err)
	}
}

func TestPendingPublications_PrepareCannotBypassTheTransitionAPI(t *testing.T) {
	for _, phase := range []PublicationPhase{
		PublicationSealed,
		PublicationAdmitted,
		PublicationTerminal,
	} {
		t.Run(string(phase), func(t *testing.T) {
			c := publicationCore(t)
			p := testComposerPublication("op-phase-" + string(phase))
			p.Phase = phase
			switch phase {
			case PublicationSealed, PublicationAdmitted:
				p.Sequence = 17
				p.Envelope = []byte("caller-supplied-envelope")
			case PublicationTerminal:
				p.TerminalCode = PublicationAuthorityChanged
			}
			if err := c.PreparePublication(p); !errors.Is(err, ErrPublicationState) {
				t.Fatalf("PreparePublication(%q) = %v, want ErrPublicationState", phase, err)
			}
			if got := c.PendingPublications(); len(got) != 0 {
				t.Fatalf("phase bypass entered durable state: %+v", got)
			}
		})
	}
}

func TestPendingPublications_PrepareRejectsTransitionOwnedFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PendingPublication)
	}{
		{"sequence", func(p *PendingPublication) { p.Sequence = 1 }},
		{"envelope", func(p *PendingPublication) { p.Envelope = []byte("caller-supplied") }},
		{"terminal_code", func(p *PendingPublication) { p.TerminalCode = "caller-supplied" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := publicationCore(t)
			p := testComposerPublication("op-" + tc.name)
			tc.mutate(&p)
			if err := c.PreparePublication(p); !errors.Is(err, ErrPublicationState) {
				t.Fatalf("PreparePublication = %v, want ErrPublicationState", err)
			}
		})
	}
}

func TestPendingPublications_StateRejectsDuplicateEpochSequence(t *testing.T) {
	c := publicationCore(t)
	first := testComposerPublication("op-sequence-first")
	second := testComposerPublication("op-sequence-second")
	for _, p := range []*PendingPublication{&first, &second} {
		p.Phase = PublicationSealed
		p.Sequence = 23
		p.Envelope = []byte("different-envelope-" + p.OperationID)
	}
	st := c.State()
	st.PendingPublications = []PendingPublication{first, second}
	if err := c.Save(st); !errors.Is(err, ErrPublicationConflict) {
		t.Fatalf("Save duplicate epoch/sequence = %v, want ErrPublicationConflict", err)
	}
}

func TestPendingPublications_StateRejectsInvalidResultOrdering(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*PendingPublication, *PendingPublication)
	}{
		{name: "duplicate_terminal_order", mutate: func(first, second *PendingPublication) {
			for _, p := range []*PendingPublication{first, second} {
				p.Phase, p.TerminalCode, p.ResultOrder = PublicationTerminal, "policy", 7
			}
		}},
		{name: "nonterminal_order", mutate: func(first, _ *PendingPublication) {
			first.ResultOrder = 7
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := publicationCore(t)
			first := testComposerPublication("op-result-order-first")
			second := testComposerPublication("op-result-order-second")
			tc.mutate(&first, &second)
			st := c.State()
			st.PendingPublications = []PendingPublication{first, second}
			if err := c.Save(st); err == nil {
				t.Fatal("invalid result ordering entered durable state")
			}
		})
	}
}

func TestPendingPublications_ResultOrderExhaustionRenormalizesWithoutChangingFIFO(t *testing.T) {
	first := testComposerPublication("op-result-near-max")
	first.Phase, first.TerminalCode, first.ResultOrder = PublicationTerminal, "policy", ^uint64(0)-1
	second := testComposerPublication("op-result-at-max")
	second.Phase, second.TerminalCode, second.ResultOrder = PublicationTerminal, "policy", ^uint64(0)
	newest := testComposerPublication("op-result-newest")
	st := State{PendingPublications: []PendingPublication{first, second, newest}}

	terminalizePublication(&st, &st.PendingPublications[2], string(schema.CodeInputBusy))

	if got := []string{
		st.PendingPublications[0].OperationID,
		st.PendingPublications[1].OperationID,
		st.PendingPublications[2].OperationID,
	}; !reflect.DeepEqual(got, []string{first.OperationID, second.OperationID, newest.OperationID}) {
		t.Fatalf("renormalization changed logical FIFO: %v", got)
	}
	for i, p := range st.PendingPublications {
		if p.ResultOrder != uint64(i+1) {
			t.Fatalf("renormalized result[%d] order = %d, want %d", i, p.ResultOrder, i+1)
		}
	}
}

func TestPendingPublications_SealRejectsAnotherRecordsEpochSequence(t *testing.T) {
	c := publicationCore(t)
	first := testComposerPublication("op-seal-first")
	second := testComposerPublication("op-seal-second")
	if err := c.PreparePublication(first); err != nil {
		t.Fatalf("prepare first: %v", err)
	}
	if err := c.PreparePublication(second); err != nil {
		t.Fatalf("prepare second: %v", err)
	}
	if err := c.SealPublication(first.OperationID, 29, []byte("first")); err != nil {
		t.Fatalf("seal first: %v", err)
	}
	if err := c.SealPublication(second.OperationID, 29, []byte("second")); !errors.Is(err, ErrPublicationConflict) {
		t.Fatalf("seal colliding epoch/sequence = %v, want ErrPublicationConflict", err)
	}
	got := c.PendingPublications()
	if got[1].Phase != PublicationPrepared || got[1].Sequence != 0 || len(got[1].Envelope) != 0 {
		t.Fatalf("failed collision mutated second record: %+v", got[1])
	}
}

func TestPendingPublications_LogicalIDAndRoutingAuthorityAreUniqueFences(t *testing.T) {
	c := publicationCore(t)
	first := testComposerPublication("op-first")
	if err := c.PreparePublication(first); err != nil {
		t.Fatalf("first prepare: %v", err)
	}

	duplicateIntent := testComposerPublication("op-second")
	duplicateIntent.LogicalID = first.LogicalID
	if err := c.PreparePublication(duplicateIntent); !errors.Is(err, ErrPublicationConflict) {
		t.Fatalf("same logical id under another operation = %v, want ErrPublicationConflict", err)
	}

	wrongAuthority := testComposerPublication("op-wrong-authority")
	wrongAuthority.AuthorityPub = bytes.Repeat([]byte{0x55}, 32)
	if err := c.PreparePublication(wrongAuthority); !errors.Is(err, ErrPublicationState) {
		t.Fatalf("wrong routing authority = %v, want ErrPublicationState", err)
	}
}

func TestPendingPublications_RegistrationIdentityChangePurgesRatherThanRetargets(t *testing.T) {
	c := publicationCore(t)
	if err := c.PreparePublication(testComposerPublication("op-old-authority")); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	st := c.State()
	st.MachineRelayAuthPub = bytes.Repeat([]byte{0x66}, 32)
	if err := c.Save(st); err != nil {
		t.Fatalf("replace routing authority: %v", err)
	}
	got := c.PendingPublications()
	if len(got) != 1 || got[0].Phase != PublicationTerminal || got[0].TerminalCode != PublicationAuthorityChanged {
		t.Fatalf("replacement did not quarantine the old publication: %+v", got)
	}
	outcome, ok := c.State().OpOutcomes[got[0].OperationID]
	if !ok || outcome.ErrorCode != schema.ErrorCode(PublicationAuthorityChanged) {
		t.Fatalf("replacement did not author a visible authority_changed outcome: %+v, %v", outcome, ok)
	}
}

func TestPendingPublications_IdentityChangePreservesDeliveryUncertaintyAndTerminalVerdicts(t *testing.T) {
	c := publicationCore(t)
	st := c.State()
	prepared := testComposerPublication("op-prepared-change")
	sealed := testComposerPublication("op-sealed-change")
	sealed.Phase, sealed.Sequence, sealed.Envelope = PublicationSealed, 51, []byte("sealed")
	admitted := testComposerPublication("op-admitted-change")
	admitted.Phase, admitted.Sequence, admitted.Envelope = PublicationAdmitted, 52, []byte("admitted")
	accepted := testComposerPublication("op-accepted-change")
	accepted.Phase, accepted.TerminalCode = PublicationTerminal, PublicationAccepted
	busy := testComposerPublication("op-busy-change")
	busy.Phase, busy.TerminalCode = PublicationTerminal, string(schema.CodeInputBusy)
	unknown := testComposerPublication("op-unknown-change")
	unknown.Phase, unknown.TerminalCode = PublicationTerminal, PublicationOutcomeUnknown
	st.PendingPublications = []PendingPublication{prepared, sealed, admitted, accepted, busy, unknown}
	st.OpOutcomes = map[string]schema.Control{
		accepted.OperationID: {Op: controlOpOK, OperationID: accepted.OperationID, SessionID: accepted.SessionID},
		busy.OperationID: {
			Op: controlOpError, OperationID: busy.OperationID, SessionID: busy.SessionID,
			ErrorCode: schema.CodeInputBusy, Error: "provider wrote no bytes",
		},
		unknown.OperationID: localPublicationOutcome(unknown, PublicationOutcomeUnknown),
	}
	if err := c.Save(st); err != nil {
		t.Fatalf("seed mixed phases: %v", err)
	}
	wantTerminalOutcomes := map[string]schema.Control{
		accepted.OperationID: st.OpOutcomes[accepted.OperationID],
		busy.OperationID:     st.OpOutcomes[busy.OperationID],
		unknown.OperationID:  st.OpOutcomes[unknown.OperationID],
	}
	st = c.State()
	st.MachineRelayAuthPub = bytes.Repeat([]byte{0x77}, 32)
	if err := c.Save(st); err != nil {
		t.Fatalf("replace authority: %v", err)
	}
	got := c.State()
	wantCodes := map[string]string{
		prepared.OperationID: PublicationAuthorityChanged,
		sealed.OperationID:   PublicationOutcomeUnknown,
		admitted.OperationID: PublicationOutcomeUnknown,
		accepted.OperationID: PublicationAccepted,
		busy.OperationID:     string(schema.CodeInputBusy),
		unknown.OperationID:  PublicationOutcomeUnknown,
	}
	for _, p := range got.PendingPublications {
		if p.Phase != PublicationTerminal || p.TerminalCode != wantCodes[p.OperationID] {
			t.Errorf("identity transition %q = phase %q code %q, want terminal/%q",
				p.OperationID, p.Phase, p.TerminalCode, wantCodes[p.OperationID])
		}
		outcome, ok := got.OpOutcomes[p.OperationID]
		if !ok {
			t.Errorf("identity transition %q has no durable outcome", p.OperationID)
			continue
		}
		if want, preserved := wantTerminalOutcomes[p.OperationID]; preserved && !reflect.DeepEqual(outcome, want) {
			t.Errorf("terminal outcome %q changed: got %+v want %+v", p.OperationID, outcome, want)
		}
	}
	retry := retryPublication(busy, "op-busy-after-authority-change")
	retry.AuthorityPub = bytes.Repeat([]byte{0x77}, 32)
	if err := c.PreparePublicationRetry(busy.OperationID, retry); !errors.Is(err, ErrPublicationState) {
		t.Fatalf("old-authority input_busy authorized a retry: %v", err)
	}
}

func TestPendingPublications_IdentityChangeBoundsTerminalizedMixedCapacity(t *testing.T) {
	c := publicationCore(t)
	st := c.State()
	st.OpOutcomes = map[string]schema.Control{
		"unrelated-operation": {Op: "error", OperationID: "unrelated-operation", ErrorCode: schema.CodePolicy},
	}
	for i := 0; i < maxPublicationResults; i++ {
		p := testComposerPublication(fmt.Sprintf("old-result-%02d", i))
		p.Phase = PublicationTerminal
		p.TerminalCode = PublicationAuthorityChanged
		st.PendingPublications = append(st.PendingPublications, p)
		st.OpOutcomes[p.OperationID] = schema.Control{
			Op: "error", OperationID: p.OperationID, ErrorCode: schema.ErrorCode(PublicationAuthorityChanged),
		}
	}
	for i := 0; i < MaxPendingPublications; i++ {
		p := testComposerPublication(fmt.Sprintf("unresolved-%02d", i))
		st.PendingPublications = append(st.PendingPublications, p)
	}
	if err := c.Save(st); err != nil {
		t.Fatalf("seed full mixed projection: %v", err)
	}
	st = c.State()
	st.Machine = "replacement-machine"
	st.EpochID = 8
	st.MachineRelayAuthPub = bytes.Repeat([]byte{0x55}, 32)
	if err := c.Save(st); err != nil {
		t.Fatalf("identity replacement was blocked by terminal overflow: %v", err)
	}
	got := c.PendingPublications()
	if len(got) != maxPublicationResults {
		t.Fatalf("identity replacement retained %d results, want %d", len(got), maxPublicationResults)
	}
	for _, p := range got {
		if p.Phase != PublicationTerminal || p.TerminalCode != PublicationAuthorityChanged {
			t.Fatalf("old authority remains publishable: %+v", p)
		}
	}
	if got[0].OperationID != "unresolved-00" || got[len(got)-1].OperationID != "unresolved-63" {
		t.Fatalf("result bound did not preserve newest retired intents: first=%q last=%q",
			got[0].OperationID, got[len(got)-1].OperationID)
	}
	outcomes := c.State().OpOutcomes
	if _, ok := outcomes["old-result-63"]; ok {
		t.Fatal("evicted old-authority result remained claimable")
	}
	if _, ok := outcomes["unrelated-operation"]; !ok {
		t.Fatal("identity result bounding removed an unrelated outcome")
	}
	for _, p := range got {
		outcome, ok := outcomes[p.OperationID]
		if !ok || outcome.ErrorCode != schema.ErrorCode(PublicationAuthorityChanged) {
			t.Fatalf("retired intent %q has no authority_changed outcome: %+v, %v", p.OperationID, outcome, ok)
		}
	}
}

func TestPendingPublications_AdmissionIsMonotonicAndIdempotent(t *testing.T) {
	c := publicationCore(t)
	p := testComposerPublication("op-admitted")
	if err := c.PreparePublication(p); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := c.MarkPublicationAdmitted(p.OperationID); !errors.Is(err, ErrPublicationState) {
		t.Fatalf("prepared -> admitted = %v, want ErrPublicationState", err)
	}
	if err := c.SealPublication(p.OperationID, 12, []byte("exact")); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if err := c.MarkPublicationAdmitted(p.OperationID); err != nil {
		t.Fatalf("admit: %v", err)
	}
	if err := c.MarkPublicationAdmitted(p.OperationID); err != nil {
		t.Fatalf("repeat admit: %v", err)
	}
	got := c.PendingPublications()
	if len(got) != 1 || got[0].Phase != PublicationAdmitted || got[0].Sequence != 12 ||
		!bytes.Equal(got[0].Envelope, []byte("exact")) {
		t.Fatalf("admitted projection = %+v", got)
	}
}

func TestPendingPublications_SurviveRestartAndRevokePurgesThem(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	c, err := Resume(Config{Dir: dir, Machine: "m1", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	st := c.State()
	st.Machine, st.EpochID, st.RoutingID, st.Keys = "m1", 7, "rid-m1", s15EpochKeys()
	st.MachineRelayAuthPub = bytes.Repeat([]byte{0x44}, 32)
	if err := c.Save(st); err != nil {
		t.Fatalf("seed: %v", err)
	}
	p := testComposerPublication("op-restart")
	if err := c.PreparePublication(p); err != nil {
		t.Fatalf("PreparePublication: %v", err)
	}
	if err := c.SealPublication(p.OperationID, 77, []byte("ciphertext")); err != nil {
		t.Fatalf("SealPublication: %v", err)
	}

	restarted, err := Resume(Config{Dir: dir, Machine: "m1", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if got := restarted.PendingPublications(); len(got) != 1 || got[0].OperationID != p.OperationID {
		t.Fatalf("restart projection = %+v", got)
	}
	if err := restarted.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}
	again, err := Resume(Config{Dir: dir, Machine: "m1", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("restart after revoke: %v", err)
	}
	if got := again.PendingPublications(); len(got) != 0 {
		t.Fatalf("revoked phone restored pending publications: %+v", got)
	}
}

func TestPendingPublications_V17MigratesToAnEmptyJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFileName)
	if err := os.WriteFile(path, []byte(stateV17Fixture), 0o600); err != nil {
		t.Fatalf("write v17 fixture: %v", err)
	}
	kek := &s14aSealer{kek: stateV4FixtureKEK}
	store, err := OpenStore(path, "m1", kek, kek)
	if err != nil {
		t.Fatalf("OpenStore v17: %v", err)
	}
	if got := store.Load().PendingPublications; len(got) != 0 {
		t.Fatalf("v17 invented pending publications: %+v", got)
	}
}

func TestPendingPublications_DirectStateSaveCannotBypassTheBound(t *testing.T) {
	c := publicationCore(t)
	st := c.State()
	for i := 0; i <= MaxPendingPublications; i++ {
		st.PendingPublications = append(st.PendingPublications, testComposerPublication(
			"over-"+string(rune('A'+i)),
		))
	}
	if err := c.Save(st); !errors.Is(err, ErrPublicationQueueFull) {
		t.Fatalf("oversized State Save = %v, want ErrPublicationQueueFull", err)
	}
	if got := c.PendingPublications(); len(got) != 0 {
		t.Fatalf("failed oversized Save changed the durable journal: %+v", got)
	}
}
