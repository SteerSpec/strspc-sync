# Configuration

All configuration lives in `steerspec-sync.yml`.
See [quickstart/steerspec-sync.yml](quickstart/steerspec-sync.yml) for an example
and the `SYNCCFG` entity in
[`rules/` at v0.4.1](https://github.com/SteerSpec/strspc-sync/blob/v0.4.1/rules/SYNCCFG.json) for the specification
of every key.

The top-level `version` is the config schema version and must be a full semver such as
`"1.0.0"`; `"1.0"` or `"v1"` fail validation. Template `version`s follow the same format.
`MAJOR.MINOR.PATCH` only: no leading zeros (`"01.0.0"`), pre-release (`"1.0.0-rc.1"`) or build
(`"1.0.0+build"`) suffix.

## Authentication

The `token` input defaults to the workflow's own `GITHUB_TOKEN`, which is scoped to the
repository running the workflow and cannot open pull requests anywhere else — so pass a
PAT (or a GitHub App installation token) with `contents` and `pull_requests` write access on
every target repo. For `auth.method: github-app`, pass `app-id` and `private-key` instead.

Sync commits files through the GraphQL `createCommitOnBranch` mutation, which GitHub signs for
every token type, so its commits pass rulesets that require verified signatures with a PAT as
well as with an App. Files are written with mode `100644`.

### Writing the deployment state

Target repos are only ever changed through pull requests. The deployment state is not:
`.steerspec/deployment-state.json` is committed straight to the central repo's default branch
after every sync. If a ruleset or branch protection on that branch requires pull requests or
status checks, the identity running sync must be allowed to bypass it, for example by adding the
GitHub App as a bypass actor (`always`) on that ruleset. Otherwise the run fails with
`deployment state not saved: ... rejected the commit` and records nothing, so the next run
proposes the same changes again.
