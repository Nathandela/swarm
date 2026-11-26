# Recursive Swarm worktree placement

The investigation started at upstream `7f67c067` on Git 2.39.3 (Apple Git-146). The reported path was the template for a disposable fixture. Its on-disk Git identities were unknown. No existing repositories, worktrees, or running sessions were used in the fixture.

## Cause and placement rule

`worktree.Create` used the launch cwd to select both the start commit and the destination. A launch from a linked checkout therefore created the next checkout below its source. The same mechanism placed worktrees under any ordinary subdirectory passed as cwd. Resolving only `git rev-parse --show-toplevel` fixes the subdirectory case, while a linked checkout still reports itself as the top level.

The candidate resolves the source's Git common directory and selects a stable worktree root for that repository. For ordinary repositories the root is `<primary>/.swarm/worktrees`. A bare-backed or separate Git directory can serve as the stable anchor when Git lists the metadata directory first. The Git command still starts the branch at the source checkout's `HEAD`. The source cwd and the actual provider cwd remain separate.

Relative location, matching remote URL, and shared commit IDs do not establish repository ownership. A separate clone under an ordinary primary checkout managed its own worktrees. The candidate refused new destinations physically inside any linked checkout in the test, including a distinct nested repository and a symlink redirect into a linked checkout. The owner can use the independent repository from a location outside the enclosing linked checkout to obtain an isolated worktree.

## Reproducing the shared worktree spike

Build the following probe against both the upstream baseline and the candidate source. Use absolute source paths and a writable disposable directory. The probe executes the same API as the supervisor's session worktree hook.

```sh
probe_dir=$(mktemp -d)
cat > "$probe_dir/main.go" <<'GO'
package main

import (
    "fmt"
    "os"

    "github.com/Nathandela/swarm/internal/worktree"
)

func main() {
    fn := worktree.Create
    if os.Args[1] == "reserve" {
        fn = worktree.ReservedCheckout
    }
    result, err := fn(os.Args[2], os.Args[3], os.Args[4])
    if err != nil {
        fmt.Fprintln(os.Stderr, err)
        os.Exit(1)
    }
    fmt.Println(result)
}
GO
build_probe() (
    cd "$probe_dir" || exit
    cat > go.mod <<EOF
module github.com/Nathandela/swarm/identityprobe

go 1.26

require github.com/Nathandela/swarm v0.0.0
replace github.com/Nathandela/swarm => $1
EOF
    go build -mod=mod -o "$2" .
)
build_probe "$baseline_source" before
build_probe "$candidate_source" after
```

Run the fixture in a fresh directory for each binary. The following commands reproduce the same-repository cases and construct the observed legacy path shape. The `.scratch` checkout in this fixture is linked to the same repository.

```sh
probe="$probe_dir/before"  # Repeat with after and a fresh fixture.
fixture=$(mktemp -d)
repo="$fixture/root"
mkdir -p "$repo/area"
git -C "$repo" init -q
commit() {
    git -C "$1" -c user.name=Probe -c user.email=probe@example.test \
        commit --allow-empty -qm "$2"
}
commit "$repo" main-seed
first="$repo/.swarm/worktrees/rehan-review"
integration="$first/.scratch/rehan-cutover-integration-20261125"
middle="$integration/.swarm/worktrees/fii-rehan-performance"
deep="$middle/.swarm/worktrees/rehan-develop-and-elvira-reconciliation-legacy-cd"
git -C "$repo" worktree add -qb legacy-first "$first"
git -C "$first" worktree add -qb legacy-integration "$integration"
git -C "$integration" worktree add -qb legacy-middle "$middle"
git -C "$middle" worktree add -qb legacy-deep "$deep"
git -C "$repo" worktree add -qb external "$fixture/external-linked"
commit "$deep" deep-only
commit "$fixture/external-linked" external-only
mkdir -p "$deep/canvas"
touch "$deep/never-copy-dirty"
ln -s "$middle" "$fixture/alias"

inspect() {
    printf 'cwd=%s\n' "$1"
    (cd "$1" && pwd -P)
    git -C "$1" rev-parse --show-toplevel
    git -C "$1" rev-parse --path-format=absolute --git-common-dir
    git -C "$1" rev-parse --short HEAD
}
try_launch() {
    inspect "$1"
    chosen=$("$probe" create "$1" "$2-id" "$2") || return
    printf 'chosen=%s\n' "$chosen"
    inspect "$chosen"
    "$probe" reserve "$1" "$2-id" "$2"
}
try_launch "$repo" session-parent
try_launch "$chosen" session-child
try_launch "$chosen" session-grandchild
try_launch "$repo/area" subdirectory
try_launch "$fixture/alias" alias
try_launch "$fixture/external-linked" linked-external
try_launch "$deep/canvas" legacy-deep
test -f "$deep/never-copy-dirty"
test ! -e "$chosen/never-copy-dirty"
git -C "$repo" worktree list --porcelain
```

For the remaining controls, repeat `try_launch` using the directories below. Add a subdirectory named `area` to each clone or freshly initialized repository before calling it.

- `git clone -q "$repo" "$integration/clone"` and `git init -q "$integration/independent"` (followed by `commit`) exercise distinct identities inside the linked checkout. `git clone -q "$repo" "$repo/research/nested"` exercises a distinct identity inside the main checkout. The candidate was also invoked from `research/nested/area` with `GIT_DIR="$repo/.git" GIT_WORK_TREE="$repo"`.
- `git clone -q --bare "$repo" "$fixture/bare.git"` followed by `git -C "$fixture/bare.git" worktree add -qb bare-source "$fixture/from-bare"` tests a bare-backed source. In `$fixture/separate`, run `git init -q --separate-git-dir "$fixture/separate.git"` and `commit` to test a split Git directory.
- In a separate ordinary repository `unsafe` with one commit, register a linked checkout at `unsafe/older/managed` and symlink `unsafe/.swarm/worktrees` to that checkout. The test ran `create` from `unsafe` and inspected both the reported and physical destinations.

## Recorded results

`R` is the physical main checkout, `W` is `.swarm/worktrees`, `I` is the deeply nested `.scratch` checkout, `M` is `I/W/fii-rehan-performance`, `D` is the deepest legacy worktree, and `E` is the external linked checkout. Paths in the placement columns are physical. The final component is the chosen launch name.

| Requested cwd → physical cwd | Git common directory | Upstream placement | Candidate placement |
| --- | --- | --- | --- |
| `R` | `R/.git` | `R/W/session-parent` | `R/W/session-parent` |
| Parent checkout | `R/.git` | `R/W/session-parent/W/session-child` | `R/W/session-child` |
| Child checkout | `R/.git` | `R/W/session-parent/W/session-child/W/session-grandchild` | `R/W/session-grandchild` |
| `R/area` | `R/.git` | `R/area/W/subdirectory` | `R/W/subdirectory` |
| Symlink `alias` → `M` | `R/.git` | `M/W/alias` (returned through `alias`) | `R/W/alias` |
| `E` | `R/.git` | `E/W/linked-external` | `R/W/linked-external` |
| `D/canvas` | `R/.git` | `D/canvas/W/legacy-deep` | `R/W/legacy-deep` |
| `I/clone/area` | `I/clone/.git` | `I/clone/area/W/nested-clone` | Refused `I/clone/W` inside `I` |
| `I/independent/area` | `I/independent/.git` | `I/independent/area/W/nested-independent` | Refused `I/independent/W` inside `I` |
| `R/research/nested/area` | `R/research/nested/.git` | Candidate-only control | `R/research/nested/W/isolation` |
| `from-bare` | `bare.git` | `from-bare/W/bare-linked` | `bare.git/W/bare-linked` |
| `separate/area` | `separate.git` | `separate/area/W/separate-git-dir` | `separate.git/W/separate-git-dir` |
| `unsafe` with `W` pointing into `unsafe/older/managed` | `unsafe/.git` | `unsafe/older/managed/redirected-root` | Refused redirected root |

The deep and external source checkouts had commits distinct from the primary. Both builds preserved their source `HEAD` at the destination and left the deep untracked sentinel with the source. The candidate left the legacy registrations in place, recovered each successful placement by its reserved branch, and allocated no directories for the three refusal cases. The baseline allocated the redirected destination but reservation recovery refused its physical path.

## Launch, ownership, and recovery

| Finding | Change and witness |
| --- | --- |
| Fresh TUI and CLI isolation, explicit voice worker isolation, dispatcher delegations, and both phone launch forms use the supervisor's shared checkout hook. | The hook separates placement from the source commit. Codex, Claude Code, agy, Hermes fresh launch, and OpenCode consume the resulting provider cwd. The stock Discord launch dialog and `pilot create` do not expose a worktree toggle. |
| First-class `swarm handoff` takes the recorded provider cwd and enables Git isolation. Document-based `swarm spawn` defaults to isolation in Git. | Both use the shared rule and check the physical destination before Git writes. The phone launch forms also check that destination against allowed roots. Hands-off TUI continuation, resume, reattach, and relogin reuse their recorded checkout. Scheduled isolated runs use a separate allocator under the state directory's sibling storage. |
| A parent can have registered descendants, and ignored directories can conceal independent Git checkouts. In controls, both forced removal of the linked parent and unforced removal of a checkout with an ignored dirty inner repo erased sentinel data. | Deletion verifies the exact path, owning repository, and session, refuses nested registrations and nested checkout metadata, and removes only the verified checkout. Inventory already reports physical registrations, ownership, and live or retained consumers. No inventory migration or worktree relocation was required. |
| Git can reuse the same path and administrative directory for a replacement. A flattened successor can outlive its source or detach from its original branch. | New checkouts record their owning session in the linked Git administrative directory. Teardown accepts canonical and legacy paths and refuses conflicting markers, duplicate registrations, or legacy ownership it cannot prove. A legacy checkout with no marker needs its session branch. An interrupted checkout recovered before a marker was written has the same rule. |
| A fallback name may already exist, and independent supervisors can race for the friendly name. | A raw Git control produced duplicate path registrations in 39 of 60 concurrent trials. Giving each process an atomic directory claim produced no duplicates in 60 trials. The implementation claims both friendly and fallback paths exclusively. The final check also refuses a destination redirected during policy validation. External Git commands do not participate in Swarm's claim. |

An unverified primary configured through `core.worktree` is refused. Ambiguous legacy checkout ownership, initialized nested submodules, and detectable independent nested checkouts are intentionally retained on explicit delete. The ordinary force-delete behavior still applies to dirty files belonging to the checkout the user explicitly deletes. If an incomplete worker reservation has lost its requested cwd, replay can still require that cwd. If Git created a checkout but the owner record could not be written, the registration and working files are retained and the launch reports the path.

## Native provider matrix

The binaries were built from the upstream baseline and the placement candidate. Each trial had its own Git repository and supervisor state, socket, lock, and log. Existing sessions and repositories were outside the fixtures. In the table, `R` is each fixture repository and `W` is `.swarm/worktrees`. Every chosen checkout had Git common directory `R/.git`. The first launch requested `R`, the second requested the parent checkout or its scratch subdirectory, and the third requested the child checkout.

| Provider and normal Swarm route | Upstream physical paths after the parent | Candidate physical paths after the parent | Native evidence |
| --- | --- | --- | --- |
| Codex 0.172.0 via `spawn --worktree --prompt`, `spawn --delegate`, and `handoff --supervision none` | `R/W/parent/W/child`, `R/W/parent/W/child/W/grandchild` | `R/W/child`, `R/W/grandchild` | All six authenticated turns reached `ready_for_review`. Native transcript cwd, `/bin/pwd -P`, and the final reply agreed. Child and grandchild started from their respective source-only commits. |
| Claude Code 2.1.329 via three `spawn --cli claude --worktree --prompt` calls, with child requested from `parent/.scratch/cutover` | `R/W/claude-parent/.scratch/cutover/W/claude-child`, then `W/claude-grand` under that child | `R/W/claude-child`, `R/W/claude-grand` | `pgrep -P` and `lsof -d cwd` identified the real Claude process and matched each saved provider cwd in both builds. The existing login was expired. No authenticated turn. |
| agy 1.4.7 via three `spawn --cli agy --worktree --prompt` calls | `R/W/parent/W/child`, `R/W/parent/W/child/W/grandchild` | `R/W/child`, `R/W/grandchild` | The real binary was identified in all launches. The shim received each recorded cwd. On both builds the CLI exited 2 with `flags provided but not defined: -settings` before an authenticated turn. |

The Codex trial used these entry points, with the local session IDs taken from the preceding launches and the context file outside the managed directories. For Claude and agy, repeat the initial `spawn` command with the previous checkout as `--dir`, the appropriate `--cli`, and a unique name.

```sh
swarm spawn --cli codex --dir "$repo" --name parent --worktree --prompt "$prompt"
(cd "$parent" && SWARM_SESSION_ID="$parent_id" swarm spawn --cli codex --name child --delegate "$context")
(cd "$child" && SWARM_SESSION_ID="$child_id" swarm handoff --cli codex --name grandchild --supervision none --context-file "$context")
```

The final binary SHA-256 was `c1c3d2ddb58c12df02d41721000d9681c8f5cc3ae12225e9cc3de99c83aa8700`. The final repeat reproduced the candidate outcomes for all three providers. In the final Codex run the parent, child, and grandchild had commits `3ad1313`, `58b49fb`, and `65f513a`. The VM `tf_nathan` rejected the available SSH identity. Remaining authenticated coverage is tracked in `agents-tracker-5ygz`.

## Repository checks

The Go build, vet, and lint gates passed. The entire worktree test package passed under the race detector. Focused race-enabled checks also passed in daemon, protocol, and skeleton, including both remote destination policy routes. A final CLI inventory run against the isolated provider fixture completed and listed all three sibling checkouts with their exact Swarm owners.

The first `go test ./... -timeout 35m` run encountered daemon startup and fixture deadlines and permission expectations under the launch shell's `0077` umask. Serial reruns with standard fixture permissions cleared those failures. The inventory test continued to hit its 15-second deadline from the launch checkout, which shares 195 Git registrations. An overlay restoring upstream `7f67c067` Go sources reproduced the same failure from the same directory. The final binary's test passed from a neutral disposable directory. The remaining suite issue is tracked in `agents-tracker-icxe`.
