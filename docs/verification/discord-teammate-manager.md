# Discord teammate implementation-plan verification

19 November 2026 · [Implementation plan](../specifications/discord-teammate-manager.md) · reviewed against v0.28.0, `8b6e5c371b6b60ddf6ad61ee311ba3edadb320ed`. The planned teammate route has not shipped. The dedicated server thread is retained with the operator; public evidence below uses synthetic data and excludes private server identifiers.

## Observations

| Test | What ran | Result and boundary |
|---|---|---|
| Live app and Gateway | Existing credential used in place on the home VM, bot identity REST lookup, brief WebSocket Identify for guild/message/message-content intents | Gateway READY. No production connector restart, new identity or permission change. Prior read-only check found history access denied on 13 active threads under two other parents. The dedicated channel allows read/send/history/public thread create/send. |
| Real Discord thread from an opener | Posted one clearly labeled synthetic opener in the designated test channel, started a thread from it, checked guild, parent and thread ID, fetched the starter by its original address | Thread ID matched the invoking message ID. This proves the transport can derive and verify the reply destination. |
| Read previous discussion | Wrote two synthetic, bot-authored posts to that thread, then requested one message before the second post | Returned the first post by exact ID. The test establishes backward paging and parent-root access in the dedicated channel. It does not establish human pre-mention access across all deployed channels. |
| Gateway and publication retries | Observed the second post through the live WebSocket and retried its REST create using the same nonce and enforce_nonce | The original ID was returned. This is short-window suppression, not indefinite exactly-once delivery. No production manager handled the test thread. |
| Baseline release suites | Independent test lanes ran the unmodified release Discord, voice, and protocol schema packages | All three passed. These are existing component contracts. |
| Cold addressing, ordinary follows and scope | Empty-roster synthetic human mention; another human sends 110 untagged messages in a joined route; guild-only scope resolves without an API client | Zero native inputs and one directory notice on first contact. Zero inputs for 110 follow-ups, 50 cached, 60 evicted. The resolver accepted an unclassified channel. Each result requires new implementation for the proposed UX. |
| Combined wire receipt | Real owner-protocol Unix socket with synthetic native backend, genuine message origin and manager admission on the same request | The server accepted both; mutating human principal or manager nonce conflicted on replay. Either missing capability refused before native send. |
| Text admission recovery | Controller submits synthetic text, loses acknowledgement and is recreated on the same store | Exactly one native send, with manager admission present and Discord origin absent. The integration must construct and persist both at the controller boundary. |
| Home VM native manager | Earlier isolated text-manager rehearsal against installed release and default native CLI | Launch refused before inference. Installed Codex 0.170.0 differs from the release's qualified 0.167.0. Installed Claude 2.1.328 differs from the qualified 2.1.319/2.1.326. No alternative CLI was installed. The [existing hosted worker evidence](voice-manager/controller-integration.md) is upstream evidence, not a live Discord-to-worker demonstration on this VM. |

## Reproducing the component spikes

Use a disposable v0.28.0 checkout with its normal shared Go cache. From the new plan branch, set the two absolute paths in your shell and run the following. The fixtures deliberately assert the released behavior, so keep them separate from the future teammate implementation's acceptance suite.

```sh
for pkg in discord protocol voice; do
  cp "$plan_checkout/docs/verification/discord-teammate-probes/$pkg.go.txt" \
    "$release_checkout/internal/$pkg/plan_spike_test.go"
done
cd "$release_checkout"
go test -count=1 ./internal/discord ./internal/voice ./internal/protocol/schema
go test -count=1 -v ./internal/discord ./internal/protocol ./internal/voice \
  -run '^TestTeammate(Plan|CombinedWire|Admission)Spike$'
```

To reproduce the authorized transport test, open a temporary Gateway client using the installed app identity. Verify the server and dedicated text channel by ID before creating anything. Post a synthetic opener with mentions disabled; start its thread via the message endpoint, write two synthetic messages and retry the second immediately with the same nonce. Compare the returned message IDs, verify the Gateway MESSAGE_CREATE channel and author, retrieve the first message with before=second ID and limit=1, and fetch the parent starter by exact ID. Close the connection. Keep all app credentials, Discord IDs and any human message bodies off public logs. [Discord threads](https://docs.discord.com/developers/topics/threads) · [Discord messages](https://docs.discord.com/developers/resources/message)

## Independent review and resolution

Sol and Astra each traced the release source independently, then reviewed the complete draft against the agreed user flow. Their first document pass produced these changes.

| Reviewer finding | Change to the plan |
|---|---|
| Sol found that the voice store has one current conversation, that Kill(id) cannot enforce exact retirement, and that the Gateway advances a receive cursor independently of application persistence. | Use discussion-private stores/endpoints and an atomic guarded retire operation. Document transport versus durable cursors, missed-event policy, replay and burst/crash tests. |
| Astra found that worker wakes have no fresh human authority, native questions use a separate answer path and the proposed stop could leave a manager resident indefinitely. | Inherit only the original job grant, correlate attempts, bind ordinary team replies to the exact pending question, and make parked worker observation and question recovery independent of a native process. |
| Both identified global worker/tool exposure, partial history masquerading as empty, and conflicting treatment of missed vs fresh inputs. | Filter at tool dispatch, handle effective history permissions and enforce the split between live admission and context-only repair. State the same-UID trust boundary. |

Sol and Astra re-read the revised plan and evidence. Astra approved the revised architecture. Sol confirmed the implementation blockers were addressed and requested an explicit parent-channel low-water mark and pre-rollout review of real provider data use. Both clarifications are incorporated. Their sign-off applies to implementation planning.

## Remaining empirical gates

Production two-human admission and conversational quality, new native capability qualification on the actual home VM, long-context access with effective permissions, independent discussion isolation, manager retirement/reopen, lost-ACK recovery across both ledgers and delayed child reporting require the implementation. Measure cold and warm response latency, wake-to-result delay, idle process/model counts and the number of native effects in the real rehearsal. Mock receipts, a bot echo or an empty thread cannot substitute for a human-input end-to-end run.

## Scoped worker result and send qualification

| Worker path | Native evidence | Availability |
|---|---|---|
| Codex fresh worker | Match the first rollout turn's exact authorized input and a terminal event with that native turn ID. Codex 0.170.0 wrote `item_completed.UserMessage` followed by `task_complete` in an isolated local Responses probe and the scoped Linux rehearsal worker. | A verified transcript can report a result when the app-server journal join is unavailable. Resumable 8 MiB reads verify every record up to a 64 MiB evidence limit. |
| Codex follow-up | Queue the immutable operation and bind it to the existing job, attempt, worker instance and project. Once the exact worker is idle and its native input route is available, retain the native turn receipt and verify the matching rollout input and terminal event. | Queued locally while busy. Delivery requires the native app-server input route. An unsupported join can leave that worker read-only. A queued operation expires at the native semantic replay limit if readiness never becomes verifiable. |
| Claude Code fresh worker | Match the first exact prompt in the verified JSONL transcript and follow its mainline UUID ancestry through attachments and tool results to a terminal assistant message. An isolated interactive 2.1.326 probe with a loopback provider produced `promptId`, mainline attachments and an assistant `end_turn`. | Fresh-worker read only. The home VM’s installed Claude version still requires a live worker rehearsal. |
| Claude Code follow-up, OpenCode, agy and Hermes workers | The Discord job path has no qualified exact native turn receipt for these operations. | Unavailable on this surface. |
