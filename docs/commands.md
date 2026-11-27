# Commands

## sync

Render templates and open PRs in target repositories.

```bash
strspc sync --config steerspec-sync.yml
strspc sync --config steerspec-sync.yml --dry-run
strspc sync --config steerspec-sync.yml --target-filter "my-org/api-*"
strspc sync --config steerspec-sync.yml --template-filter claude-md --force
strspc sync --config steerspec-sync.yml --target-filter "my-org/api-*,my-org/web" --template-filter "claude-md,agent-*"
```

`--target-filter` (every command) and `--template-filter` (sync) take a comma-separated list of
[`path.Match`](https://pkg.go.dev/path#Match) globs. A target matches on its `owner/repo`, a template on
its ID, when any glob matches. `*` does not cross `/`, so a repository glob needs the owner part. A
malformed glob fails the command instead of matching nothing.

**Action usage:**

```yaml
- uses: SteerSpec/strspc-sync/sync@v0.4.1
  with:
    dry-run: true
    target-filter: 'my-org/api-*'
```

**Outputs:** `prs-created`, `prs-updated`, `repos-skipped`, `errors`, `summary`

## monitor

Check target repositories for configuration drift and create issues.

```bash
strspc monitor --config steerspec-sync.yml
```

Monitor finds an open drift issue by its fingerprint or title, not by `monitor.issue-label`, so an issue
whose label was removed is still updated or closed instead of duplicated. An issue without the label counts
only when a bot, or an owner, member or collaborator of the repository, opened it. A failure to create, update or
close one target's issue does not stop the others: it is counted in the summary's `errors` and listed in
`repo_errors`, and the command then exits non-zero.

**Action usage:**

```yaml
- uses: SteerSpec/strspc-sync/monitor@v0.4.1
```

**Outputs:** `repos-in-sync`, `repos-drifted`, `issues-created`, `issues-closed`, `summary`

## conflict

Detect conflicts across AI configuration files in target repositories.

```bash
strspc conflict --config steerspec-sync.yml
strspc conflict --config steerspec-sync.yml --tiers 1,2
```

A tier that fails (tier 3 is not implemented yet) does not discard the others: the report of the tiers
that ran is printed and the outputs are set, then the command exits non-zero. To update an open conflict
issue instead of opening another, conflict finds it by fingerprint or title, not by `conflicts.issue-label`;
the cleanup that closes resolved conflict issues still only considers issues carrying the label. As for
monitor, an issue without the label counts only when a bot, or an owner, member or collaborator, opened it.

Every conflict issue is filed in the central repository, like monitor's drift issues. Its title names the target
(`[SteerSpec Conflict] version-drift: org/repo/CLAUDE.md`), and a cross-repository conflict such as a duplicate skill
lists its repositories in the body. Only conflicts at or above `conflicts.severity-threshold` get an issue: `critical`,
`warning` (the default) or `info`. Raising the threshold leaves the issue of a conflict still detected below it open.
A conflict issue an earlier version filed in a target is closed once the central issue tracks its conflict, with a line
pointing to it. That happens even with `auto-close` off, since it would be a duplicate. When the token cannot reach the
target's issues, it is logged and skipped. The report counts those in `issues_moved`.

A public central repository's issues are public, so a conflict about a private or internal repository gets only a
stub there: `[SteerSpec Conflict] <type>: non-public repository`. The stub names no repository or file. Its
fingerprint and participant list are keyed hashes under the action's `hash-key` input (`conflict-hash-key` on the
root action, `STEERSPEC_CONFLICT_HASH_KEY` for the CLI). Pass it from a secret and keep it stable: changing it opens
new stubs. Without it, those conflicts are not filed and the run fails with an error saying so.

The full issue lives in each private or internal repository the conflict concerns, where only that repository's
readers see it, and points to the stub. A duplicate skill's copy names its own repository and the public ones, and
only counts the other non-public ones. The report counts stubs in `issues_redacted`.

A full central issue whose details must become hidden is closed, not edited, and a fresh stub replaces it. GitHub
keeps an issue's earlier titles and bodies in its timeline and edit history, so editing would not hide them. An
organization owner can delete the closed issue to remove them. When the central repository stops being public, a stub
shows its details again, and the private copies are closed as duplicates.

Warnings about a target's issues name no repository, since the central repository's job logs are public too. A
repository whose visibility cannot be read stops the filing of the conflicts that depend on it, with an error that
names no repository. The report itself, printed to the job log and the `report` output, still lists every conflict
(#274).

**Action usage:**

```yaml
- uses: SteerSpec/strspc-sync/conflict@v0.4.1
  with:
    tiers: '1,2'
```

**Outputs:** `conflicts-found`, `critical-count`, `warning-count`, `info-count`, `summary`
