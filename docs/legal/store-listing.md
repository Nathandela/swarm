# Play Store listing — draft for approval

Not published. This is the copy for `dev.swarm.phone`'s Play Store listing, written for the
owner to approve, edit or reject before any of it goes into the Console. It is kept in the
repository so the words that describe the app live beside the app, and so a change to them shows
up in the history.

---

## App name

    swarm

(Already set in the Console.)

## Short description

*80 character limit. This is what shows under the app name in search results.*

    Watch and control the coding agents on your computer, from your phone.

69 characters.

## Full description

*4000 character limit.*

    swarm is a companion app for the swarm command-line tool, which runs on your own computer.
    It is not a standalone app: without a computer running swarm, there is nothing for it to show
    you.

    If you leave coding agents running while you step away from your desk, swarm is how you keep
    an eye on them. It shows which sessions are waiting on you, which are still working, and which
    have finished — so you can answer a prompt from the kitchen instead of finding it an hour later.

    WHAT IT DOES

    - See every agent session on your computer, grouped by what needs you
    - Read what a session is doing, as plain text from its terminal
    - Answer a prompt, stop a session, or start a new one
    - Get a notification when something is waiting on you

    HOW IT CONNECTS

    You pair the phone with your computer once, by scanning a QR code that your computer displays.
    From then on the link is end-to-end encrypted between the two: messages pass through a relay
    you deploy yourself, and that relay cannot read session names, terminal output, or anything
    you type.

    There is no swarm account, no sign-up, and no server operated by the developer. Nothing in the
    app sends anything to us, because there is nowhere for it to go.

    WHAT YOU NEED

    - A computer running the swarm command-line tool (macOS or Linux)
    - A relay you deploy — see the documentation

    swarm is open source: github.com/Nathandela/swarm

## Category

    Tools

## Contact details

- Email: nathan.delacretaz@gmail.com
- Website: https://github.com/Nathandela/swarm
- Privacy policy: https://github.com/Nathandela/swarm/blob/main/docs/legal/privacy-policy.md

## Graphics — all present

Play requires all of these before a listing can be saved.

| Asset | Spec | Status |
|---|---|---|
| App icon | 512 x 512 PNG, 32-bit | Done — Atmospheric Swarm, clean generated trajectories on the mobile Slate field, `docs/ops/play-assets/play-store-icon-512.png` |
| Feature graphic | 1024 x 500 PNG or JPEG | Done — Solid Wedge on the product ground, `docs/ops/play-assets/play-feature-graphic-1024x500.png` |
| Phone screenshots | 2 to 8, min 320 px, 16:9 or 9:16 | Emulator set exists (`docs/design/store-assets/screenshots/`: pairing, scanner, first run, plus 7in/10in tablet). Paired-app screenshots (session list, machines, activity) tracked as agents-tracker-h4yg |

The paired-app screenshots are worth taking by hand on the real handset rather than generating:
the pairing screen and the session list are the two that say what this app is, and a screenshot
of an empty inbox says nothing.
