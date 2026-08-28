package gate

// W8.1 of the phone refit (docs/specifications/phone-refit-playbook.md section 8b, bead
// agents-tracker-a84l): a style's leading reaches the view.
//
// THE DEFECT. `android:lineHeight` is a TextView attribute and not a TextAppearance one, so
// `setTextAppearance(style)` on the kit's framework TextView applies everything a style states
// except its leading. Five styles in type.xml state one (Body.Message 20.3sp, Body.Secondary
// 19.6sp, Mono.Code 18.75sp, Mono.CodeSmall 19.375sp, Mono.Fine 17.6sp) and none of them rendered
// it: the W4 evidence measured a styled notice at the same 16 px as a bare view.
// DesignScaleResolutionTest reads the XML text, so the Kotlin suite stayed green over a leading
// nothing drew. Kit.appearance reads the one attribute the platform drops and spends it through
// TextViewCompat.setLineHeight; LeadingTest.kt pins that behaviour on a view.
//
// WHAT THIS GATE PINS IS THE ROUTING. The helper corrects nothing at a site that does not call it,
// and a leading half the kit applies is worse than one none of it does -- Kit.textView's reason
// for being a constructor rather than a call at each site. So the only setTextAppearance( in the
// kit package is the one inside Kit.appearance; every other site calls the helper.
//
// The fence reads code only (comments and strings stripped, kotlinCodeOnly), so a KDoc that names
// the call it forbids -- the helper's own does -- is not a fault, and a call hidden in a comment is
// not a pass.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const w8HelperFile = "Kit.kt"
const w8HelperSignature = "fun appearance(view: TextView, @StyleRes style: Int)"
const w8HelperCall = "view.setTextAppearance(style)"

var w8BareCall = regexp.MustCompile(`\bsetTextAppearance\s*\(`)

// w8BareSites returns "file:line: text" for every setTextAppearance( in the kit sources (basename
// -> raw source, s23KitSources's shape) other than the single call inside Kit.appearance.
func w8BareSites(sources map[string]string) []string {
	var out []string
	for _, name := range s23SortedKeys(sources) {
		for i, line := range strings.Split(kotlinCodeOnly(sources[name]), "\n") {
			if !w8BareCall.MatchString(line) {
				continue
			}
			if name == w8HelperFile && strings.Contains(line, w8HelperCall) {
				continue
			}
			out = append(out, fmt.Sprintf("%s:%d: %s", name, i+1, strings.TrimSpace(line)))
		}
	}
	return out
}

func TestW8_EveryKitStyleIsAppliedThroughKitAppearance(t *testing.T) {
	sources := s23KitSources(t)
	helper := kotlinCodeOnly(sources[w8HelperFile])
	if !strings.Contains(helper, w8HelperSignature) {
		t.Errorf("W8.1: %s declares no `%s`: the kit has no way of putting a style on a view "+
			"that carries the style's leading, so type.xml's five android:lineHeight items reach "+
			"nothing", w8HelperFile, w8HelperSignature)
	}
	if n := strings.Count(helper, w8HelperCall); n != 1 {
		t.Errorf("W8.1: %s calls `%s` %d times; the platform path belongs in Kit.appearance "+
			"exactly once", w8HelperFile, w8HelperCall, n)
	}
	if faults := w8BareSites(sources); len(faults) > 0 {
		t.Errorf("W8.1: %d bare setTextAppearance( call(s) in ui/kit outside Kit.appearance. "+
			"Each applies the style's size, weight, family and tracking and drops its leading; "+
			"route it through Kit.appearance(this, style):\n\t%s",
			len(faults), strings.Join(faults, "\n\t"))
	}
}

// The control: a scratch kit on disk with the helper as shipped and one component that still
// calls the platform directly. The fence must name that one site, and only that one.
func TestW8_TheLeadingFenceCanActuallyFail(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(w8HelperFile, "internal object Kit {\n"+
		"    /** Reads the leading setTextAppearance(style) drops. */\n"+
		"    fun appearance(view: TextView, @StyleRes style: Int) {\n"+
		"        view.setTextAppearance(style)\n"+
		"    }\n"+
		"}\n")
	write("Badge.kt", "fun badge(context: Context) = Kit.textView(context).apply {\n"+
		"    setTextAppearance(R.style.TextAppearance_Swarm_Mono_Agent)\n"+
		"}\n")
	write("Notice.kt", "// setTextAppearance(style) is what this site used to call.\n"+
		"fun notice(context: Context) = Kit.textView(context).apply {\n"+
		"    Kit.appearance(this, R.style.TextAppearance_Swarm_Body_Secondary)\n"+
		"}\n")
	read := func() map[string]string {
		sources := map[string]string{}
		for _, path := range kotlinFiles(t, dir) {
			sources[filepath.Base(path)] = readFileOrFail(t, path, "W8.1")
		}
		return sources
	}
	faults := w8BareSites(read())
	if len(faults) != 1 || !strings.HasPrefix(faults[0], "Badge.kt:2:") {
		t.Fatalf("the fence reports %d fault(s) for a scratch kit with exactly one bare call at "+
			"Badge.kt:2 (the helper's own call and two comments naming the call are not faults):\n\t%s",
			len(faults), strings.Join(faults, "\n\t"))
	}
	// The exemption covers the helper's one call, not its file: a second bare call in Kit.kt is a
	// fault like any other.
	write("Stray.kt", "fun stray(view: TextView) { view.setTextAppearance(0) }\n")
	write(w8HelperFile, read()[w8HelperFile]+"\nfun strayToo(view: TextView) { view.setTextAppearance(0) }\n")
	if faults := w8BareSites(read()); len(faults) != 3 {
		t.Fatalf("a bare call in %s outside the helper, or one on an explicit receiver, is not "+
			"reported (%d fault(s), 3 expected):\n\t%s", w8HelperFile, len(faults), strings.Join(faults, "\n\t"))
	}
}
