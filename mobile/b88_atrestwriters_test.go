package swarmmobile

// ADR-007 B88 / residual 4.23 -- THE ENUMERATION THE BYTE SCAN CANNOT BE.
//
// PB-SEC-1 and PB-STATE-6 are properties of a CHANNEL: what a stolen handset yields from the
// phone's storage. The census that measures them (internal/phonecore/s15_statetier_test.go,
// via s14aStateDirBytes) walks EVERY regular file under the state dir -- its read side is
// properly channel-quantified, and its own comment reasons about the sibling-file evasion. But
// a byte scan can only see files that EXIST when it runs, and what makes them exist is the
// fixture: one `phonecore.Core`. So the quantifier survives on the read side and is dropped on
// the write side, and two production writers escaped it:
//
//	mobile/pairing.go        writes "pairing-attempt" into the state dir -- another PACKAGE, so
//	                         the census cannot construct it and the file is simply absent
//	PhoneRuntime.kt          writes "relay-url" into the state dir's PARENT -- outside the scan
//	                         root entirely, so no fixture could help
//
// Neither is exploitable (a label from a closed set; a relay URL that travels in the QR and is
// public by design), which is why no row moved. The exposure is the NEXT writer: it would get
// no census row -- the reflective completeness check is over State's FIELDS, so a non-State
// file is never demanded -- and no byte scan, exactly as these two did.
//
// THIS FENCE IS STATIC ON PURPOSE, and that is the whole design. A runtime walk of the
// directory would repeat the defect: it would see only the writers the test drove. Scanning
// SOURCE catches a fifth writer on the day it lands whether or not any test exercises it --
// the same move as TestS10_NoTestFixtureStampsANonzeroRosterCursor and
// TestS14A_NoCallSiteDiscardsACustodyError, which is to enforce a rule where it is NOT already
// obeyed.
//
// WHAT THIS DOES NOT COVER, stated because the two mechanisms have different reach and a
// reader must not take one for the other:
//
//   - IT PINS NAMES, NOT CONTENTS. A future writer could take a permitted entry's file and put
//     key material in it, and this fence would stay green. Contents are the byte scan's job --
//     and the byte scan reaches only the writers its fixture constructs, which is the gap this
//     file exists to report rather than to close. Closing it needs the escaping writers driven
//     INTO the census, not a longer list here.
//   - IT IS FILE-GRANULAR, NOT CALL-GRANULAR, on the precedent TestS14A_TheCleartextSealerHas-
//     NoCallSitesLeft set and for its stated reason: a second write inside a file already on
//     the list is not new exposure, a new FILE is, and a count-of-expressions fence fires
//     spuriously on a reformat.
//   - IT DOES NOT PROVE A LISTED WRITER IS SAFE. The reason strings record what each one is for;
//     they are not evidence about its at-rest form.
//
// WHY A LIST OF FILENAMES IS WORTH ITS MAINTENANCE COST, since a list is a thing that rots. It
// rots LOUDLY. A stale entry fails this test by name (every entry must be observed), and a new
// writer fails it by name too. The rot IS the signal -- unlike the fixture drift that produced
// residual 4.23, where the fence went quiet instead of red. A list that fails closed when it is
// out of date is maintenance; a list that fails open is the defect.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// atRestWriteCall matches a source construct that creates or writes a file. Deliberately
// broad: a fence that matched only the idiom in use today would miss the next one.
var atRestWriteCall = regexp.MustCompile(
	`os\.WriteFile|os\.Create\(|os\.OpenFile|os\.CreateTemp|writeFileAtomic\(|writeSecretFile\(` +
		`|\.writeText\(|\.writeBytes\(|FileOutputStream|\.outputStream\(\)`)

// atRestWriterRoots are the trees that own the phone's at-rest storage. internal/remote/crypto
// is included though it is FROZEN and its only live writer is machine-side: excluding a tree
// because it is not supposed to change is how a scope hole is argued into existence.
var atRestWriterRoots = []string{
	filepath.Join("internal", "phonecore"),
	filepath.Join("internal", "remote", "crypto"),
	"mobile",
	filepath.Join("android", "app", "src", "main"),
}

// permittedAtRestWriters is the enumeration: every source file that may write a file on the
// phone's at-rest path, and WHY. A name with no reason is how this list rots into a place
// people add entries to quiet a failure -- the failure mode the unbound-verb ledger was built
// against and that this project has hit before.
//
// ADDING AN ENTRY IS A DECISION, not a formality. A new writer into the phone's storage needs
// its at-rest form settled (sealed, or in PB-STATE-9's pinned cleartext list, or justified as
// carrying nothing sensitive) BEFORE it is listed here, because nothing else will ask.
var permittedAtRestWriters = map[string]string{
	filepath.Join("internal", "phonecore", "state.go"): "phone-state.json: the durable State blob -- " +
		"three sealed containers plus PB-STATE-9's pinned cleartext list. The writer the S15 census drives.",
	filepath.Join("internal", "phonecore", "keycustody.go"): "device.key: the sealed device-key container -- " +
		"public keys in the clear, Content/Wake blobs sealed under their own KEKs. Constructed by the census via Resume.",
	filepath.Join("mobile", "pairing.go"): "pairing-attempt: PB-PAIR-4's durable pairing state, one label from a " +
		"closed set (paired, sas_mismatch, different_machine, ...). CLEARTEXT and outside the census -- carries no key " +
		"material and no session content, so PB-SEC-1 is not engaged (ADR-007 B88).",
	filepath.Join("internal", "remote", "crypto", "identity.go"): "the MACHINE's identity file. Machine-side writer " +
		"in a package the phone links; never written on a handset.",
	filepath.Join("internal", "remote", "crypto", "keystore.go"): "the PRE-SEAM raw device.key. NewFileKeyStore has " +
		"zero production callers -- phonecore/keycustody.go:121 records that it is reached from tests only, and the " +
		"phone opens the sealed container instead.",
	filepath.Join("internal", "remote", "crypto", "secretfile.go"): "writeSecretFile, the shared 0600 temp+rename " +
		"helper the two crypto writers above call. Not itself a distinct file.",
	filepath.Join("android", "app", "src", "main", "kotlin", "dev", "swarm", "phone", "PhoneRuntime.kt"): "relay-url: " +
		"the remembered relay endpoint, written to the state dir's PARENT (noBackupFilesDir). CLEARTEXT and outside the " +
		"census's scan root -- the endpoint travels in the pairing QR and is public by design (ADR-007 B88).",
	filepath.Join("android", "app", "src", "main", "kotlin", "dev", "swarm", "phone", "keys", "CustodyBacking.kt"): "the " +
		"Android sealed-store backing: staged sealed records for the two key tiers.",
}

func b88RepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatalf("locate the module root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// TestB88_EveryAtRestWriterIsEnumerated is the fence. A fifth writer fails it BY NAME.
func TestB88_EveryAtRestWriterIsEnumerated(t *testing.T) {
	root := b88RepoRoot(t)

	found := map[string]bool{}
	for _, sub := range atRestWriterRoots {
		base := filepath.Join(root, sub)
		if _, err := os.Stat(base); err != nil {
			t.Fatalf("scan root %s is missing (%v); the fence would silently cover less than it claims", sub, err)
		}
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			if ext := filepath.Ext(path); ext != ".go" && ext != ".kt" {
				return nil
			}
			body, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			if atRestWriteCall.Match(body) {
				rel, _ := filepath.Rel(root, path)
				found[rel] = true
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", sub, err)
		}
	}

	// ANTI-VACUITY, first: a broken pattern or a moved tree must fail rather than pass over
	// nothing. This project has shipped a fence that guarded nothing while exiting 0.
	if len(found) == 0 {
		t.Fatal("the scan found NO at-rest write sites at all; the pattern or the roots are wrong " +
			"and this fence is guarding nothing")
	}

	var unlisted []string
	for rel := range found {
		if _, ok := permittedAtRestWriters[rel]; !ok {
			unlisted = append(unlisted, rel)
		}
	}
	sort.Strings(unlisted)
	if len(unlisted) > 0 {
		t.Errorf("%d source file(s) write on the phone's at-rest path and are NOT enumerated:\n  %s\n\n"+
			"Every file the phone writes at rest is part of what a stolen handset yields (PB-SEC-1, "+
			"PB-STATE-6). The S15 byte census cannot see a writer its fixture does not construct -- "+
			"mobile/pairing.go and PhoneRuntime.kt both escaped it that way (ADR-007 B88, residual 4.23). "+
			"Settle this writer's AT-REST FORM first -- sealed, or in PB-STATE-9's pinned cleartext list, "+
			"or carrying nothing sensitive -- then add it to permittedAtRestWriters WITH THAT REASON. "+
			"Adding the name alone silences the only thing that asks.",
			len(unlisted), strings.Join(unlisted, "\n  "))
	}

	// The other direction: a listed writer that no longer writes anything. A stale entry is how
	// the list stops describing the tree, and an entry nobody can see is an entry nobody rechecks.
	var vanished []string
	for rel := range permittedAtRestWriters {
		if !found[rel] {
			vanished = append(vanished, rel)
		}
	}
	sort.Strings(vanished)
	if len(vanished) > 0 {
		t.Errorf("%d enumerated writer(s) no longer write at rest:\n  %s\n\n"+
			"Remove them. A list that keeps names the tree has dropped is a list nobody trusts, and the "+
			"next reader cannot tell which entries are still load-bearing.",
			len(vanished), strings.Join(vanished, "\n  "))
	}
}

// TestB88_EveryEnumeratedWriterStatesItsReason keeps the list from decaying into bare names --
// which is the specific way this kind of ledger rots.
func TestB88_EveryEnumeratedWriterStatesItsReason(t *testing.T) {
	for file, reason := range permittedAtRestWriters {
		if len(strings.TrimSpace(reason)) < 40 {
			t.Errorf("permittedAtRestWriters[%q] has no usable reason (%q). Each entry records WHAT the "+
				"file writes and why its at-rest form is acceptable; without that the next reader adds a "+
				"name to quiet a failure and the enumeration stops meaning anything.", file, reason)
		}
	}
}
