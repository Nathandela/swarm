package gate

// PB-APP-11's criterion (f), which is a GO/KOTLIN SEAM criterion and therefore cannot be
// checked on either side alone.
//
// WHY THIS FILE EXISTS AT ALL. ADR-007 B121's other finding (S-1) is that the journal repair
// channel is complete in Go and has NO PRODUCTION KOTLIN CALLER -- "the action on the
// stale/repairing screen, which does not exist". The Go tests pass over a verb no screen
// calls; the Kotlin tests pass over a screen that never asks. Neither can see the other. A
// freshness verdict that lives only in the facade would be the same defect, arriving the same
// way, one requirement later: the phone would KNOW it had not heard from the machine in an
// hour and would go on rendering "Your machine is online" from the relay's word.
//
// WHAT IT ASSERTS, and it is the requirement's own sentence rather than a spelling: the pane
// that renders relay presence CANNOT BE CONSTRUCTED without the freshness verdict. That is a
// property of the data class's required parameters, so it holds for every screen and every
// future caller -- unlike "some file mentions freshness", which one deletion undoes.
//
// IT READS CHECKED-IN SOURCE ONLY: no Android SDK, no JDK, no emulator, no handset.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	pbapp11MachinePaneFile  = "dev/swarm/phone/ui/MachineAndLaunch.kt"
	pbapp11ConnectionUIFile = "dev/swarm/phone/ui/ConnectionUi.kt"
)

// pbapp11DataClass captures one Kotlin data class's parameter list.
var pbapp11DataClass = regexp.MustCompile(`(?s)data\s+class\s+(\w+)\s*\((.*?)\n\)`)

// pbapp11Param matches a declared constructor property: "val name: Type".
var pbapp11Param = regexp.MustCompile(`(?m)^\s*val\s+(\w+)\s*:\s*([\w<>?, .]+?)\s*(?:=|,|$)`)

// pbapp11KotlinParams returns the constructor properties of one data class in src.
func pbapp11KotlinParams(t *testing.T, src, class string) map[string]string {
	t.Helper()
	for _, m := range pbapp11DataClass.FindAllStringSubmatch(src, -1) {
		if m[1] != class {
			continue
		}
		out := map[string]string{}
		for _, p := range pbapp11Param.FindAllStringSubmatch(m[2], -1) {
			out[p[1]] = strings.TrimSpace(strings.TrimSuffix(p[2], ","))
		}
		return out
	}
	t.Fatalf("PB-APP-11: no `data class %s` in the app's Kotlin; this gate is measuring nothing", class)
	return nil
}

// TestPBAPP11_TheMachinePaneCannotRenderPresenceWithoutTheFreshnessVerdict is criterion (f).
func TestPBAPP11_TheMachinePaneCannotRenderPresenceWithoutTheFreshnessVerdict(t *testing.T) {
	pane := readFileOrFail(t, filepath.Join(kotlinMainRoot(t), pbapp11MachinePaneFile), "PB-APP-11")
	params := pbapp11KotlinParams(t, pane, "MachinePane")

	if _, ok := params["presence"]; !ok {
		t.Fatalf("PB-APP-11: MachinePane no longer declares `presence`, so the pairing of the "+
			"relay's opinion with the phone's own evidence -- which is what this gate exists to "+
			"hold -- has moved somewhere this test cannot see. Found: %v", params)
	}
	freshness, ok := params["freshness"]
	if !ok {
		t.Fatalf("PB-APP-11: MachinePane renders `presence` -- the RELAY's opinion, from the party "+
			"that is withholding the machine's data -- and declares no freshness verdict beside it. "+
			"A withholding relay leaves presence reading \"online\" indefinitely while nothing has "+
			"arrived from the machine for hours (ADR-007 B121/M-1)")
	}
	if freshness != "MachineFreshness" {
		t.Errorf("PB-APP-11: MachinePane.freshness is %q; want the MachineFreshness model, so the "+
			"pane carries the machine's own stamp rather than a boolean somebody computed on the "+
			"way in", freshness)
	}
	// A default would let every future caller omit it and get "not silent" for free, which is
	// the failure this criterion names, dressed as compliance.
	if regexp.MustCompile(`val\s+freshness\s*:\s*MachineFreshness\s*=`).MatchString(pane) {
		t.Errorf("PB-APP-11: MachinePane.freshness has a DEFAULT value, so a screen can build the " +
			"pane without ever asking the facade and the verdict silently becomes whatever the " +
			"default says")
	}
}

// TestPBAPP11_TheScreenModelCarriesTheMachinesOwnStamp pins the two coordinates across the
// binding, in the direction that cannot be checked from Go: the facade exports Silent and
// LastHeardUnixMs, and the screen model must have somewhere to put BOTH. A screen that kept
// only the boolean could render "not heard from your machine" and never the "since HH:MM" the
// requirement asks for -- and the time is the actionable half.
func TestPBAPP11_TheScreenModelCarriesTheMachinesOwnStamp(t *testing.T) {
	ui := readFileOrFail(t, filepath.Join(kotlinMainRoot(t), pbapp11ConnectionUIFile), "PB-APP-11")
	params := pbapp11KotlinParams(t, ui, "MachineFreshness")

	for name, want := range map[string]string{"silent": "Boolean", "lastHeardUnixMs": "Long"} {
		got, ok := params[name]
		if !ok {
			t.Errorf("PB-APP-11: the MachineFreshness screen model has no %q; the facade exports it "+
				"and no screen can render what it never receives. Found: %v", name, params)
			continue
		}
		if got != want {
			t.Errorf("PB-APP-11: MachineFreshness.%s is %q, want %q", name, got, want)
		}
	}

	// The facade's own half, read from ITS source, so the two ends are compared rather than
	// each asserted against a transcription of the other.
	for _, want := range pbapp11FacadeFields(t) {
		if !strings.Contains(ui, want.kotlin) {
			t.Errorf("PB-APP-11: the facade exports Freshness.%s and the screen model has no %q. "+
				"The Go tests pass over a field no screen reads and the Kotlin tests pass over a "+
				"model the core never fills; this file is the only check that sees both",
				want.goName, want.kotlin)
		}
	}
}

type pbapp11Field struct{ goName, kotlin string }

// pbapp11FacadeFields reads mobile/types.go for the exported Freshness struct's fields and
// maps each to the Kotlin property the screen must hold it in.
func pbapp11FacadeFields(t *testing.T) []pbapp11Field {
	t.Helper()
	path := filepath.Join(repoRoot(t), "mobile", "types.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("PB-APP-11: parse %s: %v", mustRel(t, path), err)
	}
	var out []pbapp11Field
	for _, d := range f.Decls {
		gen, ok := d.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "Freshness" {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range st.Fields.List {
				for _, name := range field.Names {
					out = append(out, pbapp11Field{
						goName: name.Name,
						kotlin: strings.ToLower(name.Name[:1]) + name.Name[1:],
					})
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("PB-APP-11: mobile/types.go declares no exported Freshness struct, so the facade "+
			"has nothing for a screen to render and this gate would pass vacuously")
	}
	return out
}
