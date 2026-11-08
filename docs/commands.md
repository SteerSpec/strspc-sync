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

**Action usage:**

```yaml
- uses: SteerSpec/strspc-sync/conflict@v0.4.1
  with:
    tiers: '1,2'
```

**Outputs:** `conflicts-found`, `critical-count`, `warning-count`, `info-count`, `summary`
