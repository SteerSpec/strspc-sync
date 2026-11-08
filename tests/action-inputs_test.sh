#!/usr/bin/env bash
# Asserts that every composite action declares a `token` input and feeds it to
# the strspc run step as GITHUB_TOKEN.
#
# The run step used to hardcode `GITHUB_TOKEN: ${{ github.token }}`, which
# overrode the caller's `env:` and left the documented github-token auth path
# unable to open pull requests in other repositories. This test keeps the input
# and its wiring from regressing, and checks each action downloads the archive
# format the release publishes for its OS (.zip on darwin, .tar.gz on linux).
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
  # The release publishes darwin builds as .zip only (.goreleaser.yml format_overrides), so the download step must
  # fetch and unzip the .zip on darwin and keep the .tar.gz for linux (SteerSpec/strspc-sync#168).
  if ! grep -Eq 'curl -fsSL -o "\$\{ZIP\}" "\$\{ARCHIVE\}\.zip"' "$action" ||
    ! grep -Eq 'unzip -o -q "\$\{ZIP\}" strspc -d /usr/local/bin' "$action"; then
    echo "FAIL: $action does not download and unzip the darwin .zip archive"
    failures=$((failures + 1))
  fi
  if ! grep -Eq 'curl -fsSL "\$\{ARCHIVE\}\.tar\.gz" \| tar xz -C /usr/local/bin strspc' "$action"; then
    echo "FAIL: $action does not download the linux .tar.gz archive"
    failures=$((failures + 1))
  fi
done

# strspc conflict writes report and summary to GITHUB_OUTPUT; the actions that run it must expose both
# (rule CNFLACT, SteerSpec/strspc-sync#172).
for action in action.yml conflict/action.yml; do
  for output in report summary; do
    if ! grep -Eq "^    value: \\$\\{\\{ steps\\.run\\.outputs\\.${output} \\}\\}$" "$action"; then
      echo "FAIL: $action does not expose the conflict output ${output}"
      failures=$((failures + 1))
    fi
  done
done

if [ "$failures" -ne 0 ]; then
  echo "$failures assertion(s) failed"
  exit 1
fi
echo "OK: ${#actions[@]} actions declare and use inputs.token and download the archive their OS publishes"
