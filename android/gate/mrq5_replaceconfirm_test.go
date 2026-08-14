package gate

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-mrq5: `Replace this computer` is one tap, and
// what it destroys does not come back.
//
// WHAT THE PRESS DOES. `App.RevokeThisDevice` deregisters this handset, rotates the epoch and
// severs the gateway; `PhoneRuntime.purgeKeys` runs in a `finally` beside it and destroys BOTH key
// tiers whether or not the command reached the machine. Neither half is recoverable on this phone:
// the way back is a new pairing code shown on the computer.
//
// WHAT STANDS BEHIND IT TODAY. Nothing. `kill` -- which ends ONE session -- has asked since S24
// (`SessionDetailPanel.killConfirmation`, put in front of the user by `PhoneSurface.confirmThenPress`),
// and the more destructive control has not. Both `PhoneSurface`'s class KDoc and `SettingsSurface`'s
// `touchFilteredActions` reason at length about there being no second checkpoint behind revoke since
// ADR-007 B133 removed the biometric gate, and neither adds the one that costs nothing. The control
// also moved in the same slice from a full-width `Button` in the unrecomposed column to a `denyChip`
// in a row's trailing slot -- a smaller target for the app's most destructive action, made findable
// for the first time (agents-tracker-64rf).
//
// WHY THESE ARE GO GATES AND NOT KOTLIN TESTS, which is the same line `d0b8_unpair_test.go` draws
// over the same function and for the same reason. `PhoneRuntime.phone()` answers
// `PhoneStartup.Unavailable` on every JVM run -- the phone core is a native library cross-compiled
// for Android ABIs -- so `render()` never draws the settings panel, `drawn` is never written, and no
// press can be driven as far as a dialog, let alone to a settle. A Robolectric test asserting "the
// first press did not revoke" would pass against a surface with no confirmation at all, because the
// press it drove did nothing either way. What a test CAN see is the source: which function the chip's
// listener reaches, which function reaches the verb, and which view takes the confirming tap.
//
// THE COPY IS NOT FENCED HERE. What the question SAYS is `PairedMachineRowScreen`'s (PB-DS-9), and
// `PairedMachineRowTest` asserts it on a plain JVM where the words are readable as values rather
// than as source text.
//
// Every scan below is a function over source text with a negative control that feeds the SAME
// function a perturbed fixture, because a control that rebuilds the comparison inline proves
// something about the copy and nothing about the assertion.
//
// NOTHING HERE WALKS THE REPOSITORY ROOT: the read starts at the app module, so it cannot descend
// into `.claude/worktrees/`, which holds other agents' full checkouts.

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Reading the surface.
// ---------------------------------------------------------------------------

// mrq5Identifier is the leading identifier of an argument group -- `(confirmReplace)` -> the name,
// `(denyChip(activity, ""))` -> `denyChip`. It is how "which VIEW takes the confirming tap" is
// answered without parsing Kotlin: a call site that passes a freshly built control answers with the
// factory's name, which is never a property this surface declares, and that is exactly the fault.
func mrq5Identifier(group string) string {
	trimmed := strings.TrimFunc(group, func(r rune) bool {
		return r == '(' || r == ')' || r == ' ' || r == '\n' || r == '\t' || r == ','
	})
	end := strings.IndexFunc(trimmed, func(r rune) bool {
		return r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
	})
	if end < 0 {
		return trimmed
	}
	return trimmed[:end]
}

// mrq5CallArgs returns the balanced argument group of the first call to name, WITH its trailing
// lambda when it has one.
//
// The trailing lambda is not an optional nicety here: `setNegativeButton(cancel) { _, _ -> ... }`
// is the ordinary Kotlin spelling of a dismiss that DOES something, and it puts the action outside
// the parentheses. A scan that read only the argument list would report that call as taking no
// action at all.
func mrq5CallArgs(code, name string) (string, bool) {
	at := strings.Index(code, name+"(")
	if at < 0 {
		return "", false
	}
	args, ok := d0b8Balanced(code, at+len(name), '(', ')')
	if !ok {
		return "", false
	}
	rest := code[at+len(name)+len(args):]
	trimmed := strings.TrimLeft(rest, " \t\n")
	if strings.HasPrefix(trimmed, "{") {
		if lambda, ok := d0b8Balanced(trimmed, 0, '{', '}'); ok {
			return args + " " + lambda, true
		}
	}
	return args, true
}

// mrq5Body returns the body of `fun name(...)`, or false when the surface declares no such
// function. Unlike d0b8FunctionBody it does not fail the test itself: half the assertions below are
// about a function that does not exist yet, and "there is no confirmation" has to be reportable as
// the defect rather than as a broken fence.
func mrq5Body(code, name string) (string, bool) {
	decl := "fun " + name + "("
	at := strings.Index(code, decl)
	if at < 0 {
		return "", false
	}
	params, ok := d0b8Balanced(code, at+len(decl)-1, '(', ')')
	if !ok {
		return "", false
	}
	return d0b8Balanced(code, at+len(decl)-1+len(params), '{', '}')
}

// mrq5EnclosingFunction is the name of the function that contains the first occurrence of call.
//
// IT IS DERIVED AND NOT LISTED, so a rename cannot quietly point these fences at nothing: which
// function reaches `revokeThisDevice` is a fact about the file, and the assertions below join two
// such facts to each other rather than to a name typed here.
func mrq5EnclosingFunction(code, call string) (string, bool) {
	at := strings.Index(code, call)
	if at < 0 {
		return "", false
	}
	decl := strings.LastIndex(code[:at], "fun ")
	if decl < 0 {
		return "", false
	}
	rest := code[decl+len("fun "):]
	end := strings.IndexByte(rest, '(')
	if end < 0 {
		return "", false
	}
	return strings.TrimSpace(rest[:end]), true
}

// mrq5ChipBlock is the `Replace this computer` chip's construction block: everything the control is
// built with, including the click listener the first tap runs.
func mrq5ChipBlock(code string) (string, bool) {
	at := strings.Index(code, "val replace:")
	if at < 0 {
		return "", false
	}
	return d0b8Balanced(code, at, '{', '}')
}

// mrq5Declares reports whether ident is one of the views `touchFilteredActions` names.
func mrq5Declares(code, ident string) bool {
	at := strings.Index(code, "touchFilteredActions")
	if at < 0 {
		return false
	}
	group, ok := d0b8Balanced(code, at, '(', ')')
	if !ok {
		return false
	}
	for _, name := range strings.Split(strings.Trim(group, "()"), ",") {
		if strings.TrimSpace(name) == ident {
			return true
		}
	}
	return false
}

// mrq5GatedAtConstruction reports whether `val ident` is initialised THROUGH SecureWindow.gate.
//
// AT CONSTRUCTION is the whole of it. `filterTouchesWhenObscured` is a property of a View instance,
// so a control rebuilt on each draw, or handed to the dialog as a fresh chip, is a different view
// from the one the property was set on -- and the fence that reads a list would still be satisfied.
//
// IT FOLLOWS ONE HOP THROUGH A FACTORY, because that is the idiom this file already uses:
// `touchFilteredSwitch` is `SecureWindow.gate(SwitchCompat(...))` and both switches are declared
// through it. A fence that demanded the call be spelled at the declaration would be pinning a style
// and would push the two chips into duplicating everything the factory shares. One hop and no more:
// the question is whether THIS declaration's construction is gated, not whether the word appears
// anywhere in the file.
func mrq5GatedAtConstruction(code, ident string) bool {
	at := strings.Index(code, "val "+ident)
	if at < 0 {
		return false
	}
	rest := code[at+len("val "+ident):]
	if rest != "" && (rest[0] == '_' || rest[0] >= 'a' && rest[0] <= 'z' ||
		rest[0] >= 'A' && rest[0] <= 'Z' || rest[0] >= '0' && rest[0] <= '9') {
		return false
	}
	eq := strings.IndexByte(rest, '=')
	if eq < 0 {
		return false
	}
	initialiser := strings.TrimSpace(rest[eq+1:])
	if strings.HasPrefix(initialiser, "SecureWindow.gate(") {
		return true
	}
	factory := strings.Index(code, "fun "+mrq5Identifier("("+initialiser)+"(")
	return factory >= 0 && strings.Contains(mrq5Member(code, factory), "SecureWindow.gate(")
}

// mrq5Member is one class member, from its declaration to the start of the next one.
//
// IT EXISTS BECAUSE A GATING FACTORY HAS NO BRACES TO READ. `private fun actionChip(): TextView =
// SecureWindow.gate(...)` is an expression body, and the first `{` after its signature belongs to
// the `.apply` INSIDE the gated call -- so a body reader would return a block that does not contain
// the very call this fence is looking for, and would report a gated control as ungated. Members are
// four-space indented throughout this module, which is what bounds the read.
func mrq5Member(code string, at int) string {
	rest := code[at:]
	end := len(rest)
	for _, next := range []string{
		"\n    private fun ", "\n    internal fun ", "\n    fun ",
		"\n    private val ", "\n    internal val ", "\n    val ",
		"\n    private var ", "\n    var ", "\n    private companion ",
	} {
		if i := strings.Index(rest[1:], next); i >= 0 && i+1 < end {
			end = i + 1
		}
	}
	return rest[:end]
}

// ---------------------------------------------------------------------------
// The four facts.
// ---------------------------------------------------------------------------

// mrq5FirstPressDoesNotRevoke: the tap on the chip reaches the function that ASKS, and not the one
// that revokes.
func mrq5FirstPressDoesNotRevoke(code string) string {
	verb, ok := mrq5EnclosingFunction(code, "revokeThisDevice")
	if !ok {
		return "agents-tracker-mrq5: nothing in SettingsSurface.kt reaches revokeThisDevice, so " +
			"this fence has no subject. The control that ends the pairing is this file's, and a " +
			"fence whose subject silently left reports clean forever"
	}
	chip, ok := mrq5ChipBlock(code)
	if !ok {
		return "agents-tracker-mrq5: SettingsSurface.kt no longer builds a `replace` chip this " +
			"fence can read, so which function its first tap runs is unknown"
	}
	if strings.Contains(chip, verb+"(") {
		return "agents-tracker-mrq5: the `Replace this computer` chip calls " + verb + "() " +
			"straight from its click listener. One tap on a chip in a row's trailing slot then " +
			"deregisters this handset, rotates the epoch, severs the gateway and destroys both " +
			"key tiers -- on a screen people browse to change a notification toggle, with no way " +
			"back that does not involve walking to the computer. `kill`, which ends ONE session, " +
			"has asked since S24"
	}
	ask, ok := mrq5EnclosingFunction(code, "setMessage(")
	if !ok {
		return "agents-tracker-mrq5: SettingsSurface.kt puts no question on screen at all -- " +
			"nothing calls setMessage. The chip does not reach the revoke directly, so either the " +
			"control is dead or the question is being asked somewhere this fence cannot see it"
	}
	if !strings.Contains(chip, ask+"(") {
		return "agents-tracker-mrq5: the chip's press does not reach " + ask + "(), which is the " +
			"function that puts the question on screen. A confirmation nothing opens is a " +
			"decoration"
	}
	return ""
}

// mrq5ConfirmedPressRevokes: the confirming answer is what reaches the verb, and the question it
// answers is the row's own copy rather than a sentence typed at this call site.
func mrq5ConfirmedPressRevokes(code string) string {
	verb, ok := mrq5EnclosingFunction(code, "revokeThisDevice")
	if !ok {
		return "agents-tracker-mrq5: nothing in SettingsSurface.kt reaches revokeThisDevice"
	}
	ask, ok := mrq5EnclosingFunction(code, "setMessage(")
	if !ok {
		return "agents-tracker-mrq5: SettingsSurface.kt asks nothing before it revokes"
	}
	body, ok := mrq5Body(code, ask)
	if !ok {
		return "agents-tracker-mrq5: " + ask + "() has no body this fence can read"
	}
	if !strings.Contains(body, verb+"(") {
		return "agents-tracker-mrq5: answering the confirmation reaches " + verb + "() from " +
			"nowhere, so the question is asked and the answer goes nowhere. A confirmation that " +
			"cannot perform the action it confirms is worse than none: the user believes the " +
			"pairing is gone and it is not"
	}
	args, ok := mrq5CallArgs(code, "setMessage")
	if !ok {
		return "agents-tracker-mrq5: setMessage takes no argument group this fence can read"
	}
	if !strings.Contains(args, "replaceConfirmation") {
		return "agents-tracker-mrq5: the question put on screen is " + strings.TrimSpace(args) +
			", not the row's `replaceConfirmation`. PB-DS-9 assigns copy to the screen model, and " +
			"the row already carries the machine's name and what replacing costs -- a second " +
			"sentence written here is the drift that ends with the confirmation naming the wrong " +
			"machine"
	}
	if strings.Contains(args, `"`) {
		return "agents-tracker-mrq5: the question carries a string literal typed in the surface " +
			"(" + strings.TrimSpace(args) + "). The words a user reads before an irreversible " +
			"action belong to one file, and it is not this one"
	}
	return ""
}

// mrq5ConfirmingViewIsTouchFiltered: PB-SEC-12 clause 1 reaches the view that takes the CONFIRMING
// tap.
//
// THIS IS THE POINT AT WHICH A CONFIRMATION BECOMES A DECORATION. `PhoneSurface.confirmThenPress`
// records the limit in its own KDoc: the tap that OPENS a platform dialog is filtered, and the
// dialog's own buttons live in a window that surface does not own, so nothing filters them. An
// overlay attack against revoke does not care which of two taps it steals. The answer is that the
// confirming control is a view THIS surface builds, gates once, and names in `touchFilteredActions`
// -- the same three reasons the chip and the switches are built at construction rather than per draw.
func mrq5ConfirmingViewIsTouchFiltered(code string) string {
	args, ok := mrq5CallArgs(code, "setView")
	if !ok {
		return "agents-tracker-mrq5: the confirmation hands the dialog no view of its own, so the " +
			"confirming tap lands on a platform button in a window this surface does not own. " +
			"filterTouchesWhenObscured is a property of a View, and PB-SEC-12 clause 1 is the only " +
			"defence left standing on revoke since ADR-007 B133"
	}
	ident := mrq5Identifier(args)
	if !mrq5Declares(code, ident) {
		return "agents-tracker-mrq5: the confirming tap lands on `" + ident + "`, which " +
			"touchFilteredActions does not name. Either it is built somewhere else, or it is " +
			"built fresh for the dialog -- and a control the list does not name is a control " +
			"nothing asserts the overlay filter over"
	}
	if !mrq5GatedAtConstruction(code, ident) {
		return "agents-tracker-mrq5: `" + ident + "` is not initialised through SecureWindow.gate. " +
			"The filter is a property of the instance, so a control that acquires it later -- or " +
			"not at all -- is in the list and undefended"
	}
	return ""
}

// mrq5DismissRevokesNothing: the way out of the dialog performs nothing.
func mrq5DismissRevokesNothing(code string) string {
	verb, ok := mrq5EnclosingFunction(code, "revokeThisDevice")
	if !ok {
		return "agents-tracker-mrq5: nothing in SettingsSurface.kt reaches revokeThisDevice"
	}
	args, ok := mrq5CallArgs(code, "setNegativeButton")
	if !ok {
		return "agents-tracker-mrq5: the confirmation offers no way out. A dialog whose only " +
			"control performs the destructive action is a confirmation that cannot be answered no"
	}
	if strings.Contains(args, verb+"(") || strings.Contains(args, "dispatch") {
		return "agents-tracker-mrq5: dismissing the confirmation reaches " + strings.TrimSpace(args) +
			". The negative answer must leave the pairing exactly as it was"
	}
	if !strings.Contains(args, "null") {
		return "agents-tracker-mrq5: the negative answer is wired to " + strings.TrimSpace(args) +
			" rather than to null. Nothing has to happen when a user declines to destroy their " +
			"pairing, and a listener there is a place for something to start happening"
	}
	return ""
}

// ---------------------------------------------------------------------------
// The fixture the negative controls perturb.
//
// It is the SHAPE this fence requires and not the shipped file: a control built once and gated, a
// confirmation that carries the row's question and the confirming view, and a revoke reached only
// from the answer. Each control below changes exactly one of those and asserts the corresponding
// scan reports it.
// ---------------------------------------------------------------------------

const mrq5Fixture = `
class SettingsSurface {
    private val replace: TextView = actionChip().apply {
        setOnClickListener { confirmThenReplace() }
    }

    internal val confirmReplace: TextView = actionChip()

    val touchFilteredActions: List<View> = listOf(needsInput, finished, replace, confirmReplace)

    private fun actionChip(): TextView = SecureWindow.gate(denyChip(activity, ""))

    private fun confirmThenReplace() {
        val row = drawn?.machineSection?.row ?: return
        val asked = AlertDialog.Builder(activity)
            .setMessage(row.replaceConfirmation)
            .setView(confirmReplace)
            .setNegativeButton(android.R.string.cancel, null)
            .show()
        confirmReplace.setOnClickListener {
            asked.dismiss()
            onReplace(replace)
        }
    }

    private fun onReplace(control: View) {
        dispatch.press(control, SendPlane.COMMAND, work = { app.revokeThisDevice() })
    }
}
`

// mrq5Perturbed returns the fixture with one thing wrong.
func mrq5Perturbed(t *testing.T, old, new string) string {
	t.Helper()
	if !strings.Contains(mrq5Fixture, old) {
		t.Fatalf("agents-tracker-mrq5: the negative control's fixture no longer contains %q, so "+
			"the perturbation is a no-op and the control asserts nothing", old)
	}
	return strings.Replace(mrq5Fixture, old, new, 1)
}

// ---------------------------------------------------------------------------
// The assertions.
// ---------------------------------------------------------------------------

func TestMRQ5_TheFirstPressOnReplaceDoesNotRevoke(t *testing.T) {
	if fault := mrq5FirstPressDoesNotRevoke(d0b8Code(t, d0b8SettingsSurface)); fault != "" {
		t.Error(fault)
	}
	if mrq5FirstPressDoesNotRevoke(mrq5Fixture) != "" {
		t.Errorf("the scan reports a fault against a surface that asks first: %s",
			mrq5FirstPressDoesNotRevoke(mrq5Fixture))
	}
	direct := mrq5Perturbed(t, "setOnClickListener { confirmThenReplace() }",
		"setOnClickListener { onReplace(replace) }")
	if mrq5FirstPressDoesNotRevoke(direct) == "" {
		t.Error("the scan passed a chip whose click listener calls the revoke directly, which is " +
			"the whole defect: it cannot tell a confirmed press from an unconfirmed one")
	}
}

func TestMRQ5_TheConfirmingPressIsWhatRevokes(t *testing.T) {
	if fault := mrq5ConfirmedPressRevokes(d0b8Code(t, d0b8SettingsSurface)); fault != "" {
		t.Error(fault)
	}
	if mrq5ConfirmedPressRevokes(mrq5Fixture) != "" {
		t.Errorf("the scan reports a fault against a surface whose confirmation performs the "+
			"revoke: %s", mrq5ConfirmedPressRevokes(mrq5Fixture))
	}
	inert := mrq5Perturbed(t, "            onReplace(replace)\n", "")
	if mrq5ConfirmedPressRevokes(inert) == "" {
		t.Error("the scan passed a confirmation whose positive answer performs nothing, which " +
			"leaves the user believing the pairing ended when it did not")
	}
	invented := mrq5Perturbed(t, "row.replaceConfirmation", `"Are you sure?"`)
	if mrq5ConfirmedPressRevokes(invented) == "" {
		t.Error("the scan passed a question typed at the call site rather than the row's own " +
			"copy, so the sentence that names the machine could drift out of the app entirely")
	}
}

func TestMRQ5_TheControlThatTakesTheConfirmingTapCarriesTheOverlayFilter(t *testing.T) {
	if fault := mrq5ConfirmingViewIsTouchFiltered(d0b8Code(t, d0b8SettingsSurface)); fault != "" {
		t.Error(fault)
	}
	if mrq5ConfirmingViewIsTouchFiltered(mrq5Fixture) != "" {
		t.Errorf("the scan reports a fault against a confirming control this surface builds, "+
			"gates and declares: %s", mrq5ConfirmingViewIsTouchFiltered(mrq5Fixture))
	}
	fresh := mrq5Perturbed(t, ".setView(confirmReplace)", `.setView(denyChip(activity, ""))`)
	if mrq5ConfirmingViewIsTouchFiltered(fresh) == "" {
		t.Error("the scan passed a confirming control built fresh for the dialog, which is a " +
			"view no gate was ever applied to")
	}
	undeclared := mrq5Perturbed(t, "listOf(needsInput, finished, replace, confirmReplace)",
		"listOf(needsInput, finished, replace)")
	if mrq5ConfirmingViewIsTouchFiltered(undeclared) == "" {
		t.Error("the scan passed a confirming control that touchFilteredActions does not name, " +
			"so nothing in the Kotlin suite asserts the filter over the view that takes the tap")
	}
	ungated := mrq5Perturbed(t, "actionChip(): TextView = SecureWindow.gate(denyChip(activity, \"\"))",
		"actionChip(): TextView = denyChip(activity, \"\")")
	if mrq5ConfirmingViewIsTouchFiltered(ungated) == "" {
		t.Error("the scan passed a confirming control that is declared but never gated, which is " +
			"the list being true and the defence being absent -- and it is the FACTORY that stopped " +
			"gating, which is the hop this scan has to follow to see anything at all")
	}
}

func TestMRQ5_DismissingTheConfirmationLeavesThePairingIntact(t *testing.T) {
	if fault := mrq5DismissRevokesNothing(d0b8Code(t, d0b8SettingsSurface)); fault != "" {
		t.Error(fault)
	}
	if mrq5DismissRevokesNothing(mrq5Fixture) != "" {
		t.Errorf("the scan reports a fault against a dialog whose negative answer does nothing: %s",
			mrq5DismissRevokesNothing(mrq5Fixture))
	}
	destructive := mrq5Perturbed(t, ".setNegativeButton(android.R.string.cancel, null)",
		".setNegativeButton(android.R.string.cancel) { _, _ -> onReplace(control) }")
	if mrq5DismissRevokesNothing(destructive) == "" {
		t.Error("the scan passed a dialog that revokes when the user declines")
	}
}
