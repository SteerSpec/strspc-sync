# Keeping the pin current

Pin an exact tag, never a branch: the action runs the binary named by the `VERSION` file at that tag (on Linux
runners; there is no macOS archive yet), and a sync run opens or updates pull requests in the target
repositories whose rendered content changed. Nothing moves an exact pin for you, so let Dependabot open a pull request for each new release:

```yaml
# .github/dependabot.yml
version: 2
updates:
  - package-ecosystem: github-actions
    directory: /
    schedule:
      interval: weekly
    groups:
      actions:
        patterns: ["*"]
```

Read the release notes before merging a bump. Sync, monitor and conflict workflows pinned in the same repository
are updated together by the `groups` entry.
