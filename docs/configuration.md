# Configuration

All configuration lives in `steerspec-sync.yml`.
See [quickstart/steerspec-sync.yml](quickstart/steerspec-sync.yml) for an example
and the `SYNCCFG` entity in
[`rules/` at v0.4.1](https://github.com/SteerSpec/strspc-sync/blob/v0.4.1/rules/SYNCCFG.json) for the specification
of every key.

## Authentication

The `token` input defaults to the workflow's own `GITHUB_TOKEN`, which is scoped to the
repository running the workflow and cannot open pull requests anywhere else — so pass a
PAT (or a GitHub App installation token) with `contents` and `pull_requests` write access on
every target repo. For `auth.method: github-app`, pass `app-id` and `private-key` instead.

Sync commits files through the GraphQL `createCommitOnBranch` mutation, which GitHub signs for
every token type, so its commits pass rulesets that require verified signatures with a PAT as
well as with an App. Files are written with mode `100644`.
