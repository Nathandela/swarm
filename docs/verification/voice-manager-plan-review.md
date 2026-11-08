# Voice manager implementation-plan review

2026-11-08 · Planning only. No implementation, hardware test or release certificate.

Subject: [implementation plan](../specifications/voice-manager-plan.md) and [ADR-038](../adr/ADR-038-voice-manager.md). Owner decisions: locked-screen voice; SSH first; both Codex/Claude selectable; unrestricted manager; BYO Gradium; reuse existing app/design.

Specialist discovery inspected Android UI/lifecycle/shared Go boundary, native Android/iOS audio/SSH and both native manager paths. Incorporated findings: separate voice service from Activity/relay lifecycle; ordinary shim-owned managers; no second Codex thread; exact Claude mod qualification; no tool-isolation claim for unrestricted manager; actual iOS binding/device gate.

## Review rounds and disposition

Three specialist agents independently read the complete written plan and ADR, then re-reviewed the revised files: Sol for app/control/source reach, Sol for native runtimes and speech composition, Astra for native devices/security/UX. Each reviewed the owner's whole requirements as well as its specialist area. They did not edit the plan; the root integrated the corrections.

| Finding | Resolution in the implementation plan |
|---|---|
| Imported SSH key could be shared with unrelated clients | Enrollment records exact ownership. Revoke app grant immediately; remove only enrollment-owned key entries. Shared key removal/rotation is separate and explicit; grant revocation is not account revocation. |
| Utterance identity was too late for lost finalize/admission ACK | C4 allocates/persists client ID before begin/audio; service acknowledges begin, and reconnect queries the same original ID. Pre-commit timeout differs from uncertain post-commit state. |
| Voice-device events were conflated with daemon journal replay | C2 and section5 define distinct voice stream ID/sequence and daemon journal binding/cursor, with gap snapshots and reconciliation. |
| Stop speaking only dropped old audio, allowing later same-turn publication to speak again | C7 revokes that producer turn's speech permit; later publication remains text-only and cannot acquire a new permit. |
| Manager ask and native permission request had incompatible lifetimes | C8 defines separate kinds. Manager clarification can survive its native turn completing; true native requests expire with provider resolution/turn/generation. |
| Seven-day receipt cleanup could reopen old action IDs | Durable input admission namespaces and contiguous retired floor survive compaction; old/unknown namespaces refuse new effects; unresolved holes retain guards. |
| Current-turn interruption did not cancel admitted Claude queued input | Keep local queue before admission; report native queued input honestly. Qualify revoke/start fencing or retain pending work and block automatic replacement until settled. No invented cancel API. |
| Event checkpoint could advance before a durable manager wake existed | Singleton generation fencing; transactional accepted event/inbox/cursor; durable prepared/admitted/native-input/settled wake state and exact receipt reconciliation. |
| Voice-off zero-model rule contradicted persistent orchestration | Zero speech and polling-only inference; authorized task events can still wake the manager. Explicit End→worker completion test. |
| Locked offline End was not a firm platform gate | Android notification End; qualified public iOS remote command path or design blocker. End works locally without ASR/network and stops mic as well as playback. |
| Android declarations and silenced recorder were under-specified | Explicit microphone/playback service-type audit, audio focus/denial, recording silenced/privacy states, and PendingIntent/exported-component validation. |
| Android reuse lacked exact change reach | P9 identifies Activity/Surface/LifecycleLane/AppLifecycle, relay-specific constructor, permission maps, token/kit registries, policy tables and binding provenance. |
| P0 combined independent prerequisite gates | P0-R/P0-S/P0-N close independently; native/runtime work is not blocked by unrelated provider key/phone access. |
| “Both connection options” could disappear into vague deferral | Concrete HTTPS follow-on objective after SSH, with auth/deployment and both-manager/locked-device acceptance; no hosted dependency added to SSH milestone. |

All three reviewers approved the revised document **as an implementation plan**, with no remaining material blocker in their review scopes. Approval does not certify an implemented runtime, locked device or release.

Reviewed SHA-256:

- `docs/specifications/voice-manager-plan.md`: `93badc1940a54b97d0141e4970bbfe8671e5abc99de793f0fe6d6dc489235563`
- `docs/adr/ADR-038-voice-manager.md`: `eb6891371f47716983f18614d285e1cd0fb7c937dfb0fe5be5202f3df7eae2df`

## Sources and validation

Specialists used Context7 followed by first-party platform/native documentation and installed/source inspection. Android source confirms background-disconnect/no-microphone-FGS behavior; iOS source/build is absent. Pinned x/crypto SSH channels lack forwarded read/write deadline support. Installed Claude help and existing native mod allowlists were compared; ordinary native sessions were selected over an unqualified SDK/account substitution. Relevant source links and exact file reach are in the plan.

The official [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk) was also checked through Context7 and its primary repository: typed tool handlers and stdio lifecycle support the proposed bridge. P2 still pins/reviews its selected dependency version and tests native-client compatibility; no SDK is added in this planning change.

Local document checks verify unique V01–V30 and P0–P12 identifiers, local links, reviewed file hashes and whitespace. Beads carries actual execution state and dependency checks. No production code changed; build/vet/lint/race and physical tests were not run for this documentation-only revision. Prior research tests remain in their original evidence package and are not relabelled as tests of this plan.

`bd dep cycles` reported no dependency cycles after materialization. Execution references (all under `agents-tracker-uv6m`):

| Plan unit | Beads suffix |
|---|---|
| P0 qualification container | `.6` |
| P0-R runtime / P0-S speech / P0-N native | `.6.1` / `.2` / `.6.2` |
| P1 shared actions / P2 controller | `.7` / `.8` |
| P3 Codex / P4 Claude / P5 text slice | `.9` / `.10` / `.3` |
| P6 speech / P7 desktop | `.11` / `.12` |
| P8 shared mobile / P9 Android / P10 iOS | `.13` / `.14` / `.15` |
| P11 whole-system quality / P12 release | `.16` / `.4` |
| HTTPS follow-on | `.17` |

The original coarse `.3` and `.4` records were rewritten into P5/P12; no obsolete restricted-manager or foreground-only acceptance remains in those execution records. Core decision `.1` is resolved; the planning task is `.5`. Implementation records remain open.

Implementation prerequisites remain explicit: fresh Gradium key/account characterization, real native manager versions/account behavior, actual Android/iPhone devices, iOS signing/binding/distribution, acoustic/locked-network tests and installed-release provenance. The user offered a key once the provider console is available; no new key was used here.
