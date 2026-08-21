# Committee fix wave round 4 -- release tag publication gated on the container lane

Date: 2026-08-21. Codex round-3 blocker 2: `.github/workflows/release.yml` gated
publication only on ci.yml (`needs: gates`); `relay-container.yml` had no
`workflow_call` trigger and no tag trigger, and the repository ruleset covers only
`refs/heads/main` -- so a release tag could publish without the relay image ever being
built, scanned (trivy HIGH/CRITICAL), non-root-asserted, or compose-hardening-asserted
for that tag.

## Absence proof (before the fix)

    $ grep -n "relay-container" .github/workflows/release.yml
    exit=1                                  # no reference at all
    $ grep -n "needs" .github/workflows/release.yml
    30:    needs: gates                     # ci.yml is the ONLY publish gate
    $ grep -n "workflow_call\|tags" .github/workflows/relay-container.yml
    exit=1                                  # not callable, and no tag trigger
    # relay-container.yml on-block: push(branches:'**'), pull_request, schedule,
    # workflow_dispatch -- nothing a `v*` tag push activates.

The ruleset scope (refs/heads/main only) is codex's finding about the GitHub-side
branch protection; it is what makes the workflow-file gap the whole story for tags.

## The fix

- `relay-container.yml`: added `workflow_call:` to the `on:` block (with a comment
  naming why), making the lane reusable. Nothing else in the workflow changed; the
  existing structural gate test over its steps
  (deploy/relay/hardening_test.go TestRelayContainerWorkflowWiresTheGates) still passes.
- `release.yml`: added a `container` job (`uses: ./.github/workflows/relay-container.yml`,
  the same reuse pattern as the existing `gates` job) and widened the publish gate to
  `needs: [gates, container]`. ci.yml untouched.

## Validation (after the fix)

`python3` + `yaml.safe_load` over both files (note: YAML 1.1 parses the bare `on` key
as boolean `true`; the check accepts either key):

    relay-container.yml: yaml.safe_load OK; on-keys = ['pull_request', 'push',
        'schedule', 'workflow_call', 'workflow_dispatch']
    release.yml: yaml.safe_load OK; publish.needs = ['gates', 'container']
    release.yml: container job uses = ./.github/workflows/relay-container.yml

Asserted structurally in the same script: `release.yml` still triggers on `v*` tags,
`jobs.gates.uses` is unchanged (`./.github/workflows/ci.yml`), `jobs.container.uses`
names the container lane, and `jobs.publish.needs` lists BOTH. A failed container lane
now fails the `publish` job's `needs`, so `goreleaser release` never runs for that tag.

    $ go test ./deploy/relay/ -run TestRelayContainerWorkflow -count=1
    ok      github.com/Nathandela/swarm/deploy/relay        0.667s
