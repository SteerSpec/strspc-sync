#!/usr/bin/env bash
# Asserts that no composite action interpolates `${{ inputs.* }}` into a run script,
# and that hostile filter values reach strspc as single, literal arguments.
#
# `strspc ${ARGS}` with the inputs pasted into ARGS made a workflow_dispatch
# value like `x; echo pwned` run as shell, with the App private key in the
# environment, and word-split/glob-expanded legitimate patterns such as
# `SteerSpec/*`. Inputs now arrive through env: and are passed as a quoted array.
set -euo pipefail

cd "$(dirname "$0")/.."

actions=(action.yml sync/action.yml monitor/action.yml conflict/action.yml)
failures=0

for action in "${actions[@]}"; do
  # Inside a `run: |` block (indented deeper than the key), no `inputs.*` expression may appear.
  if awk '
    /^      run: \|/ { inrun = 1; next }
    inrun && /^      [^ ]/ { inrun = 0 }
    inrun && /\$\{\{ *inputs\./ { print FILENAME ":" FNR ": " $0; bad = 1 }
    END { exit bad }
  ' "$action"; then :; else
    echo "FAIL: $action interpolates an inputs.* expression into its run script"
    failures=$((failures + 1))
  fi
done

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/bin"
cat > "$tmp/bin/strspc" <<'S'
#!/usr/bin/env bash
printf '%s\n' "$@" > "$STRSPC_ARGS_OUT"
S
chmod +x "$tmp/bin/strspc"

# shellcheck disable=SC2016 # single quotes: the shell syntax must stay literal
hostile='x; touch PWNED $(touch PWNED2) `touch PWNED3` *'

for action in sync/action.yml conflict/action.yml; do
  python3 - "$action" > "$tmp/run.sh" <<'P'
import sys, yaml
steps = yaml.safe_load(open(sys.argv[1]))["runs"]["steps"]
print(next(s["run"] for s in steps if s.get("id") == "run"))
P
  export STRSPC_ARGS_OUT="$tmp/args"
  (
    cd "$tmp"
    PATH="$tmp/bin:$PATH" \
    STRSPC_INPUT_CONFIG_PATH="steerspec-sync.yml" \
    STRSPC_INPUT_DRY_RUN="true" \
    STRSPC_INPUT_TARGET_FILTER="$hostile" \
    STRSPC_INPUT_TEMPLATE_FILTER="$hostile" \
    STRSPC_INPUT_FORCE="false" \
    STRSPC_INPUT_TIERS="1,2" \
      bash "$tmp/run.sh"
  )
  if ls "$tmp"/PWNED* >/dev/null 2>&1; then
    echo "FAIL: $action executed injected shell"
    failures=$((failures + 1))
  fi
  if [ "$(grep -cxF -- "$hostile" "$tmp/args")" -lt 1 ]; then
    echo "FAIL: $action did not pass the filter through as one literal argument"
    failures=$((failures + 1))
  fi
done

if [ "$failures" -ne 0 ]; then
  echo "$failures assertion(s) failed"
  exit 1
fi
echo "OK: no run-script interpolation; hostile filters stay literal"
