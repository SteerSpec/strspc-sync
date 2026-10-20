# End-to-end runs

Recorded runs of the released action against the `testbed-multi-org` organization.
The central repo is `testbed-multi-org/strspc-central` (`auth.method: github-app`,
one `claude-md` template, targets `strspc-test-repo-1..3`). Each row links the
GitHub Actions run that produced the numbers.

## 2026-10-20: v0.2.1, GitHub App auth

Workflows in the central repo: `sync.yml`, `monitor.yml`, `conflict.yml`, all
pinned to `SteerSpec/strspc-sync/<cmd>@v0.2.1` with `app-id` / `private-key`
from org secrets.

| Step | Run | Result |
|---|---|---|
| Forced sync (`force: true`) with the three sync PRs from 2026-08-21 still open | [39408576963](https://github.com/testbed-multi-org/strspc-central/actions/runs/39408576963) | `prs_updated: 3`, `errors: 0`. PR titles became `chore(steerspec-sync): update claude-md to 1.0.0`, bodies carry previous/new version and content hash. |
| Re-sync, no force | [39408821143](https://github.com/testbed-multi-org/strspc-central/actions/runs/39408821143) | `repos_skipped: 3`, `errors: 0` |
| Monitor with the sync PRs still open | [39418427868](https://github.com/testbed-multi-org/strspc-central/actions/runs/39418427868) | `repos_drifted: 3`, `issues_created: 3` in the central repo; repo-3 reported as `(file not found)` because it had no `CLAUDE.md` yet |
| Conflict, tiers 1,2, same state | [39418451284](https://github.com/testbed-multi-org/strspc-central/actions/runs/39418451284) | `critical: 2` (`version-drift` in repo-1 and repo-2, issues opened in the targets), job exit 1 on critical by design; repo-3 skipped (no file) |
| Three sync PRs merged by the owner, monitor again | [39422911108](https://github.com/testbed-multi-org/strspc-central/actions/runs/39422911108) | `repos_in_sync: 3`, `issues_closed: 3` |
| Conflict on the in-sync state | [39423008365](https://github.com/testbed-multi-org/strspc-central/actions/runs/39423008365) | `critical: 0`, exit 0. The two conflict issues from the earlier run stayed open (#55) |
| Drift injected in repo-1 (signed commit appending a comment to `CLAUDE.md`), monitor | [39423287454](https://github.com/testbed-multi-org/strspc-central/actions/runs/39423287454) | `repos_drifted: 1`, `issues_created: 1` |
| Monitor again, nothing changed | [39423322446](https://github.com/testbed-multi-org/strspc-central/actions/runs/39423322446) | `repos_drifted: 1`, `issues_created: 0` (idempotent) |
| Drift reverted, monitor | [39423452786](https://github.com/testbed-multi-org/strspc-central/actions/runs/39423452786) | `repos_in_sync: 3`, `issues_closed: 1` |

Not exercised here: the `github-token` auth path. It was run the same day from
`cashflytic/cashflytic-spec` on v0.2.1 and v0.2.2 in dry-run mode; see #51 for
the empty-token failure that produced v0.2.2.

Follow-ups opened from this run: #51 (empty `token` input, fixed in v0.2.2),
#55 (conflict issues are never closed), #56 (`pr_status` stays `open` in the
deployment state after the PR merges).

Notes for the next run:

- Target repos under the enterprise signature rule reject the REST contents API
  (`Commits must have verified signatures`); inject drift with a signed git
  commit instead.
- `conflict` exits 1 whenever a critical finding exists, so a scheduled run
  shows red until the conflict is resolved.
