# Recovering work in an existing personal Discord DM

## Field incident on v0.29.6

On 20 November 2026 the enrolled owner asked for a VM update and the status of an existing Android investigation. At 21:39:59 local time (20:39:59 UTC) Discord accepted the request. Five seconds later the bot asked for a restatement. At 21:41:04 the owner repeated the request and added an instruction to recover available history autonomously. The two owner messages had different Discord IDs, no edits and no attachments. Both remained readable through authenticated channel history.

The first message had a durable admission. The second was prepared behind it. The supervisor reported lost custody of the previous manager, one submitted terminal input, and a retained worker. The original worker had already produced its report. Outbox inspection found no confirmed manager publication of that report after completion. Repeating the investigation or asking the owner what it was about would have lost useful work. The CLI, live connector executable and live home supervisor executable all identified themselves as 0.29.6. The private evidence and report were retained outside the source tree.

## What the person needs at each boundary

The outcome is a reply to the original request in the same DM. A successful websocket reconnect, accepted admission, or finished worker by itself does not establish delivery. Check the owner's message, its current revision, the native submission, work already performed, deliberate publication and Discord acknowledgement. If any boundary is uncertain, identify which operation is affected and which later work remains safe. A person should only need to supply material that is actually missing. This applies the altitude, depth and range distinctions to the whole interaction. [Felix Rieseberg's tutorial](https://felixrieseberg.com/how-to-be-more-ambitious-a-tutorial-for-humans-with-models/)

| Observable failure | Evidence to consult | What recovery must protect |
| --- | --- | --- |
| Connector offline or gateway session expired | Resume sequence, verified channel identity and REST messages around the last durable boundary | The earliest eligible owner message and any later edit |
| Repeated request or duplicate event | Source ID and revision, job/attempt identity, action and publication receipts | A repeat can contain a new instruction. Preserve it while reusing work already done. |
| Manager dies, becomes rate limited or loses its observer during upgrade | Exact manager incarnation and custody, terminal input phase, executing action receipts, original worker | One manager with authority and a worker that can finish while the connector is down |
| Discord accepts a post but its HTTP response is lost | Durable publication ID, authenticated bot message, Discord nonce and receipt | Avoid reposting a completed action or claiming delivery without evidence |
| File arrives without a body or the worker starts after its URL expires | Verified owner, channel, message revision and attachment ID, captured bytes and actual worker readback | Preserve the referenced file and request a new copy only when recovery fails |

## Messaging system comparisons

Discord exposes resumable Gateway sequences and independently readable channel messages. A new Gateway session can establish connectivity without proving that missing requests have been processed. The channel endpoint returns newest first, so backfill needs a deliberate order and a persistent cursor. Attachment URLs are signed and expire. Refetching an authorized message can return a refreshed URL. [Gateway resume](https://docs.discord.com/developers/events/gateway#resuming), [channel messages](https://docs.discord.com/developers/resources/message#get-channel-messages), [signed attachment URLs](https://docs.discord.com/developers/reference#signed-attachment-cdn-urls)

Slack retries unacknowledged event deliveries and supplies an event identifier. Matrix assigns client transaction identifiers to distinguish a retry from a new request. Both inform the tests here: deliver the exact event twice, drop a successful acknowledgement, and send a separate owner message with similar wording. Only the last event can represent a new instruction. [Slack Events API](https://docs.slack.dev/apis/events-api/), [Matrix transaction identifiers](https://spec.matrix.org/latest/client-server-api/#transaction-identifiers)

## Isolated baseline

An unmodified v0.29.6 runtime and binary reproduced a second failure without contacting the personal DM. The default native dispatcher delegated a question to an ordinary worker. The worker received its exact answer. While that worker remained active, the rehearsal closed the connector, replaced its supervisor and let the worker complete into its durable mailbox. Reopening the same logical DM failed to publish the completion within 150 seconds. The controller reported uncertain native input. Two earlier publications remained, and the worker had recorded its queued completion. A queued note therefore demonstrated persistence and also demonstrated the missing last step in the old release.

## Owner DM attachment delivery

Inspection of v0.29.6 found that history exposed attachment metadata and that the current admission contained only the owner's text. Neither handoff supplied file bytes. The candidate fixture ingested an attachment-only owner DM via the connector Gateway handler and its authorized REST verifier. It refreshed an expired document URL, preserved the completed document when the PNG download returned HTTP 503, reopened the connector store, and delivered the PNG from the same original message. A separate persistent-outage fixture received one actionable mock DM notice and advanced the FIFO with per-file failure information after two minutes.

Two isolated native Codex rehearsals used a real manager and a delegated worker with synthetic Discord transport. The manager received a local manifest and instructions to have the worker read a random document marker and decode a PNG pixel. The assertions checked both values in the exact child's durable completion note and the manager's correlated publication. The first run passed in 59.33 seconds. The second run used the files recovered across the connector restart and passed in 82.97 seconds. Its worker instance was `6070b4ff4c48ba0ebfc73c1526f6f53f`, and its completion note was `semantic:1795208429872:51dd49d86eefc816cbd3ed50fcc5bfd7`. The publication sink was local. The tests used Linux and the normal VM user's filesystem permissions.

## Native quota-replacement rehearsal

An isolated Codex dispatcher started a real worker. The worker asked its question in 31 seconds and read the dispatcher's answer 8 seconds later. A test-only Go build overlay changed the status on one genuine Codex root-turn completion to the provider's typed `usageLimitExceeded` error. The callback matched the original physical manager, session instance, native thread and turn. The supervisor then reported provider exhaustion for that exact terminal turn while the connector was offline. The original worker queued its completion during the gap.

After reopening the connector, the supervisor retired the original manager for `provider_exhausted`. Its successor published the original worker's result in 26 seconds. The worker had one launch and retained instance `fb69356fd002a293e7d43ce2a007dbe4`; the question and completion were admissions 2 and 4. The test ended with one active manager on the same synthetic DM. The run passed in 85.76 seconds. Subscription billing, switching between real provider accounts and live Discord posting were outside this fixture.

To reproduce using the existing trusted native rehearsal setup, run `python3 internal/teammatemanager/testdata/quota_overlay.py /absolute/private/scratch` from the checkout and build the isolated test binary with `go build -overlay=/absolute/private/scratch/overlay.json -o /absolute/private/bin/swarm ./cmd/swarm`. Select that binary and its directory first on PATH for the hosted lifecycle test, with `SWARM_TEAMMATE_TEST_PERSONAL=1`, `SWARM_TEAMMATE_TEST_VM=1`, `SWARM_TEAMMATE_TEST_DURABLE=codex` and `SWARM_TEAMMATE_TEST_INTERRUPTION=quota`. The release binary uses the ordinary source tree.

## Interrupted round trips

Each trial below used a default native Codex dispatcher, one ordinary child, an exact needs-input note and a reply read by that child. The connector was closed while the child was still working. The child completed into its durable mailbox during the gap. On reopening the same logical DM, the test required a publication with the original attempt, the completion admission and the secret result. The end roster contained one worker launch and one active manager. The Discord publication sink was isolated.

| Interruption during the job | Result after reconnect | Manager outcome |
| --- | --- | --- |
| Connector outage | 10 s | Same native manager |
| Manager process death | 31 s | New manager, same worker |
| Supervisor restart | 24 s | New manager, same worker |
| v0.29.6 supervisor replaced by candidate | 21 s | New manager, same worker |
| Provider quota exhausted | 26 s | New manager, same worker. A test overlay changed one observed native turn into the provider's typed quota result. |

The quota rehearsal used an injected provider result and the normal supervisor, replacement and worker code. It did not consume a real subscription quota. Separate race tests rejected requests from the returning former owner, refused ambiguous concurrent managers, preserved an unsent draft, retained uncertain side effects, and persisted a failed replacement attempt across connector restart. One replacement launched after the retry deadline when the configured pool became available again.

A private replay of the three original field records through the candidate recovered both original owner IDs and excluded the bot's intervening notice. Additional connector tests exercise a history page outage, restart mid-pagination, deleted or edited sources, repeat wording with new details, duplicate delivery and a lost outgoing acknowledgement. Source replays do not send messages to the owner's DM. Duplicate suppressions use stable identities. A service outage can make the DM temporarily unreachable, and a deleted or inaccessible historical message cannot be reconstructed from Discord.
