# swarm — Privacy Policy

**Last updated: 2026-08-03**

swarm is an Android app that lets you watch and control coding agents running on **your own
computer**. This policy describes what the app does with data. It is written to be checkable
against the source, which is public: where a claim below can be verified in code, the file is
named.

---

## The short version

**The developer of swarm operates no server and receives no data from you.** There is no swarm
account, no sign-up, no analytics, no advertising, and no crash reporting. Nothing in the app
sends anything to the developer.

The app talks to one computer: yours, the one you pair it with. What it sends is encrypted end
to end between the phone and that computer.

---

## 1. What the app collects

**Nothing is collected by the developer.** There is no backend under the developer's control for
data to arrive at.

The app stores the following **on your device only**:

| What | Why | Where |
|---|---|---|
| Pairing identity and keys for the computer you paired with | So the phone can authenticate to your computer, and your computer to it | Android Keystore and the app's private storage |
| The name of the paired computer | To show which machine you are attached to | App's private storage |
| Cached session information and recent terminal output | So the app can show you what your agents are doing | App's private storage, encrypted at rest |
| A push notification token, if notifications are enabled | So your computer can wake the app | App's private storage, and your relay (see section 3) |

Removing the pairing (**Settings → Replace this computer**) destroys the encryption keys and the
cached content on the device. Uninstalling the app removes everything.

## 2. Permissions, and what each is actually used for

- **Camera** — used for exactly one thing: scanning the pairing QR code your computer displays.
  The camera runs only while you are on the pairing screen. **No image or video is stored,
  transmitted, or retained.** Only the decoded pairing payload is used, and only in memory.
- **Internet** — to reach your computer through a relay you configure (section 3).
- **Notifications** — to tell you when an agent needs your attention.
- **Run at startup** — to restore notification delivery after the phone reboots.

The app requests no location, no contacts, no microphone, no storage, and no phone identifiers.

## 3. The relay, and what it can see

The phone does not usually reach your computer directly. It reaches it through a **relay** — a
small server that passes messages along. **You choose and deploy the relay.** The developer does
not operate one, and the app has no default relay built in.

The connection is **end-to-end encrypted between the phone and your computer**. A relay operator
cannot read session names, hostnames, agent names, terminal output, keystrokes, or any command
you send.

A relay operator **can** observe, and you should assume they do:

- which machines and devices exist, as opaque routing identifiers
- when a device or computer is connected, and when it goes silent
- the size and timing of every message
- your push notification token, if notifications are enabled

This is documented in full, including the parts that are less flattering, in
[`docs/operations/metadata-disclosure.md`](../operations/metadata-disclosure.md).

If you run the relay yourself, you are the only operator. If you use someone else's, that
operator sees the above.

## 4. Push notifications

If you enable notifications, the app registers with **Firebase Cloud Messaging** (Google) and
receives a token. The token is stored on your device and on your relay, which uses it to wake the
app. Google's handling of that token is governed by
[Google's Privacy Policy](https://policies.google.com/privacy).

Notification payloads carry **no content** — they are a signal to wake up and fetch, not the
message itself. Disabling notifications stops this entirely; the rest of the app is unaffected.

## 5. What the app never does

- No advertising, no ad networks, no advertising identifier
- No analytics or usage tracking of any kind
- No crash or diagnostic reporting to the developer
- No selling or sharing of data — there is no data to sell
- No account, no email address, no name, no phone number

## 6. Children

swarm is a developer tool for controlling software on a computer you own. It is not directed at
children and is not designed for them.

## 7. Your control

- **See what is stored** — everything is on your device and on the computer you paired with.
- **Delete it** — Settings → Replace this computer destroys the keys and cached content.
  Uninstalling removes all app data.
- **Revoke from the computer** — `swarm remote revoke` on your machine ends the pairing from the
  other side. The phone notices and returns to the pairing screen.

## 8. Changes

Changes to this policy will be committed to this file, with the change visible in the
repository's history.

## 9. Contact

nathan.delacretaz@gmail.com

Source: <https://github.com/Nathandela/swarm>
