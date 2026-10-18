Native history pager qualification

Pinned OpenCode 1.19.3 source, commit e3c4eb242585dcd77be1a867391b7c25fd47ce52:
packages/opencode/src/session/message-v2.ts, messages pagination: session-scoped rows ordered by descending time_created/id, limit+1, hydrated selection reversed to ascending, next cursor encoded from the oldest selected native row. The host obtains an exact BeforeItem boundary by requerying the same native page start through that row, then forwards only the actual returned SDK cursor. It checks the prefix's oldest ID/time and requires every continuation row to be strictly older by time/id.

Tests use an authenticated private UDS bridge around the assembled daemon and a provider fixture that rejects invented native cursors. They cover newest/continuation, exact tool/file/plan/outcome anchors, missing/foreign anchors, equal/newer boundary refusal, equal-time ID ordering, whole-page shrinking, single-row oversize refusal, filtered empty pages, ended export boundaries, retained clipped detail, and read-only imported provenance. No native generation is used by these tests. Root's separate portable actual-native smoke qualifies the pinned SDK cursor behavior.

Bounds: overall read20s, 16 SDK page calls, native page limit50, 200 shaped items, 16KiB each, encoded page512KiB. Whole native rows are never cut to advance a cursor. Ended native CLI export reuses the existing exact-profile executable/storage guard and bounded8MiB/4096-message export. History reads do not import into the live reducer, journal, pending authority, or semantic operation ledger.
