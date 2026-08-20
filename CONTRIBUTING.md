# Contributing to SteerSpec Sync

Thanks for your interest in improving `strspc-sync`. This document covers the local setup, the
checks CI enforces, and the tracking workflow this repo uses.

## Prerequisites

- Go (the version in [`go.mod`](go.mod) — CI resolves it via `go-version-file`)
- [pre-commit](https://pre-commit.com/) for the lint hooks

## Getting started

```bash
git clone https://github.com/SteerSpec/strspc-sync.git
cd strspc-sync
go build -o strspc ./cmd/strspc
```

Install the lint hooks once, so problems surface before CI does:

```bash
pip install pre-commit
pre-commit install
```

## Tests

```bash
go test -race ./...                              # all packages, race detector on
go test -cover ./...                             # per-package coverage
go test -race -coverprofile=coverage.out ./...   # coverage profile
```

CI enforces a **70% total coverage threshold**, so new code needs tests to land. Conventions:

- Table-driven tests using the standard `testing` package
- Mock implementations of the GitHub API interfaces (`mockRepoService`, `mockPRService`, …) rather
  than live calls
- `t.TempDir()` for temporary files, `t.Setenv()` for environment variables
- Every package under `internal/` has a matching `_test.go`

## Linting

`pre-commit run --all-files` runs the same six hooks CI does:

| Hook | Config |
|------|--------|
| actionlint | (default) |
| golangci-lint v2 | [`.golangci.yml`](.golangci.yml) |
| shellcheck | (default) |
| yamllint | [`.yamllint.yml`](.yamllint.yml) |
| markdownlint | [`.markdownlint.yml`](.markdownlint.yml) |
| editorconfig-checker | [`.editorconfig-checker.json`](.editorconfig-checker.json) |

## Code conventions

- **Formatting**: gofumpt with extra rules (enforced through golangci-lint)
- **Line length**: 120 characters max for YAML and Markdown
- **Errors**: wrap with `fmt.Errorf("operation: %w", err)`; prefer early returns
- **Interfaces**: defined at package level as contracts (e.g. `RepoLister`, `PullRequestService`)
- **Constructors**: `New()` / `NewType()`
- **Context**: every API method takes `context.Context` as its first parameter
- **Concurrency**: `sync.Mutex` / `sync.RWMutex` for shared state
- **Output**: JSON to stdout via `json.NewEncoder()` with indentation; logs go to stderr

## Workflow

Work in this repo is tracked in GitHub Issues, so please **open an issue before starting a change**
— it gives the work a place to be discussed before code exists, and keeps the history readable.

Then:

1. Branch off `main`
2. Keep commits focused; reference the issue number in the body
3. Make sure `go test -race ./...` and `pre-commit run --all-files` pass
4. Open a pull request describing what changed and why

Maintainers additionally track work with beads (`bd`); see [`CLAUDE.md`](CLAUDE.md) for those
internal conventions. Outside contributors do not need it.

## Security

Please do not open a public issue for security problems — see [SECURITY.md](SECURITY.md) for the
private disclosure process.

## License

By contributing, you agree that your contributions will be licensed under the
[Apache License 2.0](LICENSE).
