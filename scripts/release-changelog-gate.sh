#!/usr/bin/env bash
# Usage: release-changelog-gate.sh TIP CHANGELOG_REF
#
# Fails when a commit that main gained since the last release has a changelog
# type and is missing from CHANGELOG.md at CHANGELOG_REF (the release pull
# request head, or main when no release pull request is open).
#
# release-please reads main newest first by commit date and stops at the last
# release commit, so a commit merged to develop before a release merged and
# promoted after it is skipped (GH #148; ported from SteerSpec/strspc-manager).
# This script finds the new commits by ancestry instead, which is what actually
# ships.
#
# The last release is the last first-parent commit of TIP that changed the
# release-please manifest: develop never changes it, so promotions don't count.
# Visible types are the changelog-sections of release-please-config.json that
# are not hidden. A commit counts as listed when CHANGELOG.md names its pull
# request (`/issues/<n>)`, from a `(#n)` subject suffix) or its full SHA.
set -euo pipefail

tip="$(git rev-parse --verify "${1:?usage: release-changelog-gate.sh TIP CHANGELOG_REF}^{commit}")"
changelog_ref="${2:?usage: release-changelog-gate.sh TIP CHANGELOG_REF}"
manifest=.release-please-manifest.json
config=release-please-config.json

base="$(git log -1 --first-parent --format=%H "$tip" -- "$manifest")"
if [[ -z "$base" ]]; then
  echo "No release commit on $tip; nothing to check."
  exit 0
fi

types="$(git show "$tip:$config" |
  jq -r '.packages["."]["changelog-sections"][] | select(.hidden != true) | .type' |
  paste -sd'|' -)"
if [[ -z "$types" ]]; then
  echo "No visible changelog-sections in $config at $tip." >&2
  exit 2
fi
changelog="$(git show "$changelog_ref:CHANGELOG.md")"

echo "Commits in ${base:0:7}..${tip:0:7} of type ${types//|/, } must be in CHANGELOG.md at $changelog_ref."
missing=0
checked=0
while IFS=$'\t' read -r sha subject; do
  [[ "$subject" =~ ^($types)(\([^\)]*\))?!?: ]] || continue
  checked=$((checked + 1))
  if [[ "$subject" =~ \(#([0-9]+)\)$ ]] && grep -qF "/issues/${BASH_REMATCH[1]})" <<<"$changelog"; then
    continue
  fi
  if grep -qF "$sha" <<<"$changelog"; then
    continue
  fi
  echo "::error::Missing from CHANGELOG.md: ${sha:0:7} $subject"
  missing=$((missing + 1))
done < <(git log --no-merges --format=$'%H\t%s' "$base..$tip")

if ((missing > 0)); then
  cat >&2 <<'EOF'
release-please skipped these commits. List them in a BEGIN_COMMIT_OVERRIDE
block in the body of the latest promotion pull request (one subject per
paragraph, each ending in (#<pr>)), then re-run the Release Please workflow.
See "Releases" in CONTRIBUTING.md.
EOF
  exit 1
fi
echo "All $checked commit(s) are listed."
