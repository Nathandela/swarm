# agents-tracker-qx9m -- failing-first evidence (GG-5)

**QR pairing is unreachable on a fresh install: the scan button requires the permission it exists
to request.** P0, from the owner's handset on the internal-testing build (versionCode 2),
2026-08-03: *"i don't see any open camera for qr code scanning button whatsoever. only enter code"*.

## The defect

Three facts closed a loop, and each one is individually correct:

1. `runtime/PermissionGate.kt:64` -- `PermissionStateResolver` answers `!hasAskedBefore -> DENIED`.
   A permission **nobody has asked for** therefore resolves to `DENIED`. This row is deliberate:
   `shouldShowRequestPermissionRationale` is false before the first ask as well as after a permanent
   one, so without the persisted bit a fresh install is indistinguishable from a permanent refusal.
2. `ui/screens/PairingPanel.kt:153` -- `if (scanner == ScannerState.SCANNING) controls +=
   PairingControl.SCAN`. The scan control was offered only where the permission was **already
   granted**.
3. `PairingSurface.kt:343-351` -- `beginScanning()` holds the **only** `requestPermissions(CAMERA)`
   call in the app, and it is the SCAN button's click listener. Its own comment ("DENIED is
   re-askable") was written for exactly this case.

So: no permission, so no control; no control, so nothing could ask; nothing asked, so no permission
-- for the life of the install. Only the manual paste fallback worked, which is what the owner saw.

This failed **PB-PAIR-2 as written** (`docs/specifications/remote-phaseB-requirements.md:439`),
whose first clause is that the `CAMERA` runtime permission is *requested*. No ADR was needed: the
fix restores the recorded requirement rather than changing it.

## Why no existing test caught it

Every test that touched this screen's permission behaviour **passed a `ScannerState` in by hand**.
Nothing in the repository had ever constructed the state a phone that has never been asked is
actually in. The new tests resolve it through the real `PermissionStateResolver` with
`hasAskedBefore = false` instead of naming it.

## The runs

All Gradle runs are `./gradlew :app:testDebugUnitTest --rerun-tasks`, counted from the result XMLs
at the repository root.

| File | Phase | Result |
| --- | --- | --- |
| `camera-red.txt` | RED 1 -- the predicate does not exist | `compileDebugUnitTestKotlin FAILED`, 4x `Unresolved reference 'offersScanner'` |
| `panel-red.txt` | RED 2 -- predicate added, panel untouched | **693 tests, 3 failed** |
| `panel-green.txt` | GREEN -- panel asks the predicate | **693 tests, 0 failed** |
| `gate-red.txt` | Gate RED against the defective panel (`git show HEAD:`) | `TestQX9M_ThePairingPanelDecidesNoScannerStateOfItsOwn` FAIL |
| `gate-green.txt` | Gate GREEN on the fixed tree | 4 tests PASS |

RED 1 is a compile failure, which is honest for a missing symbol but says nothing about whether the
panel misbehaves. RED 2 exists so the red is at the **assertion** level: `PairingFlow.offersScanner`
was added alone, leaving `PairingPanel.kt:153` untouched, and the three failures are exactly the
tests that pin the user-visible defect:

```
PairingPanelScreenTest > a fresh install is offered the control that requests the camera FAILED
PairingPanelScreenTest > an ordinary denial keeps the typed fallback and still offers the re-askable scanner FAILED
PairingPanelViewTest > a fresh install has the scan button ON SCREEN FAILED
693 tests completed, 3 failed
```

The other new tests (granted leads to the scanner, a permanent denial withdraws the scanner and
offers Settings, the paste path survives every withheld-camera state) pass in RED 2 -- they are
regression guards on behaviour that was already correct, and they must not move.

## One existing test was re-pointed, not weakened

`PairingPanelScreenTest`'s `a denied camera withdraws the scanner and offers the typed fallback`
asserted `controls == setOf(TYPED_PAYLOAD, USE_TYPED_PAYLOAD)` for `PERMISSION_DENIED` -- i.e. that
SCAN is absent. That assertion **is** the catch-22, so no fix could keep it. It was re-pointed at
the contract PB-PAIR-2 states (`an ordinary denial keeps the typed fallback and still offers the
re-askable scanner`), and its surviving half -- that an ordinary denial does not route to Settings
-- is still asserted, in the neighbouring test. Reported to the team lead before it was touched.

## The gate

`android/gate/qx9m_camerareach_test.go`, two claims, both read from source text:

- **The app's only `requestPermissions(CAMERA)` is reachable from the view bound to
  `PairingControl.SCAN`.** This wire was *intact* throughout the defect, so this half is a fence on
  a different regression than the one that shipped -- cutting it would leave every Kotlin model test
  green. Its negative controls drive three real cuts through the same fault function.
- **`PairingPanel.kt` names no `ScannerState` value.** This is the defect's shape: two of the three
  answers about the camera were already asked of `PairingFlow`, and the third was inlined in the
  screen where nothing related it to them. `gate-red.txt` is this check failing against HEAD's
  panel.

What the gate deliberately does not fence -- above all *which* states offer SCAN, which is a
predicate over an enum that no regexp evaluates -- is recorded at the bottom of the gate file with
the reasons, and is covered where it can be executed, in the Kotlin suite.
