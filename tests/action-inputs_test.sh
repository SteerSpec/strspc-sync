#!/usr/bin/env bash
# Asserts that every composite action declares a `token` input and feeds it to
# the strspc run step as GITHUB_TOKEN.
#
# The run step used to hardcode `GITHUB_TOKEN: ${{ github.token }}`, which
# overrode the caller's `env:` and left the documented github-token auth path
# unable to open pull requests in other repositories. This test keeps the input
# and its wiring from regressing.
set -euo pipefail

cd "$(dirname "$0")/.."

actions=(action.yml sync/action.yml monitor/action.yml conflict/action.yml)
failures=0

for action in "${actions[@]}"; do
  if ! grep -Eq '^  token:$' "$action"; then
    echo "FAIL: $action does not declare inputs.token"
    failures=$((failures + 1))
  fi
  if ! grep -Eq '^    default: \$\{\{ github\.token \}\}$' "$action"; then
    echo "FAIL: $action inputs.token does not default to github.token"
    failures=$((failures + 1))
  fi
  # `inputs.token || github.token`: an unset caller secret arrives as an empty
  # string, which overrides the input default, so the run step must fall back
  # itself (SteerSpec/strspc-sync#51).
  if ! grep -Eq '^        GITHUB_TOKEN: \$\{\{ inputs\.token \|\| github\.token \}\}$' "$action"; then
    echo "FAIL: $action run step does not set GITHUB_TOKEN from inputs.token with a github.token fallback"
    failures=$((failures + 1))
  fi
  if grep -Eq 'GITHUB_TOKEN: \$\{\{ github\.token \}\}$' "$action"; then
    echo "FAIL: $action still hardcodes GITHUB_TOKEN to github.token"
    failures=$((failures + 1))
  fi
done

if [ "$failures" -ne 0 ]; then
  echo "$failures assertion(s) failed"
  exit 1
fi
echo "OK: ${#actions[@]} actions declare and use inputs.token"
