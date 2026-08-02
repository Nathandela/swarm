# Play Console walkthrough — script for the Chrome-driving agent

**Tracks `agents-tracker-wsd`, prep for `agents-tracker-2qm`.** This is a script, not an essay: one
numbered step per Console screen, each naming the field and the exact value or file to enter. It
is written for a Claude-in-Chrome agent working alongside Nathan while he is signed in to his own
Google account — the agent drives the forms, Nathan is present to authenticate, pay, and approve
the steps marked below.

**Before touching the Console, read the preflight checklist at the end of this document.** Several
steps below are currently blocked on work that has not landed yet.

## How to read this script

- **Every value is sourced.** Each step cites the section of `docs/ops/play-closed-testing-application.md`
  it came from. Nothing here re-drafts that copy — if a value needs to change, change it there and
  this script inherits it by reference.
- **Two different markers, used precisely — do not treat them as the same thing:**
  - **`STOP — HAND BACK TO NATHAN`** marks an action that is irreversible or that makes something
    outward-facing (spends money, locks in an identifier permanently, or publishes something a
    real person outside this project can then see or receive). The agent completes the fields
    leading up to it, then stops and waits for Nathan's explicit go-ahead before clicking through.
  - **`BLOCKED — PREREQUISITE MISSING`** marks a step where the value or file this script would
    enter does not exist yet in the repository or environment. The agent should skip to the next
    unblocked step and report the gap rather than inventing a placeholder — a placeholder pasted
    into a real Play Console form is worse than an honest gap, because it can be submitted by
    accident.
- **Console labels drift.** Google reshuffles Play Console's navigation periodically. The section
  names below are accurate as of this writing, but if the on-screen label differs slightly, match
  by function (what the field is asking for) rather than treating a label mismatch as a reason to
  stop.

---

## 1. Create the developer account (skip if one already exists)

**Play Console → accounts.google.com sign-in → Play Console developer registration.**

- Fill in developer name (public, shown on all listings), contact email, and address details as
  prompted.
- **Identity verification follows registration and can take several days.** Google verifies the
  identity of every new developer account (government ID for an individual account, business
  documents such as a D-U-N-S number for an organization account) before the account can publish
  anything, including a closed test. This is not instant — schedule the rest of this walkthrough
  assuming a multi-day gap between this step and step 2, not a same-day one.
- **STOP — HAND BACK TO NATHAN.** Registration requires a **one-time USD 25 fee**, charged to a
  real payment method, and creates a persistent developer identity tied to this Google account.
  Confirm the payment and the account details with Nathan before submitting; this step is not
  sourced from the repo because it is Play account policy, not project state.

Not covered by `docs/ops/play-closed-testing-application.md` §0–§10 — that document assumes a
developer account already exists.

## 2. Create the app

**Play Console home → "Create app".**

| Field | Value | Source |
|---|---|---|
| App name | `swarm` | §1, §3 ("App name (30 char max)") |
| Default language | Operator's choice (English (US) is the safe default; nothing in the repo specifies one) | — |
| App or game | App | inferred from app category (§3: Tools) |
| Free or paid | Free | inferred — no monetization anywhere in the codebase or `docs/ops` |

Three mandatory policy checkboxes follow (Developer Program Policies, US export laws, and the
"not a copy" declaration) — check all three; they are true statements about this app, not
declarations this script can source individually.

This "Create app" action itself is low-stakes: an app shell with no uploaded build is normally
deletable from Console if abandoned. **The action that actually locks anything in is the first AAB
upload in step 12, not this one.**

## 3. Play App Signing — automatic, not a decision point

**Console enrolls this automatically the first time you prepare a release** (Release → Setup →
App signing, or presented inline during the first AAB upload flow). Informational only — there is
nothing to choose or confirm here.

Play App Signing has been mandatory for every app created from August 2021 onward
(<https://developer.android.com/studio/publish/app-signing>), and `dev.swarm.phone` is a new app
being created in 2026 — nowhere near that grandfather clause — so Console will not offer an opt-out
and this step needs no action beyond letting it happen. Full detail in
`docs/operations/release-signing.md`.

What this means for the keystore created in `agents-tracker-cwz`
(`~/.keystores/swarm-upload.jks`): it is only the **upload** key. Google generates and holds the
real app signing key, so if the upload keystore is lost or its password is forgotten, Nathan can
request an upload-key reset through Play Console (identity-verified, on the order of days), and
existing installs are unaffected because the key that actually signs what a device sees never
changes.

## 4. Store listing — main content

**Console → Grow (or "Store presence") → Main store listing.**

| Field | Value | Source |
|---|---|---|
| App name | `swarm` | §3 |
| Short description | `Watch and steer your coding agents from your phone. End-to-end encrypted.` | §3 |
| Full description | The full block under §3 "Full description" | §3 (paste verbatim; the doc notes the closing paragraph about "not a chat client" is optional to trim) |
| App category | Tools | §3 "Categorisation" |
| Tags | developer tools, remote access, productivity | §3 |
| Contact email (public, shown on listing) | **Not sourced — confirm with Nathan.** §3 says only "your public support address". `nathan.delacretaz@gmail.com` is known from this project's context but is a *personal* address; putting a personal address on a public store listing is Nathan's call, not this script's. Ask before filling this field. | §3 |

## 5. Store listing — graphics

**Same screen, "Graphics" section.**

| Asset | File | Source |
|---|---|---|
| App icon (512×512) | `docs/ops/play-assets/play-store-icon-512.png` | §3 "Graphic assets" table; file confirmed present in the repo |
| Feature graphic (1024×500) | `docs/ops/play-assets/play-feature-graphic-1024x500.png` | same |
| Phone screenshots (2–8 required) | **BLOCKED — PREREQUISITE MISSING.** §3 states these need a physical Android 13+ handset and cannot come from an emulator (`PB-KEY-8`'s hardware-downgrade refusal fails closed before any screen renders on emulated Keystore). `agents-tracker-p12` (the hardware run) is what produces them; nothing in the repo today is a screenshot. Do not substitute an emulator capture. | §3 |
| Tablet screenshots | Not required — no tablet support is declared | §3 |

## 6. Content rating questionnaire

**Console → Policy → App content → Content ratings.**

Category: **Utility, Productivity, Communication, or Other.**

| Question | Answer |
|---|---|
| Violence, sexuality, language, controlled substances, gambling, horror | No to all |
| Does the app allow users to interact or exchange content with other users? | No |
| Does the app share the user's current location with other users? | No |
| Does the app allow users to purchase digital goods? | No |
| Does the app contain or allow unrestricted access to the internet? | **Yes** — §5 flags this as a judgment call and recommends Yes since a stricter rating costs nothing and is safer than a reviewer later disagreeing with a No |
| Does the app natively allow users to communicate via text/voice/video? | No |

Source: §5 in full, including its own `[VERIFY]` note on the internet-access question.

## 7. Data safety form

**Console → Policy → App content → Data safety.**

| Top-level question | Answer |
|---|---|
| Does your app collect or share any of the required user data types? | Yes |
| Is all user data collected encrypted in transit? | Yes |
| Do you provide a way for users to request data deletion? | Yes |

Data type declarations:

| Type | Collected | Shared | Purpose | Optional? |
|---|---|---|---|---|
| Device or other IDs | Yes | Yes → Google (FCM) | App functionality | Required |
| App activity → Other user-generated content (or App info and performance → Other app data) | Yes | No | App functionality | Required |

Everything else (personal info, financial info, health, messages, photos, location, browsing
history, and the rest of §4's "What to answer No to" list) → **No**.

**Do not declare** cryptographic keys or pairing state — §4 is explicit that on-device-only data
is exempt, and this app's keys never leave the device (`allowBackup="false"` plus
`dataExtractionRules`).

Source: §4 in full, including its own `[VERIFY]` note on the terminal-content judgment call.

## 8. App access

**Console → Policy → App content → App access.**

| Field | Value |
|---|---|
| Is any functionality restricted? | Yes → All functionality restricted |
| Reviewer instructions | Paste §6's block verbatim, **with one required edit**: replace `<YOUR EMAIL>` with a real contact address before submitting. `nathan.delacretaz@gmail.com` is appropriate here — this text is seen only by Google's review team, not the public, unlike step 4's listing contact email. |

Source: §6, "App access — reviewer instructions (paste verbatim)".

## 9. Target audience and content

**Console → Policy → App content → Target audience.**

| Field | Value |
|---|---|
| Target age group | **18+ only.** Do not tick any bracket under 13 — that enrolls the app in the Families programme and its separate policy surface. |
| Appeal to children? | No |

Source: §6 table, "Target audience" and "Appeal to children?" rows.

## 10. Remaining App content declarations

**Same "App content" area, remaining sub-forms.**

| Declaration | Answer | Source |
|---|---|---|
| Ads | No | §6 |
| News app | No | §6 |
| COVID-19 contact tracing/status | No | §6 |
| Government app | No | §6 |
| Financial features | None of these | §6 |
| Health apps | No | §6 |

## 11. Privacy policy URL

**Console → Policy → App content → Privacy policy** (or the equivalent field on the main store
listing, depending on which the Console currently presents).

**BLOCKED — PREREQUISITE MISSING.** §6 and §9 both record this as **pending publication**:
`agents-tracker-pwc` is still open (checked directly against `bd` at the time this script was
written), so the URL `https://nathandela.github.io/swarm/ops/privacy-policy/` is not live yet. Do
not enter it into this field until `agents-tracker-pwc` closes and the page is confirmed reachable
— an unreachable privacy policy URL is a rejection reason on its own, and Play does periodically
re-check it.

## 12. Export compliance

**Console may prompt for this during the release flow, depending on territory.**

§7 gives the technical facts (TLS, Noise XXpsk0, X25519 via Keystore, AEAD; standard published
algorithms, likely ECCN 5D992.c mass-market) but flags itself `[VERIFY]` for the legal
determination. If the Console's export questionnaire asks for a classification beyond "uses
standard encryption for authentication/confidentiality," **stop and ask Nathan** rather than
answering a legal question from this script.

## 13. Build and upload the AAB

Confirm the artifact from `docs/operations/release-signing.md` exists and is signed before this
step:

```
android/app/build/outputs/bundle/release/app-release.aab
```

**Console → Release → Testing → Closed testing → [create or select the track].**

1. Create track, name it `alpha` (§8, step 1).
2. **STOP — HAND BACK TO NATHAN before uploading.** Uploading the first AAB to this application
   permanently locks the package name `dev.swarm.phone` to this developer account — Google
   documents this as unrecoverable, so confirm this is the intended, final artifact (the correct
   `targetSdk`/`compileSdk` per the preflight checklist below, signed with the intended keystore)
   before this upload, not after.
3. Upload `app-release.aab`, add release notes (not pre-drafted anywhere in the repo — ask Nathan
   for release notes text, or use a short factual line such as "Initial closed-testing build").

Source: §8 step 4, §2 (build/signing).

## 14. Add testers and countries

**Same "Closed testing" track screen, "Testers" and "Countries" tabs.**

- Testers: create an email list containing `nathan.delacretaz@gmail.com` (matches
  `agents-tracker-2qm`'s own plan). §8 notes testers must opt in via the share link before they
  can install — adding an email here does not itself distribute anything.
- Countries: **pick explicitly.** §8 warns a track with no country selected reaches nobody. No
  specific list is prescribed anywhere in the repo — confirm which countries with Nathan.

Source: §8, steps 2–3.

## 15. Roll out the release

**Same screen, "Review release" / "Start rollout to closed testing" button.**

**STOP — HAND BACK TO NATHAN.** This is the actual outward-facing submission: it sends the release
for Google's review and, once approved, makes the app installable by every address on the tester
list. This is the point of no return `agents-tracker-2qm`'s own acceptance criteria names
explicitly ("nothing submitted past a point of no return without Nathan's explicit OK"). Stop here
regardless of how mechanical everything before it was.

Source: §8 step 4; `agents-tracker-2qm` description.

---

## What this script does not cover

- **The 12-tester/14-day graduation rule** (§8, "The graduation rule — plan for it now") gates
  *production* promotion, not closed testing itself — it does not block anything in this script,
  but recruit testers above the stated minimum from the start per §8's own advice, since the count
  is of testers *continuously* opted in.
- **Firebase / push setup** (§0 blocker #2) is independent of the Console submission mechanics
  above — the app installs and the listing is reviewable either way; only background wake is
  affected. Not a step in this script.
- Anything past the initial rollout (responding to a review rejection, promoting to production,
  handling the graduation-rule testers) is out of scope here.

---

## Preflight checklist — confirm ALL of these before opening the Console

1. **Signed AAB exists and verifies.** `android/app/build/outputs/bundle/release/app-release.aab`
   is present and `jarsigner -verify -verbose -certs` reports `jar verified.`
   (`docs/operations/release-signing.md`).
2. **Privacy policy is live**, not just drafted — fetch the URL and confirm it resolves before
   entering it in step 11. As of this writing it does not yet (`agents-tracker-pwc` open).
3. **Phone screenshots are captured from real hardware**, not an emulator — the app cannot start
   on emulated Keystore (§3), so a screenshot that exists at all is proof it came from a real
   device. As of this writing none exist (`agents-tracker-p12` open).
4. **`targetSdk` is correct for the actual upload date.** Today (this document's writing date,
   2026-08-02) the pinned value of 35 is still accepted for a new app. **That changes on August
   31, 2026** — Play requires 36 for new apps and updates from that date, and closed-testing
   tracks are not exempt (`docs/ops/play-closed-testing-application.md` §0, verified against
   Google's own current policy page in `agents-tracker-cwz`; tracked as its own bump in
   `agents-tracker-xfw`). **If the AAB in step 13 is built on or after August 31, 2026, confirm
   `agents-tracker-xfw` has landed before uploading it** — an AAB built at 35 past that date will
   be rejected at upload regardless of everything else in this script being correct.

If any of these four is not true, stop before opening Play Console at all — every step above that
depends on them is marked `BLOCKED` for exactly this reason.
