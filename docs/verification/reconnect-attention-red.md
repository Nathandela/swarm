# Reconnection and attention failing-first evidence

Captured before each corresponding implementation/fix on 2026-10-10. Fixtures contain no account credentials.

## attention-red.log

```text
=== RUN   TestAttentionThreadStatus
    attention_test.go:38: Codex must expose the optional pure EventStatus normalizer
--- FAIL: TestAttentionThreadStatus (0.00s)
=== RUN   TestAttentionUserInput
    attention_test.go:68: Codex must expose the optional pure EventStatus normalizer
--- FAIL: TestAttentionUserInput (0.00s)
=== RUN   TestAttentionTurnCompleted
    attention_test.go:103: Codex must expose the optional pure EventStatus normalizer
--- FAIL: TestAttentionTurnCompleted (0.00s)
=== RUN   TestAttentionRejectsUntrustedFramesWithoutStaticFallback
    attention_test.go:121: Codex must expose the optional pure EventStatus normalizer
--- FAIL: TestAttentionRejectsUntrustedFramesWithoutStaticFallback (0.00s)
=== RUN   TestAttentionNormalizerDoesNotClaimOtherEvents
    attention_test.go:158: Codex must expose the optional pure EventStatus normalizer
--- FAIL: TestAttentionNormalizerDoesNotClaimOtherEvents (0.00s)
=== RUN   TestAttentionNormalizerIsPure
    attention_test.go:170: Codex must expose the optional pure EventStatus normalizer
--- FAIL: TestAttentionNormalizerIsPure (0.00s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/adapter/codex	0.189s
=== RUN   TestAttentionErrorDerivation
=== RUN   TestAttentionErrorDerivation/running/idle
    attention_test.go:18: provider error with process=running turn=idle: group=ready_for_review, want needs_input
=== RUN   TestAttentionErrorDerivation/running/active
=== RUN   TestAttentionErrorDerivation/running/unknown
=== RUN   TestAttentionErrorDerivation/exited/idle
=== RUN   TestAttentionErrorDerivation/exited/active
=== RUN   TestAttentionErrorDerivation/exited/unknown
=== RUN   TestAttentionErrorDerivation/lost/idle
=== RUN   TestAttentionErrorDerivation/lost/active
=== RUN   TestAttentionErrorDerivation/lost/unknown
--- FAIL: TestAttentionErrorDerivation (0.00s)
    --- FAIL: TestAttentionErrorDerivation/running/idle (0.00s)
    --- PASS: TestAttentionErrorDerivation/running/active (0.00s)
    --- PASS: TestAttentionErrorDerivation/running/unknown (0.00s)
    --- PASS: TestAttentionErrorDerivation/exited/idle (0.00s)
    --- PASS: TestAttentionErrorDerivation/exited/active (0.00s)
    --- PASS: TestAttentionErrorDerivation/exited/unknown (0.00s)
    --- PASS: TestAttentionErrorDerivation/lost/idle (0.00s)
    --- PASS: TestAttentionErrorDerivation/lost/active (0.00s)
    --- PASS: TestAttentionErrorDerivation/lost/unknown (0.00s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/status	0.177s
FAIL
```

## reconnect-red.log

```text
--- FAIL: TestReconnect_RetriesPastSixFailures (0.00s)
    reconnect_test.go:49: automatic recovery stopped before the daemon returned
--- FAIL: TestReconnect_RequiresRosterAndSubscription (0.00s)
    --- FAIL: TestReconnect_RequiresRosterAndSubscription/roster (0.00s)
        reconnect_test.go:75: incomplete candidate reported successful recovery
    --- FAIL: TestReconnect_RequiresRosterAndSubscription/nil_events (0.00s)
        reconnect_test.go:75: incomplete candidate reported successful recovery
    --- FAIL: TestReconnect_RequiresRosterAndSubscription/dial_error (0.00s)
        reconnect_test.go:75: incomplete candidate reported successful recovery
--- FAIL: TestReconnect_DrainsFloodDuringHydration (0.20s)
    reconnect_test.go:102: event flood blocked the subscription while List was running
--- FAIL: TestReconnect_ClosesDisplacedClientAndIgnoresStalePayloads (0.00s)
    reconnect_test.go:121: replacement did not close the displaced client
--- FAIL: TestReconnect_OfflineConfirmAndRenameKeepTheirDrafts (0.00s)
    reconnect_test.go:141: offline confirmation submitted or consumed
--- FAIL: TestReconnect_CandidateArrivingAfterCloseIsReleased (0.00s)
    reconnect_test.go:165: TUI has no shared recovery cleanup
FAIL
FAIL	github.com/Nathandela/swarm/internal/tui	0.541s
FAIL
```

## backend-attention-red.log

```text
=== RUN   TestBackendAttentionThreadStatusAndRecovery
    backend_attention_test.go:67: frame {"method":"thread/status/changed","params":{"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce","status":{"type":"active","activeFlags":[]}}}: turn= interaction=, want active/none
--- FAIL: TestBackendAttentionThreadStatusAndRecovery (0.05s)
=== RUN   TestBackendAttentionConcurrentRequestsResolveOnlyTheirOwnWait
    backend_attention_test.go:73: frame {"method":"item/tool/requestUserInput","id":1,"params":{"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce","turnId":"turn-1","itemId":"question-1","isBlocking":true,"questions":[{"id":"q1","header":"Choice","question":"Which option?","options":null}]}}: turn= interaction=, want idle/prompt
--- FAIL: TestBackendAttentionConcurrentRequestsResolveOnlyTheirOwnWait (0.04s)
=== RUN   TestBackendAttentionNonblockingRequestsNeverClearOtherAttention
=== RUN   TestBackendAttentionNonblockingRequestsNeverClearOtherAttention/working
=== RUN   TestBackendAttentionNonblockingRequestsNeverClearOtherAttention/question
    backend_attention_test.go:97: frame {"method":"item/tool/requestUserInput","id":1,"params":{"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce","turnId":"turn-1","itemId":"pending","isBlocking":true,"questions":[{"id":"q1","header":"Choice","question":"Which option?","options":null}]}}: turn= interaction=, want idle/prompt
=== RUN   TestBackendAttentionNonblockingRequestsNeverClearOtherAttention/approval
    backend_attention_test.go:99: frame {"method":"serverRequest/resolved","params":{"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce","requestId":2}}: turn=idle interaction=none, want idle/permission
=== RUN   TestBackendAttentionNonblockingRequestsNeverClearOtherAttention/error
    backend_attention_test.go:97: frame {"method":"turn/completed","params":{"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce","turn":{"id":"turn-1","items":[],"status":"failed","error":null}}}: turn=idle interaction=none, want idle/error
--- FAIL: TestBackendAttentionNonblockingRequestsNeverClearOtherAttention (0.17s)
    --- PASS: TestBackendAttentionNonblockingRequestsNeverClearOtherAttention/working (0.04s)
    --- FAIL: TestBackendAttentionNonblockingRequestsNeverClearOtherAttention/question (0.04s)
    --- FAIL: TestBackendAttentionNonblockingRequestsNeverClearOtherAttention/approval (0.04s)
    --- FAIL: TestBackendAttentionNonblockingRequestsNeverClearOtherAttention/error (0.05s)
=== RUN   TestBackendAttentionOldResolutionPreservesReplacementAnswerability
    backend_attention_test.go:109: frame {"method":"serverRequest/resolved","params":{"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce","requestId":1}}: turn=idle interaction=none, want idle/permission
--- FAIL: TestBackendAttentionOldResolutionPreservesReplacementAnswerability (0.05s)
=== RUN   TestBackendAttentionTurnCompletionEndsRequestLifetime
=== RUN   TestBackendAttentionTurnCompletionEndsRequestLifetime/completed
    backend_attention_test.go:122: frame {"method":"item/tool/requestUserInput","id":1,"params":{"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce","turnId":"turn-1","itemId":"old-question","isBlocking":true,"questions":[{"id":"q1","header":"Choice","question":"Which option?","options":null}]}}: turn= interaction=, want idle/prompt
=== RUN   TestBackendAttentionTurnCompletionEndsRequestLifetime/interrupted
    backend_attention_test.go:122: frame {"method":"item/tool/requestUserInput","id":1,"params":{"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce","turnId":"turn-1","itemId":"old-question","isBlocking":true,"questions":[{"id":"q1","header":"Choice","question":"Which option?","options":null}]}}: turn= interaction=, want idle/prompt
=== RUN   TestBackendAttentionTurnCompletionEndsRequestLifetime/failed
    backend_attention_test.go:122: frame {"method":"item/tool/requestUserInput","id":1,"params":{"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce","turnId":"turn-1","itemId":"old-question","isBlocking":true,"questions":[{"id":"q1","header":"Choice","question":"Which option?","options":null}]}}: turn= interaction=, want idle/prompt
--- FAIL: TestBackendAttentionTurnCompletionEndsRequestLifetime (0.13s)
    --- FAIL: TestBackendAttentionTurnCompletionEndsRequestLifetime/completed (0.04s)
    --- FAIL: TestBackendAttentionTurnCompletionEndsRequestLifetime/interrupted (0.05s)
    --- FAIL: TestBackendAttentionTurnCompletionEndsRequestLifetime/failed (0.04s)
=== RUN   TestBackendAttentionMalformedAndForeignFramesCannotClearWait
    backend_attention_test.go:140: frame {"method":"item/tool/requestUserInput","id":1,"params":{"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce","turnId":"turn-1","itemId":"pending","isBlocking":true,"questions":[{"id":"q1","header":"Choice","question":"Which option?","options":null}]}}: turn= interaction=, want idle/prompt
--- FAIL: TestBackendAttentionMalformedAndForeignFramesCannotClearWait (0.05s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/skeleton	0.820s
=== RUN   TestAttentionErrorSurvivesGridFreshnessUntilHealthyTypedEvent
=== RUN   TestAttentionErrorSurvivesGridFreshnessUntilHealthyTypedEvent/turn/started
=== RUN   TestAttentionErrorSurvivesGridFreshnessUntilHealthyTypedEvent/thread/status/changed
--- PASS: TestAttentionErrorSurvivesGridFreshnessUntilHealthyTypedEvent (0.00s)
    --- PASS: TestAttentionErrorSurvivesGridFreshnessUntilHealthyTypedEvent/turn/started (0.00s)
    --- PASS: TestAttentionErrorSurvivesGridFreshnessUntilHealthyTypedEvent/thread/status/changed (0.00s)
PASS
ok  	github.com/Nathandela/swarm/internal/engine	0.133s
FAIL
```

## backend-wait-snapshot-red.log

```text
=== RUN   TestBackendAttentionWaitingSnapshotPreservesKnownRequests
    backend_attention_test.go:87: frame {"method":"item/tool/requestUserInput","id":2,"params":{"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce","turnId":"turn-1","itemId":"question","isBlocking":true,"questions":[{"id":"q1","header":"Choice","question":"Which option?","options":null}]}}: turn=idle interaction=prompt, want idle/permission
--- FAIL: TestBackendAttentionWaitingSnapshotPreservesKnownRequests (0.05s)
=== RUN   TestBackendAttentionResolutionPreservesUncorrelatedSnapshotWait
--- PASS: TestBackendAttentionResolutionPreservesUncorrelatedSnapshotWait (0.05s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/skeleton	0.450s
FAIL
```

## backend-snapshot-correlation-red.log

```text
=== RUN   TestBackendAttentionBothSnapshotFlagsRetainUnobservedQuestion
    backend_attention_test.go:104: frame {"method":"serverRequest/resolved","params":{"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce","requestId":1}}: turn=active interaction=none, want idle/prompt
--- FAIL: TestBackendAttentionBothSnapshotFlagsRetainUnobservedQuestion (0.04s)
=== RUN   TestBackendAttentionObservedRequestCorrelatesEarlierSnapshot
    backend_attention_test.go:111: frame {"method":"serverRequest/resolved","params":{"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce","requestId":1}}: turn=idle interaction=prompt, want active/none
--- FAIL: TestBackendAttentionObservedRequestCorrelatesEarlierSnapshot (0.03s)
FAIL
FAIL	github.com/Nathandela/swarm/internal/skeleton	0.431s
FAIL
```

## appserver-envelope-red.log

```text
--- FAIL: TestAttentionRejectsAmbiguousEnvelopeBeforeCallbacks (0.00s)
    attention_envelope_test.go:55: 6 ambiguous envelopes reached callbacks
FAIL
FAIL	github.com/Nathandela/swarm/internal/appserver	0.258s
FAIL
```

## reconnect-final-cases-red.log

```text
--- FAIL: TestReconnect_StaleStateVisibleAcrossScreens (0.00s)
    reconnect_test.go:210: screen 1 hides its disconnected state
--- FAIL: TestReconnect_RefreshCannotUndoCompletedRename (0.00s)
    reconnect_test.go:226: an earlier List undid the completed rename instead of refreshing
--- FAIL: TestReconnect_HydratedBannerExpires (6.14s)
    reconnect_test.go:243: hydrated attention banner did not expire
FAIL
FAIL	github.com/Nathandela/swarm/internal/tui	6.501s
FAIL
```

## reconnect-options-red.log

```text
--- FAIL: TestReconnect_OptionsReloadPreservesPolicyDraft (0.00s)
    reconnect_test.go:264: open options did not reload its replacement connection
FAIL
FAIL	github.com/Nathandela/swarm/internal/tui	0.354s
FAIL
```

## reconnect-accounts-options-red.log

```text
--- FAIL: TestReconnect_OptionsReloadPreservesPolicyDraft (0.00s)
    --- FAIL: TestReconnect_OptionsReloadPreservesPolicyDraft/5 (0.00s)
        reconnect_test.go:266: open options did not reload its replacement connection
FAIL
FAIL	github.com/Nathandela/swarm/internal/tui	0.289s
FAIL
```

## reconnect-roster-restore-red.log

```text
--- FAIL: TestReconnect_AuthoritativeRosterCanRestoreAnAbsentSession (0.00s)
    reconnect_test.go:301: authoritative roster suppressed a session restored by the daemon
FAIL
FAIL	github.com/Nathandela/swarm/internal/tui	0.302s
FAIL
```

## reconnect-initial-subscription-red.log

```text
--- FAIL: TestReconnect_InitialSubscriptionFailureStartsRecovery (0.10s)
    --- FAIL: TestReconnect_InitialSubscriptionFailureStartsRecovery/error (0.10s)
        reconnect_test.go:365: initial subscription failure silently kept a live-looking view
    --- FAIL: TestReconnect_InitialSubscriptionFailureStartsRecovery/nil_channel (0.00s)
        reconnect_test.go:353: initial subscription failure left no recovery signal
FAIL
FAIL	github.com/Nathandela/swarm/internal/tui	0.450s
FAIL
```
