# CLAUDE.md

## Project Overview

SteerSpec Sync (`strspc`) is a Go CLI tool and GitHub Action that synchronizes AI configuration
files (CLAUDE.md, `.claude/agents/`, `.claude/skills/`, `.claude/settings/`) across a GitHub
organization. Define templates once in a central repo, distribute via PRs.

Implements the [SteerSpec Sync specification](https://github.com/SteerSpec/strspc-spec).

## Workflow Rules

- A GitHub issue MUST always be created documenting the plan prior to starting any work.
- Use beads (`bd`) for all task tracking — never markdown TODOs or external trackers.
- Every beads epic MUST reference at least one GitHub issue.

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

## CI/CD

- **ci.yml**: `go vet` + `go test -race` + golangci-lint on push/PR to main
- **release.yml**: GoReleaser on tag push (`v*`), builds binaries + creates GitHub release
- Coverage artifact uploaded but no threshold enforcement (see issue #5)

## Auth Pattern

Config uses `method: github-token` without a token field. The token is resolved from the
`GITHUB_TOKEN` env var at runtime in `cmd/strspc/main.go:newGHClient()`. This is necessary
because config files aren't workflow YAML and can't use `${{ secrets.X }}`.
