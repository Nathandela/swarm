# M0 inventories

These ledgers supplement `reference-surfaces.tsv` (dispatcher arms and aliases) and
`reference-board-keys.tsv` (board gestures). They are discovery indexes for the pinned
Go reference, not claims that a Rust implementation or live run has passed parity.

`generate.py` reads the immutable M0 pin `f26648d69086e329132ac7849954d2f29a895387`
and extracts standard Go `flag` registrations in command binaries, including custom
`Var` values, typed registrations, and each `FlagSet`'s implicit `-h`/`-help`. It annotates each row with disposition and an evidence owner. Dynamic
public parsing is indexed separately in `manual-operations.tsv`; provider-owned CLI
flags and registrations outside `cmd/swarm` still require provider-specific capture.
Scenario rows use file line ranges or Go symbols (not synthetic Markdown row anchors).
M0 identifies the oracle, owner, disposition and observation for each retained case. The implementing milestone runs that case against the candidate. Physical-host qualification and sustained live trials belong to the release gate.

| Ledger | Coverage |
|---|---|
| `flags.tsv` | Literal flag registrations found under `cmd/**` command binaries (generated). |
| `manual-operations.tsv` | Public positionals, nested operations and internal command protocols not described by flag registration. |
| `screens.tsv` | TUI destination screens and shared overlay/form controls to walk. |
| `env-config-artifacts.tsv` | Runtime environment contracts and durable/local artifacts. |
| `scenarios.tsv` | Positive, refusal, unavailable, and recovery scenarios with an owner and source anchor. |
| `captures.tsv` | Scenario-to-test mapping, pinned/current test presence, targeted outcomes, fleet-log observations, and open live gates. |

Run `python3 docs/specifications/rust-migration/inventory/generate.py --check` from the
repository root. The output intentionally records source anchors rather than duplicating
all behavior text from the specification.

`captures.tsv` distinguishes a named test's existence from a passing execution and a
package-level log result from a live qualification. The two fleet logs do not contain
their invoking command or a pinned Go build; their stack paths identify the working
checkout. That checkout was `5863d844dee9fa30119f63b3a73bfdc934be4c52`; Go source/test
diffs from the M0 pin were limited to dispatcher-guide and checkout-test changes. The
targeted command and its exact package outcomes are recorded in the `RUN_01` row. No
fleet run is an all-green result, and no row claims the Rust candidate has passed.
