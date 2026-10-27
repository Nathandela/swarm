# Copied source

`bubbletea/` is the production source of `charm.land/bubbletea/v2` v2.0.2.
`../testutil/teatest/` is the test helper from
`github.com/charmbracelet/x/exp/teatest/v2` v2.0.0-20260713092006-0d683c34c74b.
Their complete MIT licenses remain beside the copies and in the distributed
`THIRD_PARTY_NOTICES` file.

Bubble Tea carries the small renderer stop/join fix from upstream
[PR #1781](https://github.com/charmbracelet/bubbletea/pull/1781), commit
`5589b49965b303992cd18ac0f801d78cb6b2802b`, adapted to v2.0.2. Each renderer start
owns an exit channel; stop joins it before another start can reset the ticker.
This covers ordered terminal handoffs, not arbitrary concurrent lifecycle calls.
The teatest helper differs only in its Bubble Tea import path.

The copies are ordinary packages in this module. Do not add nested `go.mod`
files, a `vendor` directory, or a module `replace`: those break the supported
downloaded-module `go install ...@version` path. `provenance.json` pins upstream
and copied source bytes; `TestCopiedSourceProvenance` checks drift and inventory.
The outside-checkout module ZIP install test checks the actual installation path.

Relocation removes Bubble Tea's canonical module identity from `govulncheck`.
These source pins establish provenance, **not CVE cleanliness**. Review upstream
Bubble Tea and teatest advisories when changing dependencies or preparing a
release, and retain that review in the release evidence. Keep their original
module names and versions in this manifest for advisory lookup.
The [current public-advisory review](advisory-review-2026-10-27.json) records its
sources and limits. Follow-up `h6sc.26.1` tracks repeated review and removal.

shortcut: copied source lacks canonical vulnerability-scanner identity; review
advisories before every release and remove it after upstream qualification
(`h6sc.26.1`).

Remove the copies and restore upstream imports after an official fixed release
passes the unchanged terminal-restore regression under GOMAXPROCS=1 and `-race`,
plus the production attach/login handoff tests. The original regression and
stop/join lifecycle test must remain effective while the copy is carried.
