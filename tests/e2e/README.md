# End-to-end tests

Two parts: the automated suite (below) and the recorded manual runs that preceded it.

## Automated suite

Go tests behind the `e2e` build tag in `tests/e2e/`. They exec a `strspc` binary built from a
chosen ref and assert through the real GitHub client against the `testbed-multi-org`
organization. `go test ./...` never runs them (no tag).

### How to run

The `e2e-test.yml` workflow runs automatically on every `develop` → `main` promotion PR
(head `develop` in this repo), against the PR head SHA. The repo ruleset `required-e2e` makes
its `e2e` check required on `main`, so nothing ships untested. Other PRs into `main`, such as
release-please's, skip the job, and a skipped job passes the check. Runs start without an
approval: the `e2e-testbed` environment has no protection rules.

For ad-hoc runs, dispatch the workflow with a `ref` input: a branch, tag or SHA of this repo.

Either way the workflow builds `strspc` from that ref, runs
`go test -tags e2e ./tests/e2e/... -count=1`, and always runs a cleanup step. Runs are
serialized by the `e2e-testbed` concurrency group (`cancel-in-progress: false`).

Required secrets, org-level, with `strspc-sync` in their repository scope:

- `STEERSPEC_APP_ID`
- `STEERSPEC_PRIVATE_KEY`

The PAT scenario `TestSyncPATCommitsSigned` (#162) runs sync with `auth.method: github-token` and a user's token
instead of the App, and checks that the PRs and the state commit are opened by that user and verified. It runs only
on a dispatch with `pat: true`, which passes the org secret `BOT_TESTBED_GITHUB_TOKEN` (a `cnslr-bt` token) as
`E2E_PAT_TOKEN`; promotion runs skip it. The token must reach `testbed-multi-org`, with Contents and Pull requests
write on `strspc-e2e-central` and the sync targets: a fine-grained PAT owned by another organization only sees this
one's public repositories.

The GitHub App installation token is used for sync itself and for injecting drift. Writes (sync
and the harness) go through `CreateOrUpdateFile`, which commits with the GraphQL
`createCommitOnBranch` mutation (#126), and the harness deletes files with the same mutation:
GitHub signs those commits, so they pass the Signed Commit ruleset. The Contents API `DELETE` is
not used because its App commits are unsigned and the ruleset rejects them with a 409 (#108).
The forced-sync scenario checks that every sync PR commit is verified. A new testbed should be
smoke-tested for a write and a delete.

### Testbed repos

| Repo | Visibility | Role |
| --- | --- | --- |
| `strspc-e2e-central` | internal | Templates, config and deployment state. Sync PRs never target it. |
| `strspc-e2e-public` | public | Target; gets the central source for `sync-visibility`. |
| `strspc-e2e-private` | private | Target; gets the `strspc-e2e-src` source for `sync-visibility`. |
| `strspc-e2e-src` | private | Second source repo for the `owner/repo:path` form. Not a target. |
| `strspc-e2e-symlink` | private | Target whose `CLAUDE.md` is a symlink, seeded once by a signed commit (#82). |
| `strspc-e2e-develop` | private | Target whose default branch is `develop`. |

### Fixtures

`tests/e2e/fixtures/` holds the files seeded into the testbed:

| File | Seed into |
| --- | --- |
| `steerspec-sync.yml` | `strspc-e2e-central` (the harness copies it per run) |
| `templates/basic.mustache` | `strspc-e2e-central:templates/basic.mustache` |
| `templates/visibility.public.mustache` | `strspc-e2e-central:templates/visibility.public.mustache` |
| `src-repo/templates/visibility.private.mustache` | `strspc-e2e-src:templates/visibility.private.mustache` |

The fixture config uses short stable template ids: `sync-basic` (destination `CLAUDE.md`,
mustache, every target) and `sync-visibility` (destination `.steerspec-e2e/visibility.md`,
source fallback plus `sources:` for `public` and `private`). When the harness copies the config
it rewrites every `id:` to `e2e-<run_id>-<id>`, so the PR branches are
`steerspec-sync/e2e-<run_id>-sync-basic/1.0.0` and so on. Tests must use the rewritten ids.

Variables the templates read: global `org_name`, `e2e_marker`; per template `template_name`.
Rendered content is distinguishable: `BASIC-SOURCE` (basic), `PUBLIC-SOURCE` (central
visibility source), `PRIVATE-SOURCE` (`strspc-e2e-src` visibility source).

### One-time setup (Wave 0, repo owner)

1. Create the repos in the table above with the stated visibility, each with an initial commit
   on its default branch.
2. Set the default branch of `strspc-e2e-develop` to `develop`.
3. In `strspc-e2e-symlink`, commit `CLAUDE.md` as a symlink (git mode `120000`, any target such
   as `AGENTS.md`, which must also exist) with a signed `git commit` and push it. The Contents
   API cannot create symlinks.
4. Seed the fixture files from the table above into `strspc-e2e-central` and `strspc-e2e-src`.
5. Install the SteerSpec App on all testbed repos, and add it as a bypass actor of the ruleset
   on `strspc-e2e-central` `main`, so sync can commit its deployment state.
6. Add `strspc-sync` to the repository scope of the `STEERSPEC_APP_ID` and
   `STEERSPEC_PRIVATE_KEY` org secrets.
7. Create the `e2e-testbed` environment in `strspc-sync` (Settings, Environments), with no
   protection rules. The workflow runs the dispatched ref's tests with the App key, and the
   secrets are org secrets that any workflow of this repo can read, so the trust boundary is write
   access to `strspc-sync` and PR review on `develop`, not an environment approval.
8. Give `strspc-e2e-develop`, and no other repo in the org, the topic `strspc-e2e-topic`
   (`TestSyncTargetSelection` selects it by topic and fails with this step's reference if it is
   missing).

### Isolation and cleanup

- Each test rewrites template ids to `e2e-<run_id>-<test>-*` (so a PR one scenario closes never
  shows up in another's listings, while `TestCleanupRun` still sweeps the whole `e2e-<run_id>-`
  prefix) and `harness.New` resets the state file, so
  runs and the earlier recorded runs on `strspc-central` / `strspc-test-repo-*` cannot collide.
- Files a scenario changes through `WriteFile`, `PutFile` or `DeleteFile` are snapshotted first
  and restored by cleanup. A process killed mid-run cannot restore them (the baseline lives in
  memory), so each scenario starts by `ResetFile`-ing every file it depends on.
- Cleanup runs in `t.Cleanup` and in an `if: always()` workflow step (`TestCleanupRun`, enabled
  only by `E2E_CLEANUP_ONLY=true`): restore changed files, reset the deployment state file to
  empty (the central repo is dedicated to the suite, so empty is its baseline, and this also works
  in the cleanup-only process), close PRs, delete `steerspec-sync/e2e-<run_id>-*` branches, close
  open issues that mention the run's template ids or carry the `steerspec-drift` /
  `steerspec-conflict` label (conflict issues are titled by type and path, so they carry no run
  marker; runs are serialized, so any such open issue is a leftover). After a green run the testbed
  has no open PRs or issues and no `e2e-*` branches.

### Scenarios

- Sync: a forced sync opens one PR per target with branch `steerspec-sync/<id>/<version>`,
  expected file content and PR body and only verified (signed) commits (#126);
  deployment state is committed; a rerun opens nothing (#94);
  a closed-unmerged PR is reported (#91); a no-op rerun writes no state commit (#100).
- Monitor: drift injected with an App commit gives one issue; a rerun adds none
  (fingerprint); reverting the drift closes it.
- Conflict: tier 1 and tier 2 conflict issues open and auto-close.
- Edge cases: the symlinked `CLAUDE.md` is skipped with a reason (#82); `develop` default
  branch is honored; per-visibility sources (#80): public targets get the central source,
  private targets get `strspc-e2e-src`, and only the private PR links it.
- Lifecycle (#156): a template edit at the same version updates the open PR on its branch; a
  version bump opens a PR on the new branch, reports the previous version and closes the stale
  PR (`close-stale-prs`); a merged sync PR is recorded as `merged`, and neither a plain nor a
  forced rerun opens anything.
- Strategies (#156): `marker` replaces only the managed section and keeps the repo's content
  byte for byte, appending a section the file lacks; `full-replace` overwrites the file with
  `{{var}}` substitution.
- Target selection (#156), from the config alone: include glob plus explicit include, topic,
  exclude glob, and overrides (an override variable, `exclude_templates`, `include_templates`)
  yield exactly the expected (repo, template) pairs.

### Recorded automated runs

| Run | Ref | Result |
| --- | --- | --- |
| [40766051379](https://github.com/SteerSpec/strspc-sync/actions/runs/40766051379) | `main` | First live run: failed. The App's Contents API `DELETE` is unsigned and the enterprise signature rule rejected cleanup, so leftover files made four sync scenarios skip. Fixed by deleting through GraphQL (#108). |
| [40769125396](https://github.com/SteerSpec/strspc-sync/actions/runs/40769125396) | `fix/e2e-signed-delete` | All 10 scenarios green, testbed clean afterwards. |
| [40773218378](https://github.com/SteerSpec/strspc-sync/actions/runs/40773218378) | throwaway branch with the in-sync skip (#94) disabled | Mutation check: `TestSyncAlreadyCurrentOpensNothing` went red (a PR was opened for current content), the other 9 stayed green. |

The mutation check is the proof that the suite can fail: after changing sync behavior, break the
guard under test on a throwaway branch and dispatch against it.

## Recorded manual runs

Recorded runs of the released action against the `testbed-multi-org` organization.
The central repo is `testbed-multi-org/strspc-central` (`auth.method: github-app`,
one `claude-md` template, targets `strspc-test-repo-1..3`). Each row links the
GitHub Actions run that produced the numbers.

## 2026-10-20: v0.2.1, GitHub App auth

Workflows in the central repo: `sync.yml`, `monitor.yml`, `conflict.yml`, all
pinned to `SteerSpec/strspc-sync/<cmd>@v0.2.1` with `app-id` / `private-key`
from org secrets.

| Step | Run | Result |
| --- | --- | --- |
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

Follow-ups opened from this run: #51 (empty `token` input, fixed in v0.2.2), #55
(conflict issues are never closed) and #56 (`pr_status` stays `open` in the
deployment state after the PR merges).

Notes for the next run:

- Target repos under the enterprise signature rule reject the REST contents API
  (`Commits must have verified signatures`); inject drift with a signed git
  commit instead.
- `conflict` exits 1 whenever a critical finding exists, so a scheduled run
  shows red until the conflict is resolved.
