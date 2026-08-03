# swarm-publish / internal/play -- TDD evidence

The failing-first run and its confirming green run for `cmd/swarm-publish` and
`internal/play` (GG-5: the red run is evidenced, not asserted).

| File | What it is |
| --- | --- |
| `publish-red.txt` | Tests written, no implementation. Every failure is an undefined symbol (`Publish`, `Config`, `run`) -- red for the RIGHT reason, not a syntax error in the tests. |
| `publish-green.txt` | The same tests after implementation, `-race`, 19 passing. |

## What these tests do and do not establish

They model the Play Developer Publishing API v3 PROTOCOL: the call sequence
(edits -> upload -> track -> commit), the RS256 assertion (verified against the
matching public key), the `completed` release status, the duplicate-versionCode
verdict, and that `--dry-run` never commits.

They establish NOTHING about whether Google accepts any of it. Every request goes
to a loopback `httptest.Server`, every key is generated in-process, and the
service account has not been granted Play access. This has never been run against
the real API, and a green run here is not evidence that a bundle would publish.
