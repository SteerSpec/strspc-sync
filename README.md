<img src="https://steerspec.dev/assets/logo.svg" alt="SteerSpec logo" width="96" height="96">

# SteerSpec Sync

SteerSpec Sync is a Go CLI and GitHub Action for platform and engineering teams that distribute AI configuration
files (`CLAUDE.md`, `.claude/agents/`, `.claude/skills/`, `.claude/settings.json`) from one central repo to every
repository in a GitHub organization, by pull request.

<!-- markdownlint-disable MD013 -->
[![CI](https://github.com/SteerSpec/strspc-sync/actions/workflows/ci.yml/badge.svg)](https://github.com/SteerSpec/strspc-sync/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/SteerSpec/strspc-sync?color=161B22&labelColor=161B22)](https://github.com/SteerSpec/strspc-sync/releases)
[![License](https://img.shields.io/badge/License-Apache%202.0-161B22?labelColor=161B22)](LICENSE)
[![Marketplace](https://img.shields.io/badge/Marketplace-SteerSpec%20Sync-161B22?labelColor=161B22&logo=github&logoColor=E6EDF3)](https://github.com/marketplace/actions/steerspec-sync)
<!-- markdownlint-enable MD013 -->

Pre-1.0; see [Releases](https://github.com/SteerSpec/strspc-sync/releases) for the current version.
Sync never force-pushes a git ref and never merges for you (the `--force` flag only bypasses the
recorded deployment-state hash check; unchanged content never opens a pull request): every change lands as a reviewable pull request in the
target repo. The behaviour is specified by the `dev.steerspec.sync` realm, linked under
[Specification](docs/specification.md).

## Installation

### GitHub Action

Use the composite actions directly from the Marketplace:

```yaml
# Full action (specify command)
- uses: SteerSpec/strspc-sync@v0.4.1
  with:
    command: sync

# Convenience sub-actions
- uses: SteerSpec/strspc-sync/sync@v0.4.1
- uses: SteerSpec/strspc-sync/monitor@v0.4.1
- uses: SteerSpec/strspc-sync/conflict@v0.4.1
```

Pin an exact tag, never a branch. See [Keeping the pin current](docs/keeping-the-pin-current.md) for a Dependabot
setup that opens a pull request for each new release.

### CLI Binary

Download an archive and its `checksums.txt` from
[Releases](https://github.com/SteerSpec/strspc-sync/releases/tag/v0.4.1). Linux ships as `.tar.gz` and macOS as
`.zip`, for `amd64` and `arm64`. Compare the archive's SHA-256 with the entry in `checksums.txt`:

```bash
# Linux (amd64; use linux_arm64 on arm64)
curl -fsSLO https://github.com/SteerSpec/strspc-sync/releases/download/v0.4.1/strspc_0.4.1_linux_amd64.tar.gz
tar xzf strspc_0.4.1_linux_amd64.tar.gz strspc

# macOS (Apple silicon; use darwin_amd64 on Intel)
curl -fsSLO https://github.com/SteerSpec/strspc-sync/releases/download/v0.4.1/strspc_0.4.1_darwin_arm64.zip
unzip strspc_0.4.1_darwin_arm64.zip strspc

sudo mv strspc /usr/local/bin/
```

## Quick Start

1. Create a central repo (e.g. `your-org/steerspec-config`)
2. Add a `steerspec-sync.yml` config and your templates (see [docs/quickstart/](docs/quickstart/) for examples)
3. Add a workflow to sync on push:

```yaml
name: SteerSpec Sync
on:
  push:
    branches: [main]
    paths:
      - 'templates/**'
      - 'steerspec-sync.yml'

jobs:
  sync:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: SteerSpec/strspc-sync/sync@v0.4.1
        with:
          dry-run: false
          token: ${{ secrets.STEERSPEC_TOKEN }}
```

The default `GITHUB_TOKEN` cannot open pull requests in other repositories; see
[Authentication](docs/configuration.md#authentication) for the token and GitHub App options.

## Where to go

| If you want to | Go to |
| --- | --- |
| Run it from a workflow | [Installation](#github-action), [example workflows](docs/quickstart/workflows/) |
| Set up your first central repo | [Quick Start](#quick-start), [docs/quickstart/](docs/quickstart/) |
| Understand what a run does | [How it works](docs/how-it-works.md) |
| Run the CLI locally | [CLI Binary](#cli-binary), [Commands](docs/commands.md) |
| Look up a configuration key or set up authentication | [Configuration](docs/configuration.md) |
| Keep the action pin current | [Keeping the pin current](docs/keeping-the-pin-current.md) |
| Read the specification | [Specification](docs/specification.md) |
| Build, test or contribute | [CONTRIBUTING.md](CONTRIBUTING.md) |
| Report a vulnerability | [Security](#security) |

## Governance

Open source software by Consulare.

The copyright holder is named in [NOTICE](NOTICE).

Every change, including the maintainers', lands by pull request; see [CONTRIBUTING.md](CONTRIBUTING.md).

## Security

To report a security issue privately, email <security@steerspec.dev> and do not open a public issue. See
[SECURITY.md](SECURITY.md) for what to include and the response timeline.

## Community

Bug reports and pull requests are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) for the build,
test, and lint workflow. Pull requests are reviewed by GitHub Copilot, and an automated approval from a bot is
expected; see the
[strspc-pr-review v1.10.2 README](https://github.com/SteerSpec/strspc-pr-review/blob/v1.10.2/README.md).

### Related projects

Part of the [SteerSpec](https://steerspec.dev) ecosystem:

- [strspc-rules](https://github.com/SteerSpec/strspc-rules) — canonical rule format specification
- [strspc-manager](https://github.com/SteerSpec/strspc-manager) — core enforcement engine
- [strspc-CLI](https://github.com/SteerSpec/strspc-CLI) — render, validate, and manage rule files

## License

Apache 2.0 -- see [LICENSE](LICENSE).
