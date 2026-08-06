package gate

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-r3os: "the purge failure the Go side reports
// has nowhere on this handset to go."
//
// WHAT THE CONTRACT SAYS. `mobile.App.PurgeKeys` states it in its own words: "An error means the
// material AT REST survived (a full disk, a read-only data directory). The memory half has
// happened regardless." Both Go layers keep that promise and both are already fenced --
// `internal/phonecore`'s TestS14A_R3_APurgeClearsMemoryEvenWhenTheDurableWriteFails and
// `mobile/conformance`'s robustness suite drive a read-only state directory and assert that the
// error comes back while the live keys and the decrypted caches go.
//
// WHAT THE HANDSET DOES WITH IT. `PhoneRuntime.purgeKeys` catches every exception and discards
// it, so the fact dies one frame above the facade: the revoke settle draws an unpaired phone,
// and the sealed containers are still in the app's data directory. The caller cannot honour a
// contract whose failure it is never handed -- which is why this fence is about the RUNTIME's
// signature rather than about any screen. What the screen then says is SettingsSurface's half.
//
// A RETHROW IS NOT THE FIX AND IS FENCED AS ITS OWN FAULT. The one call site is inside
// `SettingsSurface.onReplace`'s `finally`, which exists so that both key tiers go whether or not
// the revoke reached the machine; an exception thrown from a `finally` REPLACES the exception the
// block was already carrying, so a purge that threw would hand the user the housekeeping failure
// in place of the panic action's own answer. The failure has to come back as a VALUE.
//
// WHY THIS IS A GO GATE AND NOT A KOTLIN TEST, in d0b8_unpair_test.go's words for the same file:
// `PhoneRuntime.phone()` answers `PhoneStartup.Unavailable` on every JVM run -- the phone core is
// a gomobile AAR of .so files cross-compiled for Android ABIs -- so `ready` is null under
// Robolectric and `purgeKeys` returns before it reaches the facade at all. No unit test can drive
// a purge, let alone one whose durable write fails.
//
// NOTHING HERE WALKS THE REPOSITORY ROOT. The scan is one named file.

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const r3osRuntimeFile = "dev/swarm/phone/PhoneRuntime.kt"

// r3osPurgeDecl matches the declaration and captures whatever stands between the parameter list
// and the body, which is where a return type would be.
var r3osPurgeDecl = regexp.MustCompile(`fun\s+purgeKeys\s*\([^)]*\)([^{]*)\{`)

// r3osCarriesTheFailure is the type a routed failure comes back as. It is nullable because
// "nothing went wrong" is the ordinary answer -- the shape `unlockContent` already uses.
var r3osCarriesTheFailure = regexp.MustCompile(`:\s*RoutedError\?`)

// r3osRoutes is the runtime's own verb for turning a facade throw into words a screen can show.
var r3osRoutes = regexp.MustCompile(`\brouteStartupFailure\s*\(|\bRoutedError\s*\(`)

// r3osRethrows is a transfer of the exception back out of the arm. See the file comment for why
// that is a fault here and not a fix.
var r3osRethrows = regexp.MustCompile(`\bthrow\b`)

func r3osSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join(kotlinMainRoot(t), filepath.FromSlash(r3osRuntimeFile))
	return kotlinWithoutStringLiterals(kotlinCodeOnly(readFileOrFail(t, path, "agents-tracker-r3os")))
}

// r3osFaults reports every way purgeKeys can leave the durable failure unsayable.
//
// THE TWO CHECKS ARE SEPARATE BECAUSE THE TWO FAILURES ARE. A verb that answers Unit cannot carry
// the fact however well its arm routes it; an arm that swallows leaves a verb whose return type
// promises a report it never makes.
//
// @param code the source, comments and string literals already stripped.
func r3osFaults(where, code string) []string {
	decl := r3osPurgeDecl.FindStringSubmatch(code)
	if decl == nil {
		return []string{where + ": nothing here declares `purgeKeys`, so this fence has no subject. " +
			"If the verb moved, re-point the gate at it rather than deleting it"}
	}
	var faults []string
	if !r3osCarriesTheFailure.MatchString(decl[1]) {
		answers := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(decl[1]), ":"))
		if answers == "" {
			answers = "Unit"
		}
		faults = append(faults, where+": `purgeKeys` answers `"+answers+"`, so the "+
			"durable failure has nowhere to go. `App.PurgeKeys` returns an error to say the key "+
			"material AT REST survived the purge, and a caller that is handed nothing draws an "+
			"unpaired phone over sealed containers still on disk")
	}
	body, ok := kotlinFunBody(code, "purgeKeys")
	if !ok {
		return append(faults, where+": `purgeKeys` has no body this fence can read")
	}
	for _, at := range o6utCatch.FindAllStringIndex(body, -1) {
		arm, ok := o6utBlockAfter(body, at[1])
		if !ok {
			continue
		}
		switch {
		case r3osRethrows.MatchString(arm):
			faults = append(faults, where+": `purgeKeys` throws its failure back out. The one call "+
				"site is inside `SettingsSurface.onReplace`'s `finally`, where a throw REPLACES the "+
				"revoke's own answer -- the user is told about the disk instead of about the machine")
		case !r3osRoutes.MatchString(arm):
			faults = append(faults, where+": `purgeKeys` catches the facade failure and discards it. "+
				"The memory half is gone either way; what the caller is never told is that the "+
				"sealed containers at rest survived, on the one handset whose owner has just "+
				"disowned it")
		}
	}
	return faults
}

// TestR3OS_TheRuntimeHandsBackThePurgeFailureItIsGiven is the fence.
func TestR3OS_TheRuntimeHandsBackThePurgeFailureItIsGiven(t *testing.T) {
	if faults := r3osFaults(r3osRuntimeFile, r3osSource(t)); len(faults) > 0 {
		t.Errorf("agents-tracker-r3os: the phone reports a purge that did not finish as one that "+
			"did:\n  %s\n\nPB-KEY-7's memory half cannot fail and has already happened. The half "+
			"that CAN fail is the one the owner of a revoked handset most needs told.",
			strings.Join(faults, "\n  "))
	}
}

// TestR3OS_ThePurgeScanDiscriminates is the control, in both directions.
//
// `shipped` is `purgeKeys` as it stood at the commit this test was written on, comment stripped.
func TestR3OS_ThePurgeScanDiscriminates(t *testing.T) {
	const shipped = `class PhoneRuntime {
    @Synchronized
    fun purgeKeys() {
        val live = ready ?: return
        try {
            live.app.purgeKeys()
        } catch (refused: Exception) {
        }
    }
}`
	if faults := r3osFaults("shipped.kt", shipped); len(faults) != 2 {
		t.Fatalf("the scan finds %d faults in a verb that answers Unit AND swallows the throw, so "+
			"every clean run of the assertion above is about nothing:\n%s",
			len(faults), strings.Join(faults, "\n"))
	}

	// Half a fix, in each direction. Both are states this verb could plausibly be left in.
	const routedButUnit = `class PhoneRuntime {
    fun purgeKeys() {
        val live = ready ?: return
        try {
            live.app.purgeKeys()
        } catch (refused: Exception) {
            routeStartupFailure(refused)
        }
    }
}`
	if faults := r3osFaults("routedbutunit.kt", routedButUnit); len(faults) != 1 {
		t.Errorf("the scan does not report a failure that is routed into a sentence and then "+
			"dropped on the floor, which is the same silence one step later:\n%s",
			strings.Join(faults, "\n"))
	}

	const rethrown = `class PhoneRuntime {
    fun purgeKeys(): RoutedError? {
        val live = ready ?: return null
        return try {
            live.app.purgeKeys()
            null
        } catch (refused: Exception) {
            throw refused
        }
    }
}`
	if faults := r3osFaults("rethrown.kt", rethrown); len(faults) != 1 {
		t.Errorf("the scan passes a purge that throws out of the `finally` its only call site puts "+
			"it in, where it replaces the revoke's own answer:\n%s", strings.Join(faults, "\n"))
	}

	// What the fix produces: the verb says what happened and the caller decides what to do with it.
	const fixed = `class PhoneRuntime {
    @Synchronized
    fun purgeKeys(): RoutedError? {
        val live = ready ?: return null
        return try {
            live.app.purgeKeys()
            null
        } catch (refused: Exception) {
            routeStartupFailure(refused)
        }
    }
}`
	if faults := r3osFaults("fixed.kt", fixed); len(faults) > 0 {
		t.Errorf("the scan rejects a verb that hands its failure back, which is a fence nobody can "+
			"satisfy:\n%s", strings.Join(faults, "\n"))
	}

	// AND IT SPEAKS ONLY ABOUT ITS SUBJECT. `PhoneRuntime` is full of verbs that catch, and the
	// neighbour below is the closest one -- same file, same shape, different question.
	const neighbour = `class PhoneRuntime {
    fun purgeKeys(): RoutedError? {
        val live = ready ?: return null
        return try {
            live.app.purgeKeys()
            null
        } catch (refused: Exception) {
            routeStartupFailure(refused)
        }
    }

    fun somethingElse() {
        try {
            live.app.other()
        } catch (ignored: Exception) {
        }
    }
}`
	if faults := r3osFaults("neighbour.kt", neighbour); len(faults) > 0 {
		t.Errorf("the scan claims a catch in a different function, so its message would name a verb "+
			"that is fine:\n%s", strings.Join(faults, "\n"))
	}
}
