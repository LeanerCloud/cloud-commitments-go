#!/bin/bash
# Self-test for .gitleaks.toml. Allowlisted fixture literals are derived from
# the config itself (each `regexes` entry, ^ and $ stripped) and located in the
# tracked tree. Checks that:
#   - the config uses no `paths`, `condition`, `stopwords`, `commits`,
#     `targetRules` or `regexTarget` key (gitleaks 8.30.1 skips a whole file
#     when `paths` is combined with condition = "AND"), and that injecting one
#     is detected;
#   - each literal scans clean on its own;
#   - the literal with a prefix or suffix, or beside a PAT-shaped string, is caught;
#   - the real tree is clean.
# Usage: GITLEAKS=/path/to/gitleaks bash scripts/test-gitleaks-config.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONFIG="$REPO_ROOT/.gitleaks.toml"
GITLEAKS="${GITLEAKS:-gitleaks}"
command -v "$GITLEAKS" >/dev/null || { echo "gitleaks not found (set GITLEAKS)" >&2; exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Assembled at runtime so this file itself scans clean.
PAT="ghp""_aB3dE5gH7jK9mN1pQ3sT5vW7yZ9bC1dE3fG5"

pass=0 fail=0
check() { # label expected_rc actual_rc
    if [ "$3" -eq "$2" ]; then pass=$((pass + 1)); else fail=$((fail + 1)); echo "FAIL: $1 (expected rc $2, got $3)" >&2; fi
}

scan() { # dir; prints nothing, returns gitleaks rc
    local rc=0
    "$GITLEAKS" dir "$1" --config "$CONFIG" --no-banner --redact --exit-code 1 >/dev/null 2>&1 || rc=$?
    echo "$rc"
}

# 0 when the config is free of the forbidden keys, 1 otherwise.
forbidden_keys() {
    if grep -qE '^[[:space:]]*(paths|condition|stopwords|commits|targetRules|regexTarget)[[:space:]]*=' "$1"; then return 1; fi
}
rc=0; forbidden_keys "$CONFIG" || rc=$?
check "config has no paths/condition/stopwords/commits/targetRules/regexTarget" 0 "$rc"
for key in 'paths = ['"'''"'unrelated\.txt'"'''"']' 'condition = "AND"' 'stopwords = ["x"]' 'commits = ["abc"]' 'targetRules = ["generic-api-key"]' 'regexTarget = "line"'; do
    { cat "$CONFIG"; printf '\n[[allowlists]]\n%s\n' "$key"; } >"$tmp/injected.toml"
    rc=0; forbidden_keys "$tmp/injected.toml" || rc=$?
    check "injected '$key' is detected" 1 "$rc"
done

# Literals from the config: regexes = ['''^literal$'''].
literals=$(sed -nE "s/^regexes = \['''\^(.*)\\$'''\]\$/\1/p" "$CONFIG")
[ -n "$literals" ] || { echo "no anchored regexes found in $CONFIG" >&2; exit 1; }
unanchored=$(grep -E "^regexes" "$CONFIG" | grep -vE "\['''\^.*\\$'''\]\$" || true)
[ -z "$unanchored" ] || { echo "FAIL: regexes not anchored with ^...\$:" >&2; echo "$unanchored" >&2; fail=$((fail + 1)); }

i=0
while IFS= read -r literal; do
    i=$((i + 1))
    path=$(git -C "$REPO_ROOT" grep -lF -e "$literal" -- . ':!.gitleaks.toml' ':!scripts/test-gitleaks-config.sh' | head -1)
    [ -n "$path" ] || { echo "FAIL: allowlisted literal #$i not found in the tree" >&2; fail=$((fail + 1)); continue; }
    d="$tmp/c$i"
    mkdir -p "$d/$(dirname "$path")"
    printf 'const token = "%s"\n' "$literal" >"$d/$path"
    check "fixture literal in $path is allowed" 0 "$(scan "$d")"
    printf 'const token = "%sx9"\n' "$literal" >"$d/$path"
    check "fixture literal plus a suffix in $path is caught" 1 "$(scan "$d")"
    printf 'const token = "%s"\n' "$literal" >"$d/$path"
    printf 'const real = "%s"\n' "$PAT" >>"$d/$path"
    check "PAT-shaped string beside the fixture in $path is caught" 1 "$(scan "$d")"
done <<<"$literals"

check "repository tree is clean" 0 "$(scan "$REPO_ROOT")"

echo "$pass PASS, $fail FAIL"
[ "$fail" -eq 0 ]
