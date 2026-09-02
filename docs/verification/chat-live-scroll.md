# Conversation live-tail anchoring — TDD evidence

**Scope**: an open Android conversation follows new/streamed content only when the reader was
already following its tail. This is a repair to the accepted conversation-surface behavior, not a
new navigation decision. **Release target**: v0.13.25. Full-gate and handset evidence remain
explicitly pending below.

## RED: the policy was right, but the Android layout timing was not

`TranscriptIncremental.stickToBottom` already made the correct pre-mutation decision. The view
spent it immediately on a newly inserted zero-sized row, before Android measured the new tail. A
handset could therefore jump toward the top or leave the latest output below the viewport.

`ConversationLiveScrollTest.kt` uses the real conversation scaffold and completed layout passes.
Its initial four cases cover appended messages and growth of the existing streaming agent message,
each for a reader at the tail and a reader deliberately scrolled up. The first implementation
repaired append but the expanded streaming case exposed the incomplete anchor. This is the
transcript captured from the implementing agent's focused run before its later GREEN run overwrote
Gradle's mutable JUnit output; no standalone RED XML was retained:

```
ConversationLiveScrollTest: tests=4, failures=1
a streaming final message keeps a following reader anchored to the bottom
expected bottom 1011 but was scrollY 531
```

The three controls passed: appended content followed the tail, and neither append nor streaming
growth hijacked a reader who had scrolled up. The block above is the retained evidence; the current
JUnit path contains the subsequent GREEN result and is not cited as a historical RED artifact.

## Focused GREEN

After the layout-safe streamed-tail correction, review added two adversarial cases:

- a deferred tail follow is cancelled if the reader scrolls up before the next layout pass;
- a mixed update that inserts front history while the streamed tail grows preserves the history
  anchor instead of treating the tail rebind as permission to jump to the bottom.

The final real-view suite reports:

```
ConversationLiveScrollTest: tests=6, skipped=0, failures=0, errors=0
```

The focused Gradle run completed successfully in `46s`; its mutable JUnit report recorded all six
tests green (`15.573s` test-suite time). A nearby transcript/scroll policy selection also passed
all 54 tests:

```
$ cd android
$ . ./toolchain.env
$ ./gradlew :app:testDebugUnitTest \
    --tests '*ConversationLiveScrollTest' \
    --tests '*ConversationScrollTest' \
    --tests '*TranscriptIncrementalPositionTest' \
    --tests '*TranscriptIncrementalTest' \
    --tests '*TranscriptIncrementalRedrawTest' \
    --tests '*TranscriptDecisionTest' \
    --rerun-tasks --console=plain
BUILD SUCCESSFUL in 1m 49s
54 tests, 0 failures, 0 errors
```

Together, those runs prove that append and streaming growth follow an already-following reader;
scrolled-up readers, a newer scroll during deferred layout, and mixed history/tail updates preserve
their reading anchor. The historical RED above remains an implementing-agent transcript, not a
claim that mutable current JUnit output retains the failure.

## Required release evidence

- `[COMPLETE FOCUSED REGRESSION — final real-view suite 6/6 and nearby scroll/transcript selection
  54/54.]`
- `[PENDING FULL REGRESSION — full Android lint/unit gate and android/gate on the reviewed commit.]`
- `[PENDING PLAY — signed, Firebase-provenanced v0.13.25 AAB, Play versionCode 37, guarded dry-run
  followed by the authorized alpha-track publish.]`
- `[PENDING HANDSET — when already at the tail, appended and streamed agent output remains visibly
  anchored at the newest content; when the reader scrolls up, the same updates preserve that
  reading position.]`
