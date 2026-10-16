#!/usr/bin/env bash
# Usage: verify-golangci-config.sh <golangci-lint-binary> [config]
#
# Runs `golangci-lint config verify` against the vendored schema. The tool's
# default is to fetch the schema over HTTPS with a hard 2s timeout, which
# flakes in CI (#420). `--schema` is a hidden flag that upstream CI also uses.
set -euo pipefail

if [[ "$#" -lt 1 || "$#" -gt 2 ]]; then
  echo "usage: $0 <golangci-lint-binary> [config]" >&2
  exit 2
fi
bin="$1"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
config="${2:-$root/.golangci.yml}"
makefile="$root/Makefile"
workflow="$root/.github/workflows/ci.yml"

pin="$(sed -n 's/^GOLANGCI_LINT_VERSION?=\(v[0-9][0-9.]*\)$/\1/p' "$makefile")"
if [[ "$(printf '%s\n' "$pin" | grep -c .)" -ne 1 ]]; then
  echo "expected exactly one GOLANGCI_LINT_VERSION pin in $makefile" >&2
  exit 1
fi

# Only the version: line inside the golangci-lint-action step counts; ci.yml
# has other version: keys.
action_versions="$(awk '
  /^[[:space:]]*- name:/ { in_step = 0 }
  /uses:[[:space:]]*golangci\/golangci-lint-action@/ { in_step = 1; next }
  in_step && /^[[:space:]]*version:/ { print $2 }
' "$workflow")"
install_pins="$(grep -o 'golangci-lint@v[0-9.]*' "$workflow" | sed 's/^golangci-lint@//')"

for pair in "action version:$action_versions" "go install pin:$install_pins"; do
  label="${pair%%:*}"
  value="${pair#*:}"
  if [[ "$(printf '%s\n' "$value" | grep -c .)" -ne 1 ]]; then
    echo "expected exactly one $label in $workflow, found: ${value:-none}" >&2
    exit 1
  fi
  if [[ "$value" != "$pin" ]]; then
    echo "$label $value != Makefile pin $pin; bump all pins together and re-vendor the schema" >&2
    exit 1
  fi
done

actual="$("$bin" --version | sed -n 's/.*version \(v\{0,1\}[0-9][0-9.]*\).*/\1/p')"
if [[ "v${actual#v}" != "$pin" ]]; then
  echo "binary reports ${actual:-unknown}, expected $pin" >&2
  exit 1
fi

minor="${pin#v}"
minor="${minor%.*}"
schema="$root/.github/golangci/golangci.v${minor}.jsonschema.json"
if [[ "$schema" != /* ]]; then
  echo "schema path must be absolute: $schema" >&2
  exit 1
fi
for f in "$schema" "$schema.sha256"; do
  if [[ ! -f "$f" ]]; then
    echo "missing $f; vendor the schema for $pin from the upstream tag" >&2
    exit 1
  fi
done

expected_sum="$(awk '{ print $1 }' "$schema.sha256")"
actual_sum="$(shasum -a 256 "$schema" 2>/dev/null || sha256sum "$schema")"
if [[ "${actual_sum%% *}" != "$expected_sum" ]]; then
  echo "schema checksum mismatch for $schema" >&2
  exit 1
fi

exec "$bin" config verify --config "$config" --schema "file://$schema"
