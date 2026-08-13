package skeleton

// THE CHAIN TEST: the REAL Claude Code producer's items, over the whole shipped stack, to the
// screen the owner would tap.
//
// interaction_e2e_test.go proved the CARRIAGE with a scripted double in the adapter seat, and
// said so in its own header: "the one hop this does not prove is the producer's own content: a
// real CLI's hook body shaped by a real adapter into real items". That producer has since
// landed (internal/adapter/claude, docs/verification/a1b-claude-producer.md). This file closes
// the sentence -- the same rig, the same hops, with the double replaced by the shipped shaper
// and the recorded S-B corpus replayed through it:
//
//	corpus     REAL RECORDED BYTES. internal/adapter/claude/testdata/interaction/*.json, read
//	           from the producer's own directory rather than copied here -- a second copy would
//	           drift from the corpus the producer's golden table is written against, and then
//	           this file would be fencing a fixture nobody else uses.
//	shaper     REAL. internal/adapter/claude's InteractionSource, constructed by the production
//	           claude.New(), doing its own parsing of its own hook bodies.
//	producer   REAL. Validate -> §2 envelope -> the D7 tuple and the content seal -> §5's caps
//	           -> ADR-010 §7's append floor -> journal.
//	gateway    REAL, a SEPARATE PROCESS (cmd/swarm-remote).
//	relay      REAL internal/remote/relay.Server over a real localhost WebSocket.
//	phone      REAL swarmmobile.App over a durable phonecore.Core, paired through the ceremony.
//
// WHERE IT ENTERS, AND WHAT COVERS THE HOP ABOVE. This file enters at captureInteractions --
// the production entry point serveHookInteractions itself calls -- and the payload replayed
// into it is the recorded HookPayload the carriage delivers. ADR-010 §6's carriage above that
// entry point (`engine.Callback` carries `Raw`, `swarm hook` keeps the CLI's body for its
// capture=raw rows) was NOT IMPLEMENTED when this file was written, which
// docs/verification/a1b-claude-producer.md §10 measured as the producer shaping nothing in
// production. It is implemented now, and interaction_carriage_test.go enters one hop higher, at
// hookclient.Post over the daemon socket, so that hop is covered where it lives rather than by
// widening this test's mouth (docs/verification/i1-carriage.md).
//
// Entering there also means the adapter is HANDED IN rather than looked up, so the one further
// thing this does not exercise is `adapterFor`/`resolveAdapter` choosing `claude` for a claude
// session. That is registry behaviour with its own tests, and the alternative -- overwriting
// `rig.sk.adapterFor` from the test goroutine while the daemon reads it under `itemMu` -- would
// buy a lookup at the price of an unsynchronized write on a rig that runs under -race.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/adapter/fixtureio"
	"github.com/Nathandela/swarm/internal/protocol"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// claudeCorpusDir is the producer's own recorded corpus, referenced and never copied. See the
// file header, and PROVENANCE.md beside the fixtures.
const claudeCorpusDir = "../adapter/claude/testdata/interaction"

// replayClaudeCorpus drives one recorded fixture's hook payloads through the REAL Claude Code
// shaper into the daemon's production capture path, in recording order.
//
// THE BODIES ARE VERBATIM; THE CAPTURE INSTANT IS THIS DAEMON'S OWN, and that distinction is
// load-bearing rather than convenient. ADR-010 §3 makes timestamps daemon-authoritative, and a
// fixture's `received_at_ms` records when the RECORDING was taken (2026-07-18) -- it is not an
// instant at which this daemon captured anything. Replaying it as one makes the approval's
// window (`it.TS.Add(approvalTTL)`, approval.go) open and close three weeks in the past, so the
// expiry sweep resolves the card `expired` before any owner could answer it. That is what the
// first RED measured, verbatim in a1b-claude-producer.md §9.
func replayClaudeCorpus(t *testing.T, sk *Daemon, session, fixture string) {
	t.Helper()
	fx, err := fixtureio.LoadFixture(filepath.Join(claudeCorpusDir, fixture))
	if err != nil {
		t.Fatalf("LoadFixture(%s): %v", fixture, err)
	}
	ad := claude.New()
	for _, hp := range fx.HookPayloads {
		hp.ReceivedAtMs = time.Now().UnixMilli()
		sk.captureInteractions(session, ad, hp)
	}
}

// TestClaudeChainE2E_TheRecordedCorpusRendersAndBothVerdictsResolveOnThePhone.
func TestClaudeChainE2E_TheRecordedCorpusRendersAndBothVerdictsResolveOnThePhone(t *testing.T) {
	rig := newS19Rig(t)
	rig.Pair()
	rig.StartGateway()
	rig.Eventually("the machine's reconcile record reached the phone", func() bool {
		return rig.Summary().Reconciled
	})

	// The session PAINTS a recorded claude permission dialog and blocks, because since M1.2 a
	// phone answer is APPLIED by typing that dialog's own keys into this PTY (mirror-program.md
	// section 3): an approve onto a session showing no dialog is refused, not resolved. The
	// adapter resolver is the real claude one for the same reason -- it holds the key map.
	dialog, cols, rows := gridScript(t, bashDialogGrid)
	sessionID := rig.LaunchOnMachineSized(dialog, cols, rows)
	rig.sk.adapterFor = func(string) (adapter.Adapter, bool) { return claude.New(), true }
	rig.Eventually("the phone's roster shows the session the machine launched", func() bool {
		return rig.RosterHas(sessionID)
	})
	_, localID, ok := protocol.ParseID(sessionID)
	if !ok {
		t.Fatalf("owner Launch returned %q, which is not a namespaced id", sessionID)
	}

	// ---- leg 1: one recorded Edit turn, whole -------------------------------
	// The fixture is a real Claude Code turn: a prompt, a Read, an Edit that escalated to a
	// permission dialog, and the applied change. Five distinct items out of seven records.
	replayClaudeCorpus(t, rig.sk, localID, "claude-edit-permissionrequest-run1.json")

	var transcript []swarmmobile.TranscriptItem
	rig.Eventually("the recorded Edit turn's five items reached the phone", func() bool {
		transcript = readTranscript(t, rig, sessionID)
		return len(transcript) >= 5
	})
	byKind := transcriptByKind(transcript)

	// -- the prompt the owner actually typed, verbatim across five hops --------
	msgs := byKind["user_message"]
	if len(msgs) != 1 {
		t.Fatalf("the phone holds %d user_message(s); the recorded turn has exactly one "+
			"UserPromptSubmit%s", len(msgs), rig.gatewayTail())
	}
	const prompt = "Using the Edit tool, change the text 'line two' to 'line TWO EDITED' in edit-target3.txt"
	if msgs[0].Text != prompt {
		t.Errorf("user_message text = %q; want the recorded prompt %q", msgs[0].Text, prompt)
	}

	// -- the tool runs, OPEN AND CLOSE FOLDED INTO ONE ITEM EACH ---------------
	// IS-DELTA-3: the close carries the open's ref, the daemon folds both under one item_id
	// (itemIDLocked), and the phone folds by item_id (IS-ENV-2). Two tool calls in the
	// recording, FOUR records, and the owner must see two rows -- not four, and not two rows
	// stuck `in_progress` on a turn that finished.
	runs := byKind["tool_run"]
	if len(runs) != 2 {
		t.Fatalf("the phone holds %d tool_run item(s); the recorded turn made TWO tool calls and "+
			"emitted FOUR records for them. A count of 4 is the fold failing (IS-DELTA-3/IS-ENV-2); "+
			"items: %v%s", len(runs), transcriptKinds(transcript), rig.gatewayTail())
	}
	tools := map[string]swarmmobile.TranscriptItem{}
	for _, it := range runs {
		if it.Status != "completed" {
			t.Errorf("a tool_run reached the phone with status %q; both recorded calls returned, and "+
				"an item left `in_progress` is a row that spins forever (IS-ST-1)", it.Status)
		}
		body := itemBody(t, it)
		tool, _ := body["tool"].(string)
		tools[tool] = it
	}
	read, hasRead := tools["Read"]
	if _, hasEdit := tools["Edit"]; !hasRead || !hasEdit {
		t.Fatalf("the phone's tool_run rows name %v; the recording ran Read then Edit", mapKeys(tools))
	}
	// §7's structured action, produced machine-side so the card reads "Read <path>" without the
	// phone parsing an argument (IS-TOOL-1).
	if act, _ := itemBody(t, read)["action"].(map[string]any); act["type"] != "read" ||
		act["path"] != "/Users/Nathan/spike-sb-work/edit-target3.txt" {
		t.Errorf("the Read tool_run's action = %v; want {type:read, path:/Users/Nathan/spike-sb-work/edit-target3.txt}", act)
	}
	if got, _ := itemBody(t, read)["output_excerpt"].(string); got != "line one\nline two\nline three\n" {
		t.Errorf("the Read tool_run's output_excerpt = %q; want the file the CLI actually read. The "+
			"excerpt is the only thing on that row a human reads", got)
	}

	// -- the applied change ---------------------------------------------------
	// IS-FC-1: only an APPLIED change is a file_change, so it hangs off PostToolUse's
	// structuredPatch and not off the proposed edit.
	changes := byKind["file_change"]
	if len(changes) != 1 {
		t.Fatalf("the phone holds %d file_change(s); the recorded turn applied exactly one edit%s",
			len(changes), rig.gatewayTail())
	}
	fc := itemBody(t, changes[0])
	if fc["path"] != "/Users/Nathan/spike-sb-work/edit-target3.txt" || fc["change"] != "modify" {
		t.Errorf("the file_change names %v/%v; want the edited path, modified", fc["path"], fc["change"])
	}
	if fc["added"] != float64(1) || fc["removed"] != float64(1) {
		t.Errorf("the file_change counts +%v/-%v; the recorded hunk replaced one line with one line",
			fc["added"], fc["removed"])
	}
	if diff, _ := fc["diff_excerpt"].(string); diff != "@@ -1,3 +1,3 @@\n line one\n-line two\n+line TWO EDITED\n line three" {
		t.Errorf("the file_change's diff_excerpt = %q; want the hunk rendered from the recorded "+
			"structuredPatch -- it is the whole of what the owner is shown about the edit", diff)
	}

	// -- the card ------------------------------------------------------------
	cards := byKind["approval_request"]
	if len(cards) != 1 {
		t.Fatalf("the phone holds %d approval_request(s); the recording escalated exactly once%s",
			len(cards), rig.gatewayTail())
	}
	card := cards[0]
	cardBody := itemBody(t, card)
	if cardBody["summary"] != "Edit /Users/Nathan/spike-sb-work/edit-target3.txt" {
		t.Errorf("the card's summary = %v; want the LITERAL action the owner is authorizing", cardBody["summary"])
	}
	// §3.5 keeps the ids the CLI's OWN. These two are Claude Code's `behavior` values, and the
	// card labels its buttons from decisions[].label (IS-APR-3).
	assertDecisions(t, cardBody, map[string]string{"allow": "Yes", "deny": "No"})
	if cardBody["mode"] != "card" {
		t.Errorf("the Edit card's mode = %v; want `card` -- S-C measured Edit's PermissionRequest hook "+
			"resolving natively, and a prompt_card here would put a keystroke behind a one-tap button",
			cardBody["mode"])
	}
	assertNoKeystrokeLeak(t, cardBody)

	// -- one turn, holding all five ------------------------------------------
	// IS-ENV-1: the turn OPENS on the user_message, and every item inside carries its id. A
	// transcript whose rows carry no common turn cannot be grouped into the exchange that
	// produced them -- which is the whole of what makes it a transcript and not a log.
	turn := msgs[0].TurnID
	if turn == "" {
		t.Fatalf("the user_message that opened the turn carries no turn_id (IS-ENV-1)")
	}
	for _, it := range transcript {
		if it.TurnID != turn {
			t.Errorf("the %s item carries turn_id %q; the turn the recorded prompt opened is %q. Every "+
				"item captured inside a turn belongs to it (IS-ENV-1)", it.Kind, it.TurnID, turn)
		}
	}

	// ---- leg 2: the allow verdict, phone to machine and back ----------------
	pending := readPendingApprovals(t, rig)
	if len(pending) != 1 || pending[0].ItemID != card.ItemID {
		t.Fatalf("PendingApprovals holds %d card(s) %v; want exactly the unresolved %q. The resolutions "+
			"the phone already holds are %v -- a card resolved before anyone answered it is a request "+
			"the owner never got to decide", len(pending), transcriptIDs(pending), card.ItemID,
			resolutionsOn(t, rig, sessionID))
	}
	// EVERY FIELD ECHOED OFF THE PHONE'S OWN COPY -- IS-APR-2 forbids the phone computing or
	// adjusting content_hash or expires_at, so the approve is built from the card as it ARRIVED
	// rather than from anything the daemon still holds. That is what makes this a round trip.
	code, err := rig.sk.approveInteraction(rig.sk.api.endpointID, "op-allow",
		approveFor(t, rig.sk, localID, cardBody, "allow"))
	if err != nil {
		t.Fatalf("an approve echoed verbatim off the phone's own card was refused %q: %v%s",
			code, err, rig.gatewayTail())
	}
	// M1.2: the daemon TYPED the answer into the dialog and resolved nothing. The record lands
	// on the machine's own observation that the dialog left the screen, and carries the phone
	// because the daemon knows which key it pressed.
	dialogLeaves(rig.sk, localID)

	resolved := awaitFacadeResolution(t, rig, sessionID, card.ItemID)
	if resolved["decision"] != "allowed" {
		t.Errorf("the resolution's decision = %v; want `allowed`. The chosen id `allow` carries "+
			"Verdict=allow from the adapter's own capture, and §3.6's allowed/denied split is read "+
			"off exactly that (owner ruling 2026-08-07)", resolved["decision"])
	}
	if resolved["by"] != "phone" || resolved["operation_id"] != "op-allow" {
		t.Errorf("the resolution is attributed by=%v operation_id=%v; want phone/op-allow (§3.6)",
			resolved["by"], resolved["operation_id"])
	}

	// IS-LIFE-3's retention EXEMPTION lifting is the half only an end-to-end run can see: the
	// exemption keeps an unresolved card unevictable, and a request that never resolves is a
	// leak of exactly that kind. It lifts when the resolution folds, not when the daemon decides.
	rig.Eventually("the answered card left the phone's pending set", func() bool {
		return len(readPendingApprovals(t, rig)) == 0
	})
	if got := findItem(readTranscript(t, rig, sessionID), card.ItemID); got == nil || !got.Resolved {
		t.Errorf("the answered approval_request is %v on the phone; a resolution marks the request "+
			"Resolved -- which is what ends its IS-LIFE-3 exemption -- and it must NOT delete the "+
			"record, because a transcript that erases what it answered cannot show what was decided", got)
	}

	// ---- leg 3: the deny verdict, on a second recorded request --------------
	// A different fixture, and deliberately the CARVE-OUT one: `touch approval-test.txt` names a
	// file path, which S-C measured tripping a second confirmation the hook's allow does not
	// resolve, so this request declares prompt_card AT CAPTURE.
	replayClaudeCorpus(t, rig.sk, localID, "claude-bash-permissionrequest-run1.json")

	var second swarmmobile.TranscriptItem
	rig.Eventually("the second recorded request raised a new card on the phone", func() bool {
		p := readPendingApprovals(t, rig)
		if len(p) == 1 && p[0].ItemID != card.ItemID {
			second = p[0]
			return true
		}
		return false
	})
	secondBody := itemBody(t, second)
	if secondBody["mode"] != "prompt_card" {
		t.Errorf("the Bash card's mode = %v; want `prompt_card` -- S-C's carve-out is decided at "+
			"capture, and a `card` here renders a one-tap button that silently leaves the session "+
			"blocked behind a dialog nobody is at the machine to answer", secondBody["mode"])
	}
	assertDecisions(t, secondBody, map[string]string{"allow": "Yes", "deny": "No"})
	// THIS is the card the leak fence bites on. The prompt-card path is the ONLY one for which
	// the adapter produces a keystroke map at all (claude's approvalFrom), so checking the native
	// `card` above proves nothing on its own -- there is nothing there to leak. Measured: mutation
	// 3 in a1b-claude-producer.md §11 emitted `keystrokes` from interactionFields and the suite
	// stayed green until this call existed.
	assertNoKeystrokeLeak(t, secondBody)

	code, err = rig.sk.approveInteraction(rig.sk.api.endpointID, "op-deny",
		approveFor(t, rig.sk, localID, secondBody, "deny"))
	if err != nil {
		t.Fatalf("the deny was refused %q: %v%s", code, err, rig.gatewayTail())
	}
	dialogLeaves(rig.sk, localID)
	denied := awaitFacadeResolution(t, rig, sessionID, second.ItemID)
	if denied["decision"] != "denied" {
		t.Fatalf("the resolution's decision = %v; want `denied`. `deny` is the CLI's own id and carries "+
			"nothing about polarity on the wire -- the ONLY thing that makes this a refusal rather than "+
			"an approval is the Verdict the ADAPTER attached at capture, which is why the two legs of "+
			"this test differ in exactly one string", denied["decision"])
	}
	rig.Eventually("the refused card left the phone's pending set", func() bool {
		return len(readPendingApprovals(t, rig)) == 0
	})
}

// ---- readers ---------------------------------------------------------------

// awaitFacadeResolution waits for the approval_resolved that answers interactionID to reach the
// PHONE and returns its decoded body. It reads the facade rather than the journal on purpose:
// the resolution's whole job is to dismiss a card on the surface holding it.
func awaitFacadeResolution(t *testing.T, r *s19Rig, session, interactionID string) map[string]any {
	t.Helper()
	var out map[string]any
	r.Eventually("the approval_resolved for "+interactionID+" reached the phone", func() bool {
		for _, it := range readTranscript(t, r, session) {
			if it.Kind != "approval_resolved" {
				continue
			}
			body := itemBody(t, it)
			if body["interaction_id"] == interactionID {
				out = body
				return true
			}
		}
		return false
	})
	return out
}

// resolutionsOn lists `decision/by` for every approval_resolved the phone holds for the session.
// It is a diagnostic and not an assertion: a card that vanished from PendingApprovals did so for
// exactly one of §3.6's six reasons, and naming which one is the difference between "the chain
// dropped it" and "something resolved it first".
func resolutionsOn(t *testing.T, r *s19Rig, session string) []string {
	t.Helper()
	var out []string
	for _, it := range readTranscript(t, r, session) {
		if it.Kind == "approval_resolved" {
			body := itemBody(t, it)
			out = append(out, fmt.Sprintf("%v/%v", body["decision"], body["by"]))
		}
	}
	return out
}

// itemBody decodes a transcript item's raw body. gomobile binds no map, so §3's per-kind fields
// cross as the item's own JSON and the client decodes them (IS-COMPAT-1/-2) -- which is exactly
// what this does.
func itemBody(t *testing.T, it swarmmobile.TranscriptItem) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(it.Body), &body); err != nil {
		t.Fatalf("the %s item's body is not a JSON object: %v (%q)", it.Kind, err, it.Body)
	}
	return body
}

func transcriptByKind(items []swarmmobile.TranscriptItem) map[string][]swarmmobile.TranscriptItem {
	out := map[string][]swarmmobile.TranscriptItem{}
	for _, it := range items {
		out[it.Kind] = append(out[it.Kind], it)
	}
	return out
}

func findItem(items []swarmmobile.TranscriptItem, itemID string) *swarmmobile.TranscriptItem {
	for i := range items {
		if items[i].ItemID == itemID {
			return &items[i]
		}
	}
	return nil
}

func mapKeys(m map[string]swarmmobile.TranscriptItem) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// assertNoKeystrokeLeak checks that the decision->keystroke map stayed MACHINE-SIDE. IS-APR-3
// forbids it on the item and IS-LIFE-6 forbids the phone authoring a keystroke; it rides
// adapter.Interaction only because that is the one adapter->core carrier, and the daemon must
// drop it before anything is journaled. A phone that received one could press a key.
func assertNoKeystrokeLeak(t *testing.T, body map[string]any) {
	t.Helper()
	if ks, leaked := body["keystrokes"]; leaked {
		t.Errorf("a %v approval_request carried a `keystrokes` map to the phone: %v. IS-APR-3 keeps it "+
			"off the item and IS-LIFE-6 keeps the phone out of the keyboard", body["mode"], ks)
	}
}

// assertDecisions checks the card's buttons: the ids are the CLI's own (§3.5), the labels are
// what IS-APR-3 makes the phone render, and NOTHING ELSE rides along.
//
// The exact-key check is IS-APR-4's second half, and it is the `keystrokes` fence's twin. The
// verdict the adapter attaches at capture is MACHINE-SIDE: the daemon reads it to classify
// §3.6's allowed/denied and the phone switches on nothing, so a copy on the wire would be a
// second place for the two to disagree -- and a phone that received one could render a polarity
// the daemon never resolved from. Measured: emitting `verdict` beside `id`/`label` in
// interactionFields left the whole suite green until this check existed.
func assertDecisions(t *testing.T, body map[string]any, want map[string]string) {
	t.Helper()
	raw, ok := body["decisions"].([]any)
	if !ok || len(raw) != len(want) {
		t.Fatalf("the card offers %v; want %d decision(s) %v -- a card with no buttons cannot be "+
			"answered from the phone at all", body["decisions"], len(want), want)
	}
	for _, d := range raw {
		obj, _ := d.(map[string]any)
		id, _ := obj["id"].(string)
		label, ok := want[id]
		if !ok {
			t.Errorf("the card offers decision id %q, which is not one Claude Code's PermissionRequest "+
				"hook accepts; §3.5 keeps the ids the CLI's own so Decision can answer them", id)
			continue
		}
		if obj["label"] != label {
			t.Errorf("decision %q is labelled %v; want %q", id, obj["label"], label)
		}
		for k := range obj {
			if k != "id" && k != "label" {
				t.Errorf("decision %q carried %q to the phone: %v. §3.5 puts {id, label} on the wire and "+
					"IS-APR-4 keeps the verdict MACHINE-SIDE -- the daemon classifies allowed/denied from "+
					"it and no phone surface switches on polarity", id, k, obj[k])
			}
		}
	}
}
