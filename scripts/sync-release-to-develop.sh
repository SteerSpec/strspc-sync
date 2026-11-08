#!/usr/bin/env bash
# Usage: sync-release-to-develop.sh VERSION
#
# Opens a pull request against develop that copies the release files release-please
# committed on main (VERSION, .release-please-manifest.json, CHANGELOG.md), so develop
# and branches cut from it carry the released version: the actions read VERSION to pick
# the binary they download (GH #183). release-please only writes to main, its
# target-branch, and main never flows back to develop.
#
# Run from a checkout of the release commit with origin/develop fetched, and GH_TOKEN set
# to the steerspec-sync App token: its PR triggers CI and auto-approve, and the commit is
# made through createCommitOnBranch, which GitHub signs (required_signatures). A person
# merges the PR. Does nothing when develop already has these files as released, or the
# sync branch already has an open PR.
set -euo pipefail

version="${1:?usage: sync-release-to-develop.sh VERSION}"
repo="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is not set}"
files=(VERSION .release-please-manifest.json CHANGELOG.md)
branch="chore/sync-release-${version}"
title="chore(release): sync ${version} to develop"

if git diff --quiet origin/develop HEAD -- "${files[@]}"; then
  echo "develop already has the ${version} release files."
  exit 0
fi

# A standalone assignment, so a failed listing stops the script (set -e) instead of reading as "no PR".
open_pr="$(gh pr list --repo "$repo" --head "$branch" --state open --json number -q '.[].number')"
if [[ -n "$open_pr" ]]; then
  echo "Pull request #${open_pr} from ${branch} is already open."
  exit 0
fi

develop_sha="$(git rev-parse origin/develop)"
# Start the branch at develop's tip; reset it there if a previous run left it behind. Only a 404 means the branch is
# missing: any other failure of the probe stops the script.
if probe="$(gh api "repos/${repo}/git/ref/heads/${branch}" 2>&1)"; then
  gh api -X PATCH "repos/${repo}/git/refs/heads/${branch}" -f sha="$develop_sha" -F force=true >/dev/null
elif [[ "$probe" == *"HTTP 404"* ]]; then
  gh api "repos/${repo}/git/refs" -f ref="refs/heads/${branch}" -f sha="$develop_sha" >/dev/null
else
  echo "checking branch ${branch}: ${probe}" >&2
  exit 1
fi

additions="$(for f in "${files[@]}"; do
  jq -n --arg path "$f" --arg contents "$(base64 <"$f" | tr -d '\n')" '{path: $path, contents: $contents}'
done | jq -s .)"

jq -n \
  --arg repo "$repo" \
  --arg branch "$branch" \
  --arg head "$develop_sha" \
  --arg headline "$title" \
  --arg body "Copies ${files[*]} from the v${version} release on main (GH #183)." \
  --argjson additions "$additions" \
  '{
    query: "mutation($input: CreateCommitOnBranchInput!) { createCommitOnBranch(input: $input) { commit { oid } } }",
    variables: {input: {
      branch: {repositoryNameWithOwner: $repo, branchName: $branch},
      expectedHeadOid: $head,
      message: {headline: $headline, body: $body},
      fileChanges: {additions: $additions}
    }}
  }' | gh api graphql --input - -q '.data.createCommitOnBranch.commit.oid'

gh pr create --repo "$repo" --base develop --head "$branch" --title "$title" --body "$(
  cat <<EOF
Copies the files release-please committed for v${version} on main into develop:
\`VERSION\`, \`.release-please-manifest.json\` and \`CHANGELOG.md\`. release-please only
writes to main, so without this develop, and every branch cut from it, keeps the previous
version, and the actions run from those branches download an old binary (GH #183).

Opened by \`release-please.yml\` after the release. Merge it before the next promotion.
EOF
)"
