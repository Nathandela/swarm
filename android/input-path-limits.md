# Input-path limits on the handset

PB-SEC-12: "UI-redress and input-path defenses: overlay/tapjacking protection on gated actions
(`filterTouchesWhenObscured` or equivalent), no sensitive clipboard use, and documented limits
regarding third-party IMEs and accessibility services."
Criterion: "Tests where testable; documented where not."

This file is the "documented where not" half, plus one limit that sits beside a clause the app
does satisfy and would otherwise be over-read. Everything here is a limit the app cannot close
from inside itself; what it *can* close is asserted instead, and the assertions are named.

## What is asserted rather than documented

- **Tapjacking.** `dev.swarm.phone.SecureWindow.gate` sets `filterTouchesWhenObscured` on every
  gated control, so the framework discards a touch that arrived while another window covered
  the view. Applied at construction rather than restated per call site.
  `PhoneActivityWindowTest.every_gated_action_filters_obscured_touches` drives a real Activity
  and checks each one; `android/gate/s18_sec12_uiredress_test.go` checks the sources.
- **The system clipboard.** No production Kotlin references `ClipboardManager`,
  `setPrimaryClip`, `getPrimaryClip`, `ClipData` or `CLIPBOARD_SERVICE`, and the Go gate fences
  their absence. The app therefore never puts session content on the clipboard and never reads
  it on its own initiative.
- **Screen capture — NOT a control any more, and deliberately so.** `FLAG_SECURE` and
  `setRecentsScreenshotEnabled(false)` were removed by ADR-007 B65: the shipped app allows
  screenshots and screen recording. `android/window-security.tsv` records what that exposes,
  screen by screen, and `android/gate/s18_sec4_windowsecurity_test.go` fails if either API
  comes back.

## The paste path leaves bytes behind, and "no clipboard use" does not cover it

`App.Paste(session, text String)` is **not** a clipboard use. The user performs a paste, Android
hands the app the resulting text, and the app types it at the remote shell; the app never
touches `ClipboardManager`. So it does not fail PB-SEC-12's clipboard clause, and the clause is
satisfied as written.

It is recorded here anyway, because a reader who finds "no clipboard use: verified" and stops
will conclude the paste path leaves nothing behind. It does not:

- The text crosses the JNI boundary as a **Java `String`**. PB-BIND-4 keeps `[]byte` crossings
  to an enumerated few, and this is not one of them.
- Java `String`s are **immutable**. There is no operation that overwrites one in place, so a
  password pasted out of a manager stays in the JVM heap, in whatever copies the runtime made
  of it, until garbage collection reclaims them — and GC does not overwrite, it merely makes the
  bytes reusable.
- The `[]byte` the facade derives from that `String` is **not zeroized** after the frame is
  sealed either.

So the material is reachable by anything that can read this process's memory. PB-SEC-13 is what
narrows that set: the release build is `isDebuggable = false` and `isProfileable = false`, so
neither `adb` jdwp nor a shell-side heap dump is available on a release APK. A rooted device, or
a debug build, is outside it.

**Fixing this is not PB-SEC-12's to fix.** It would mean changing the crossing to a byte array
and zeroizing on both sides, which is a PB-BIND-4 and ADR-007 B8 decision about the bound
surface, not a hardening change inside this slice. It is recorded rather than done.

## Third-party IMEs

A keyboard on Android is an app. Whichever one the user has selected receives **every keystroke
before this app does**, including into the terminal input field, and it is free to log, sync or
transmit them. There is no API by which an app can refuse a keyboard, require a particular one,
or learn whether the current one is trustworthy: `InputMethodManager` will name the current IME,
but a name is not an attestation, and refusing to run against an unrecognised keyboard would
break the accessibility and localisation cases that make third-party IMEs exist.

The consequence has to be stated plainly, because it is easy to assume otherwise: **the
take-control lease and the biometric gate protect the CHANNEL, not the KEYBOARD.** PB-INPUT-2
guarantees that no keystroke is accepted by the machine without a confirmed lease, and PB-SEC-2
guarantees the lease was authorised by a fresh biometric. Neither says anything about what saw
the keystroke on its way from the user's finger into this app's input field.

The app's honest posture is to say so rather than to imply a protection it does not have. The
IME is user-installed and user-enabled; the user's own choice of keyboard is part of their
trusted computing base for this app.

## Accessibility services

An enabled accessibility service can **read the rendered screen** through the accessibility node
tree and **synthesise touches and gestures** through `dispatchGesture`. Both are the point of the
API and both are granted explicitly by the user in system settings, with a warning screen the
platform owns.

Two specific limits follow:

- **`FLAG_SECURE` never excluded it, and it is gone now anyway.** The flag stopped the
  compositor handing pixels to a screenshot or a recorder; it never removed the window from the
  accessibility node tree, so the terminal grid and the pairing SAS were readable by an
  accessibility service even while they were unscreenshottable. That it bought nothing here is
  part of why ADR-007 B65 withdrew it.
- **`filterTouchesWhenObscured` does not exclude it either.** That flag discards touches
  delivered while another *window* obscures the view. An injected gesture is not an obscured
  touch, so the tapjacking defence on the gated actions — take control, kill, revoke — does not
  stop an accessibility service pressing them.

There is no API that lets an app opt out of accessibility, and there should not be: doing so
would make the app unusable for the people the API exists for. `FLAG_SECURE` was the only lever
anywhere near this area, it did not reach an accessibility service either, and it is no longer
set at all (ADR-007 B65). So this limit stands on its own: nothing in the app addresses it.

## Scope

Nothing above is a claim about a physical handset. PB-E2E-5 — real biometrics, a real camera,
real FCM delivery, real Doze, hardware Keystore attestation — is deferred, and the assertions
named in this file are source-level or Robolectric-level: they establish what the app *asks*
the platform for, not what the platform then does.
