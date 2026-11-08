# Compatibility

From 1.0.0, strspc-sync follows [semantic versioning](https://semver.org). A minor or patch release
does not break a workflow, a config file or a script that uses the surfaces below as documented.
A breaking change waits for the next major version.

## Covered

- **Config file:** every key of `steerspec-sync.yml`, its meaning and its defaults, as specified
  by the `SYNCCFG` entity under [`rules/`](../rules/). The top-level `version` is the config
  schema version; a release may add optional keys but does not remove a key or change its meaning.
- **Action inputs and outputs** of the root action and of `sync/`, `monitor/` and `conflict/`:
  names, accepted values and output formats.
- **CLI:** the commands (`sync`, `monitor`, `conflict`, `version`), their flags, and their exit
  status: 0 on success, non-zero on failure, and for `conflict` non-zero when it finds a critical
  conflict.
- **JSON output:** the report each command writes to stdout and to the `summary` / `report`
  outputs. A release may add fields; it does not remove or rename one or change its type.
- **Deployment state:** `.steerspec/deployment-state.json` in the central repo. A new release
  reads the state an older 1.x release wrote.
- **Issue and PR markers:** the branch names (`steerspec-sync/...`), labels and HTML comment
  fingerprints that sync, monitor and conflict use to find their own pull requests and issues,
  so an upgrade keeps updating and closing what an older 1.x release opened.

## Not covered

- **Tier 3 conflict detection** is experimental. `--tiers 3` fails with "not yet implemented"
  today ([#20](https://github.com/SteerSpec/strspc-sync/issues/20)); when it ships, its findings
  and options may change in a minor release until this page lists it as covered.
- **Log lines** on stderr: their wording and fields may change. Parse the JSON report instead.
- **Wording** of pull request and issue bodies, apart from the markers above.
- **Go packages** under `internal/`, and the e2e harness under `tests/`.

## What counts as breaking

A change is breaking if a config, workflow or script that worked on the previous release, used as
documented, fails or behaves differently: a removed or renamed key, input, output, flag or JSON
field; a changed default; or a value that used to be accepted and now fails validation.

Breaking changes use a `!` conventional commit (`fix(config)!: ...`) with a `BREAKING CHANGE:`
footer, so release-please bumps the major version and the [changelog](../CHANGELOG.md) lists them
first. Before 1.0.0, release-please bumps the minor version for them instead.
