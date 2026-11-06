# Specification

The SteerSpec Sync specification is the `dev.steerspec.sync` realm in
[`rules/` at v0.4.1](https://github.com/SteerSpec/strspc-sync/tree/v0.4.1/rules):
166 rules across 11 top-level entities and 5 sub-entities (the entities cover configuration, manifest, templates,
sync, monitor and conflict actions, deployment state, component registry and versioning), depending on
[`dev.steerspec.core`](https://github.com/SteerSpec/strspc-rules). Rules are authored with the
[`strspc` CLI](https://github.com/SteerSpec/strspc-CLI) (`strspc rule add`, `strspc rule promote`)
and validated in CI (`strspc realm validate`, `strspc lint`, `strspc diff`); every pull request is
also checked against them by `strspc check` (structural checks; the AI evaluation runs only where the org
supplies an `ANTHROPIC_API_KEY`). See the
[CI workflow at v0.4.1](https://github.com/SteerSpec/strspc-sync/blob/v0.4.1/.github/workflows/ci.yml).
