#!/usr/bin/env bash
# Tests scripts/sync-release-to-develop.sh (GH #183) against a throwaway git repository and
# a stub gh that records its calls, so nothing reaches GitHub.
set -euo pipefail

script="$(cd "$(dirname "$0")/.." && pwd)/scripts/sync-release-to-develop.sh"
failures=0
fail() {
  echo "FAIL: $*"
  failures=$((failures + 1))
}

# setup DIR SAME: a repo whose HEAD is the release commit and origin/develop an older
# commit; SAME=1 gives develop the released files already.
setup() {
  local dir=$1 same=$2
  git init -q "$dir"
  git -C "$dir" config user.email t@example.com
  git -C "$dir" config user.name t
  git -C "$dir" config commit.gpgsign false
  printf '0.4.2\n' >"$dir/VERSION"
  printf '{".": "0.4.2"}\n' >"$dir/.release-please-manifest.json"
  printf '# Changelog\n\n## 0.4.2\n' >"$dir/CHANGELOG.md"
  printf 'code\n' >"$dir/main.go"
  git -C "$dir" add -A
  git -C "$dir" commit -q -m develop
  git -C "$dir" update-ref refs/remotes/origin/develop HEAD
  if [[ "$same" == 1 ]]; then
    printf 'more\n' >>"$dir/main.go"
  else
    printf '0.4.4\n' >"$dir/VERSION"
    printf '{".": "0.4.4"}\n' >"$dir/.release-please-manifest.json"
    printf '# Changelog\n\n## 0.4.4\n\n## 0.4.2\n' >"$dir/CHANGELOG.md"
  fi
  git -C "$dir" commit -q -am release
}

# stub_gh DIR OPEN_PR: a gh that logs each call to DIR/calls, saves the graphql payload to
# DIR/graphql.json, and lists an open PR when OPEN_PR is 1.
stub_gh() {
  local dir=$1 open_pr=$2
  mkdir -p "$dir/bin"
  cat >"$dir/bin/gh" <<EOF
#!/usr/bin/env bash
echo "\$*" >>"$dir/calls"
case "\$1 \$2" in
  "pr list") [[ "$open_pr" == 1 ]] && echo 7; exit 0 ;;
  "pr create") echo "https://github.com/o/r/pull/8"; exit 0 ;;
  "api graphql") cat >"$dir/graphql.json"; echo oid123; exit 0 ;;
  "api repos/o/r/git/ref/heads/"*) exit 1 ;;
  *) exit 0 ;;
esac
EOF
  chmod +x "$dir/bin/gh"
}

# run DIR: runs the script in DIR/repo with the stub gh of DIR first on PATH.
run() {
  local dir=$1
  (cd "$dir/repo" && PATH="$dir/bin:$PATH" GITHUB_REPOSITORY=o/r "$script" 0.4.4) >"$dir/out" 2>&1
}

# 1. Release files differ: one signed commit of the three files on a branch from develop, and a PR.
d="$(mktemp -d)"
setup "$d/repo" 0
stub_gh "$d" 0
if ! run "$d"; then
  fail "sync exited non-zero: $(cat "$d/out")"
fi
develop_sha="$(git -C "$d/repo" rev-parse origin/develop)"
grep -q "api repos/o/r/git/refs -f ref=refs/heads/chore/sync-release-0.4.4 -f sha=${develop_sha}" "$d/calls" ||
  fail "branch not created from develop: $(cat "$d/calls")"
payload="$d/graphql.json"
[[ "$(jq -r '.variables.input.expectedHeadOid' "$payload")" == "$develop_sha" ]] || fail "expectedHeadOid is not develop"
[[ "$(jq -r '.variables.input.branch.branchName' "$payload")" == "chore/sync-release-0.4.4" ]] || fail "wrong branch"
[[ "$(jq -r '.variables.input.message.headline' "$payload")" == "chore(release): sync 0.4.4 to develop" ]] ||
  fail "wrong commit headline"
[[ "$(jq -r '[.variables.input.fileChanges.additions[].path] | join(",")' "$payload")" == \
  "VERSION,.release-please-manifest.json,CHANGELOG.md" ]] || fail "wrong files"
[[ "$(jq -r '.variables.input.fileChanges.additions[0].contents' "$payload" | base64 -d)" == "0.4.4" ]] ||
  fail "VERSION contents not the release's"
grep -q "pr create --repo o/r --base develop --head chore/sync-release-0.4.4" "$d/calls" || fail "no PR to develop"
rm -rf "$d"

# 2. develop already has the release files: no GitHub calls.
d="$(mktemp -d)"
setup "$d/repo" 1
stub_gh "$d" 0
run "$d" || fail "sync with nothing to do exited non-zero: $(cat "$d/out")"
[[ ! -s "$d/calls" ]] || fail "expected no gh calls, got: $(cat "$d/calls")"
rm -rf "$d"

# 3. A sync PR is already open: no commit, no new PR.
d="$(mktemp -d)"
setup "$d/repo" 0
stub_gh "$d" 1
run "$d" || fail "sync with an open PR exited non-zero: $(cat "$d/out")"
if grep -qE "graphql|pr create" "$d/calls"; then
  fail "expected no commit or PR with one open: $(cat "$d/calls")"
fi
rm -rf "$d"

if ((failures > 0)); then
  echo "$failures assertion(s) failed"
  exit 1
fi
echo "OK: sync-release-to-develop opens one signed PR, and only when develop lags"
