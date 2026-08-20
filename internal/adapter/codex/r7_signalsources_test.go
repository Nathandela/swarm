package codex

// FAILING-FIRST (TDD RED, GG-5) for Mirror M4.5's adapter half: typed events replace the
// grid heuristic as Codex's status driver, which pays the D1 debt this package's own header
// has carried since Epic 14 ("the live app-server typed-event producer is deferred to
// Epic 14's flagged real-CLI smoke; the typed mapping here is fixture-proven pending that
// live wiring", codex.go:14-16). Bead: agents-tracker-hggx.8. ADR-013 §R7.7's last section.
//
// M4.5 PAYS THE DEBT BY BUILDING THE PRODUCER, NOT BY CHANGING THE MAPPING. The three rows
// that exist are correct and stay. Two are ADDED, both from RECORDED frames, and the
// heuristic row STAYS -- it is ADR-007's T-3 fallback, it is the only thing that keeps a
// pre-R7 Codex session working, and internal/engine already ranks a fresh typed signal above
// it within StalenessThreshold so the two cannot fight.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// r7Rows indexes the declared event rows by their event name.
func r7Rows(t *testing.T) map[string]map[string]string {
	t.Helper()
	rows := map[string]map[string]string{}
	for _, s := range New().SignalSources() {
		if s.Kind != "event" {
			continue
		}
		rows[s.Descriptor["event"]] = s.Descriptor
	}
	return rows
}

// TestR7CodexSignalSources_DeclaresTheRecordedApprovalMethodTheGateActuallyCaptured closes a
// real hole. The adapter declares `item/commandExecution/requestApproval` and NOTHING ELSE
// for permission, while the R1 gate's approval arrived as `item/fileChange/requestApproval`
// (approval-request.json) -- so on the one approval flow anybody has ever run end to end,
// the declared mapping does not fire.
//
// The exact set of server-to-client request methods is not guessable from
// protocol-methods.txt, which inventories notifications only; it comes from the generated
// ServerRequest union and is recorded at r7-schema-methods.txt.
func TestR7CodexSignalSources_DeclaresTheRecordedApprovalMethodTheGateActuallyCaptured(t *testing.T) {
	rows := r7Rows(t)

	row, ok := rows["item/fileChange/requestApproval"]
	if !ok {
		t.Fatal("no row for item/fileChange/requestApproval; that is the approval the R1 gate " +
			"CAPTURED (approval-request.json), and without the row a Codex session sitting on a " +
			"file-change approval reads idle on the phone")
	}
	if row["interaction"] != "permission" {
		t.Errorf("item/fileChange/requestApproval maps interaction %q, want \"permission\"", row["interaction"])
	}
	if sibling, ok := rows["item/commandExecution/requestApproval"]; !ok || sibling["interaction"] != "permission" {
		t.Error("the pre-existing item/commandExecution/requestApproval row was removed or changed; " +
			"M4.5 pays the D1 debt by BUILDING THE PRODUCER, not by rewriting a mapping that was " +
			"already correct")
	}
}

// TestR7CodexSignalSources_ServerRequestResolvedClearsThePermissionInteraction is the row
// without which `permission` sticks until turn/completed. The server broadcasts
// `serverRequest/resolved` to every attached client the instant ANY of them answers
// (RECORDED: frame-samples.json, and r1-codex-gate.md:129-131 -- it is what lets the surface
// that did not answer retire its dialog).
func TestR7CodexSignalSources_ServerRequestResolvedClearsThePermissionInteraction(t *testing.T) {
	row, ok := r7Rows(t)["serverRequest/resolved"]
	if !ok {
		t.Fatal("no row for serverRequest/resolved; without it a Codex session that the OWNER " +
			"approved at the terminal keeps showing `permission` on the phone until the whole turn " +
			"ends -- an awaiting-input badge on a session that is working")
	}
	if row["interaction"] != "none" {
		t.Errorf("serverRequest/resolved maps interaction %q, want \"none\"", row["interaction"])
	}
}

// TestR7CodexSignalSources_TheTurnRowsAreUnchanged pins the two rows that were already right,
// so the R7 additions cannot silently rewrite them.
func TestR7CodexSignalSources_TheTurnRowsAreUnchanged(t *testing.T) {
	rows := r7Rows(t)
	for event, want := range map[string]string{"turn/started": "active", "turn/completed": "idle"} {
		row, ok := rows[event]
		if !ok {
			t.Errorf("the %s row is gone", event)
			continue
		}
		if row["turn"] != want {
			t.Errorf("%s maps turn %q, want %q", event, row["turn"], want)
		}
	}
}

// TestR7CodexSignalSources_TheGridHeuristicRowSTAYS is the guard against the obvious wrong
// reading of "typed events replace the grid heuristic". They replace it as the RUNTIME
// DRIVER; the row is ADR-007's T-3 fallback and removing it would take every pre-R7 Codex
// session -- launched with no --remote and no backend at all -- from heuristic status to no
// status. ADR-013 §R7.7 says so in as many words.
func TestR7CodexSignalSources_TheGridHeuristicRowSTAYS(t *testing.T) {
	for _, s := range New().SignalSources() {
		if s.Kind == "heuristic" && s.Descriptor["grid"] == "codex" {
			return
		}
	}
	t.Fatal("the codex grid heuristic row was removed; a session launched before R7 has no backend " +
		"and no typed producer, and this row is the only status it has ever had")
}

// TestR7CodexSignalSources_EveryDeclaredEventIsARealMethod is the anti-drift fence: a row
// naming a method the server does not have is a mapping that can never fire, which is the
// exact shape the fileChange hole above already took once.
func TestR7CodexSignalSources_EveryDeclaredEventIsARealMethod(t *testing.T) {
	set := map[string]bool{}
	for _, file := range []string{"protocol-methods.txt", "r7-schema-methods.txt"} {
		data, err := os.ReadFile(filepath.Join(r7FixtureDir, file))
		if err != nil {
			t.Fatalf("read the recorded method inventory %s: %v", file, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			set[line] = true
		}
	}
	if len(set) == 0 {
		t.Fatal("the recorded method inventory is empty; this fence would pass vacuously")
	}
	for event := range r7Rows(t) {
		if !set[event] {
			t.Errorf("the adapter declares event %q, which is in neither the recorded notification "+
				"inventory nor the generated ServerRequest union; a row that names no real method "+
				"never fires", event)
		}
	}
}
