# S24 RED evidence — PB-DS-6, PB-DS-9, PB-DS-11

GG-5 requires the failing-first run to be evidenced. This directory holds the actual output, not a
claim that it happened.

## Files

| File | What it is |
|---|---|
| `screens-gate-red.txt` | `go test ./android/gate/ -run "TestPBDS11_\|TestPBDS6_TheKit\|TestPBDS6_TheScreen\|TestPBDS9_" -v` before any of it was fixed. 4 failing of 8. |
| `screens-model-red.txt` | `TriageInboxScreenTest`'s 23 assertions against the `TODO()` stub committed at `a8d644e`. 22 failing. |
| `screens-model-green.txt` | The same 23 assertions, same harness, against the implementation. |

## What the gate run found that was not asked for

The brief named ten PB-DS-11 violations. The scan found **eleven**: the eleventh is
`res/drawable/ic_swarm_wake.xml`, which carried the ARGB literal `#FFFFFFFF` under a comment
arguing that the platform masks a notification icon to its own tint and discards the value. That
is the argument the requirement refuses in as many words — "independent of whether its value is
currently correct" — so the fill became `@android:color/white`, a platform resource reference.
Nothing rendered changes.

The scan also has to be readable by the people it fences. Its first run after that fix reported the
file *still* dirty: the commit message and the comment explaining the replacement both quoted the
literal. `xmlMarkupOnly` strips XML comments for the same reason `kotlinCodeOnly` strips Kotlin
ones — a fence a comment can trip punishes documentation, and one a comment can satisfy is turned
off by it. `TestPBDS11_TheXMLScanSeesAttributesAndNotComments` is the control on that stripper, and
it asserts both directions: a literal in a real attribute is still found *after* stripping.

## How the Kotlin runs were produced, exactly

**Not with Gradle.** The Kotlin lane is sequenced by another agent and this slice may not invoke
`./gradlew`, so the model suite was compiled and run on a plain JVM using the
`kotlin-compiler-embeddable` and `junit` jars the Gradle cache already holds, and the JDK
`android/toolchain.env` pins. `TriageInboxScreen` and `TriageInbox` are pure Kotlin — no Android
import, no resources, no Robolectric — so the classes that ran are the classes the Gradle lane
compiles. The command is recorded at the head of each file.

The RED run uses the implementation **as committed at `a8d644e`**: every signature present, the
copy tables real, `of()` a single `TODO()`. So the 22 failures are `NotImplementedError` —
missing behaviour rather than unresolved references, which is what makes them evidence rather than
a syntax error. The one assertion that passes there is
`every group the model can carry has a heading and empty copy`, which reads only the copy tables;
that it was already green is recorded rather than hidden.

Both files are a re-run of the **final** assertions, so what is recorded is the wording the tests
ship with rather than an earlier draft's. The two assertions added after `a8d644e` — the
alphabetical scope order and the singular badge announcement — are in both runs.

## What is NOT evidenced here

`TriageInboxViewTest` is Robolectric: it needs the Android lane, and it has not been run. What has
been verified about the view without Gradle is that it **compiles** — `TriageInboxView.kt` type-
checks against the real `ui/kit` sources, `android.jar` and `androidx.core`, so every kit call
signature in it is right. Whether those calls put the right things on screen in the right order is
exactly what that suite asks, and it is unanswered.

One of its assertions is expected to fail and should not be deleted:
`an empty section renders its own copy under its own heading`. PB-DS-9 requires an empty section to
render as a section with its empty copy; the block that copy goes in is derivation table row 8
(`.empty`), and the kit does not ship it. The screen package may not build one — a visual factory
outside `ui/kit` contradicts PB-DS-6 in the same breath as claiming it, and this slice's own fence
would have to allowlist the file it lived in. The copy exists and is asserted; the component does
not.
