# CLAUDE.md

## Project Overview

SteerSpec Sync (`strspc`) is a Go CLI tool and GitHub Action that synchronizes AI configuration
files (CLAUDE.md, `.claude/agents/`, `.claude/skills/`, `.claude/settings.json`) across a GitHub
organization. Define templates once in a central repo, distribute via PRs.

Implements the SteerSpec Sync specification, which lives in this repository as the
`dev.steerspec.sync` realm under [`rules/`](rules/) (166 rules across 11 entities, authored and
validated with the `strspc` CLI; see [Rules](#rules) below).

## Workflow Rules

- A GitHub issue MUST always be created documenting the plan prior to starting any work.
- Beads (`bd`) MUST be used for ALL activities — planning, tracking, and follow-up. Never use
  markdown TODOs, inline comments, or external trackers as a substitute.
- Every beads epic MUST reference at least one GitHub issue. An epic without a linked GH issue
  is invalid and must not be worked on.
- Upon every commit and push, ALL related beads issues and GH issues MUST be updated:
  - Completed work: close beads epics (`bd close <id>`) and GH issues (`state_reason: completed`).
  - In-progress work: update beads status and add a progress comment to the GH issue.
  - Every closed GH issue MUST have a summary comment documenting what was implemented,
    which files were changed, and the commit reference.

## Build & Run

```bash
go build -o strspc ./cmd/strspc        # Build binary
strspc sync --config steerspec-sync.yml # Run sync
strspc monitor --config steerspec-sync.yml
strspc conflict --config steerspec-sync.yml
```

Version is injected via ldflags at release time (`main.version`, `main.commit`).
GoReleaser builds linux/darwin x amd64/arm64 (tar.gz for linux, zip for darwin).

## Testing

```bash
go test -race ./...                          # Run all tests with race detector
go test -cover ./...                         # Show per-package coverage
go test -race -coverprofile=coverage.out ./... # Generate coverage profile
```

- Table-driven tests with `testing` package
- Mock implementations of interfaces for GitHub API (mockRepoService, mockPRService, etc.)
- Use `t.TempDir()` for temp files, `t.Setenv()` for env vars
- Every `internal/` package has a corresponding `_test.go` file

## Linting

Pre-commit framework runs 6 hooks — install with `pip install pre-commit` then
`pre-commit install`. Hooks are also integrated into beads' `core.hooksPath`.

| Hook | Config |
|------|--------|
| actionlint | (default) |
| golangci-lint v2 | `.golangci.yml` |
| shellcheck | (default) |
| yamllint | `.yamllint.yml` |
| markdownlint | `.markdownlint.yml` |
| editorconfig-checker | `.editorconfig-checker.json` |

CI runs `go vet`, `go test -race`, and golangci-lint on push/PR to main.

## Code Conventions

- **Formatting**: gofumpt with extra-rules (enforced via golangci-lint)
- **Line length**: 120 chars max for YAML/Markdown
- **Error handling**: wrap with `fmt.Errorf("operation: %w", err)`, early returns
- **Interfaces**: define at package level as contracts (e.g., `RepoLister`, `PullRequestService`)
- **Constructors**: `New()` or `NewType()` pattern
- **Context**: all API methods take `context.Context` as first parameter
- **Concurrency**: `sync.Mutex` / `sync.RWMutex` for shared state
- **Output**: JSON to stdout via `json.NewEncoder()` with indentation

## Project Structure

```text
cmd/strspc/          CLI entry point, flag parsing, command routing
internal/
  config/            YAML config parsing & validation
  github/            GitHub API client (repos, pulls, issues)
  hash/              BLAKE3 content hashing
  registry/          Target repo resolution (glob, topic, exclude)
  state/             Deployment state (JSON, hash-based drift detection)
  template/          Rendering strategies (mustache, marker, full-replace)
  sync/              Sync orchestration, PR creation/update
  monitor/           Drift detection, issue creation
  conflict/          Multi-tier conflict detection (tier 1/2/3)
action.yml           Root composite action (GitHub Marketplace)
sync/action.yml      Sub-action for sync command
monitor/action.yml   Sub-action for monitor command
conflict/action.yml  Sub-action for conflict command
docs/quickstart/     Example config + templates
```

## Rules

`rules/` is the `dev.steerspec.sync` realm: the specification of this tool as SteerSpec entity
files, one per entity (`SYNCCFG.json`, `SYNCACT.json`, ...). Edit them only through the CLI so
versions and hashes stay consistent: `strspc rule add <EUID> --body ... --added-by @you`,
`strspc rule promote <RULE>`, `strspc note add`. Validate with `strspc realm validate rules
--recursive --strict`, `strspc lint rules --strict` and `strspc diff rules --base origin/main`.
`.strspc/config.yaml` resolves these rules plus `dev.steerspec.core` for `strspc check`.

## CI/CD

- **ci.yml**: `go vet` + `go test -race` + golangci-lint + lint-extra (actionlint, shellcheck,
  yamllint, markdownlint) on push/PR to main; 70% coverage threshold enforced natively via
  `go tool cover` (no third-party service), with the total written to the run summary and
  `coverage.out` kept as a build artifact
- **release.yml**: pre-flight `go test -race` + build smoke test, then GoReleaser on tag push (`v*`)

## Auth Pattern

Config uses `method: github-token` without a token field. The token is resolved from the
`GITHUB_TOKEN` env var at runtime in `cmd/strspc/main.go:newGHClient()`. This is necessary
because config files aren't workflow YAML and can't use `${{ secrets.X }}`.

Each composite action sets that env var from its `token` input (default `${{ github.token }}`),
so callers pass a PAT with `with: token:`, not `env:` — a step-level `env:` in the action
overrides whatever the caller's `env:` says. The default workflow token is scoped to the
current repository and cannot open PRs elsewhere, so cross-repo sync needs a PAT or an App
installation token. `tests/action-inputs_test.sh` asserts all four action.yml files keep
this wiring.

<!-- STEERSPEC:BEGIN:org-standards -->
## SteerSpec org standards

Managed by [SteerSpec/.claude](https://github.com/SteerSpec/.claude) through
[strspc-sync](https://github.com/SteerSpec/strspc-sync). Change
`templates/CLAUDE.md.mustache` there; edits made here are overwritten by the
next sync.

- **Tiers.** strspc-rules (rule definitions) → strspc-manager (Go engine) →
  strspc-CLI. Dependencies point downward only. Release and tag upstream before
  bumping a downstream `go.mod` or core pin.
- **Pull requests only.** Org rulesets reject direct pushes to the default
  branch and require verified commit signatures; Copilot review and
  pr-auto-approve gate the merge. Agents open PRs and never merge them.
- **Conventional commits** (`type(scope): subject`). release-please derives
  versions and changelogs from them.
- **Rules live next to the code.** Each repo owns a realm under `rules/` that
  depends on `dev.steerspec.core`. Author rules through `strspc rule …` so the
  content hashes stay valid; never hand-edit rule JSON.
- **Rule states.** Draft text may still change. Published text is final while
  the code may still violate it. Implemented is enforced in CI (`fail_on: [I]`);
  promote to Implemented only once the code complies.
- **Work tracking.** Multi-session work is tracked in beads (`bd`), not in
  markdown todo files.
- **Org agents.** Architect, Agile PM, Code Review and Release come from the
  `strspc-agents` plugin: `/plugin marketplace add SteerSpec/.claude`, then
  `/plugin install strspc-agents@steerspec`.
<!-- STEERSPEC:END:org-standards -->