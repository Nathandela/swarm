# Managed OpenCode sessions

This implementation is qualified against OpenCode **1.19.3**. Release qualification
is still in progress; consult the [implementation evidence](../verification/opencode-implementation-2026-10-18.md)
for the tested source revisions and remaining gates. Existing standalone sessions
retain their original behavior. Development builds do not replace the installed Swarm.

## Start and monitor work

The Swarm launch form uses the same editable model selector as other harnesses.
Built-in models use their complete native IDs, including
`opencode/deepseek-v4.1-flash-free` and `opencode/glm-5.3-flash-free`.
Leaving the field untouched preserves the target project's native default.

Managed workers retain one normal OpenCode TUI, native profile and exact native
root. Detaching or restarting the Swarm daemon preserves that worker. Swarm
refuses to guess a replacement conversation from a shared working directory.
Native children are shown as native descendants; separately spawned Swarm workers
retain their own supervision relationship.

`swarm ls --json`, `swarm watch SESSION --until needs_input`, and pilot reads expose
native activity, pending requests and observation gaps. A native idle state does
not establish successful completion of a particular input.

Use semantic messages for agent communication:

```sh
swarm message SESSION --text 'Review the latest change'
swarm message SESSION --status --operation-id OPERATION
```

Retain the printed operation ID. Admission means the exact native user input
exists; completion is reported separately. An uncertain receipt must be inspected,
not resubmitted. `swarm send` remains an explicit terminal operation. Attached
control, an existing draft, native busy work and unqualified observation can refuse
semantic submission before any input is sent.

## Inspect and answer native requests

First list pending requests, then read the complete card for the exact root or child:

```sh
swarm requests SESSION
swarm requests SESSION --request-id REQUEST --native-session-id NATIVE_SESSION
```

Inspect its full native context, content hash and offered choices. Answer using
that observed hash:

```sh
swarm decide SESSION --request-id REQUEST --native-session-id NATIVE_SESSION \
  --request-hash HASH --reply once
swarm decide SESSION --request-id REQUEST --native-session-id NATIVE_SESSION \
  --request-hash HASH --reply answer --answers '[["first choice"],["custom answer"]]'
swarm decide SESSION --status --operation-id OPERATION
```

Question groups and answers preserve native order. Permission `always` installs
the native patterns and can resolve matching requests in that native session;
permission `reject` rejects its pending permission requests. Question rejection
targets the exact question. These effects are displayed on the card.

Swarm binds answers to the worker instance, backend generation, native target,
request content and expiry. Missing, oversized, imported, expired or unqualified
cards cannot grant authority. Native request disappearance alone does not prove
that an uncertain answer was processed. Receipt status reads never post again.

## Recovery and limits

Preserve the source checkout, native profile, root and operation IDs before ending
a worker. Resume uses that saved binding. Changed explicit resume models and
unqualified executable identities are refused; use the native picker or an
explicit fresh handoff where appropriate. Follow the
[managed refresh policy](cli-session-refresh.md#managed-opencode-policy) when the
installed CLI changes.

Strict turn Stop remains unavailable because the pinned native API cannot fence
the expected turn. Account rotation, automatic relogin and Swarm-initiated native
compaction are not enabled. Native automatic compaction can create a continuation
without an authoritative link to the original input; its original receipt remains
unresolved instead of claiming another input's completion.

Native free-model quota and generation remain provider-controlled. Scripted
localhost provider tests qualify runtime behavior, not external model availability
or model quality. Actual 24-hour and 72-hour qualification remains outstanding.
