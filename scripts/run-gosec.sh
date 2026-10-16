#!/usr/bin/env bash
# scripts/run-gosec.sh
#
# Single source of truth for running gosec in this repository. The pre-commit
# hook (scripts/gosec-hook.sh), `make security-scan-go` and the CI gosec step
# all call this script, so a local pass predicts CI.
#
# Rule set: gosec defaults, no -exclude. Suppress a reviewed false positive
# inline with `#nosec Gxxx -- reason`, never by excluding a rule here.
#
# Usage:
#   run-gosec.sh [--sarif-dir DIR] [--module MOD [PKG...]]...
#   run-gosec.sh --list-modules
#
# With no --module the whole of every module is scanned (./...). PKG paths are
# relative to the module root (for example ./exchange); default ./...
# --sarif-dir writes DIR/gosec-<module>.sarif (slashes become dashes) instead of
# printing text findings.
#
# Exit codes: 0 clean, 1 findings or a scan error, 2 setup/usage failure.
#
# The gosec binary is cached per (gosec version, Go toolchain), because gosec
# cannot read export data produced by a newer toolchain than the one it was
# built with. The toolchain defaults to the go directive of pkg/go.mod, which
# is what CI's setup-go uses.

set -euo pipefail

GOSEC_VERSION="v2.28.0"
MODULES=(pkg providers/aws providers/azure providers/gcp ci_cd_sanity_tests)

if [[ "${1:-}" == "--list-modules" ]]; then
    printf '%s\n' "${MODULES[@]}"
    exit 0
fi

REPO_ROOT="$(git rev-parse --show-toplevel)"

sarif_dir=""
declare -a scan_mods=() scan_pkgs=()

while [[ $# -gt 0 ]]; do
    case "$1" in
        --sarif-dir)
            [[ $# -ge 2 ]] || { echo "run-gosec: --sarif-dir needs a directory" >&2; exit 2; }
            sarif_dir="$2"; shift 2 ;;
        --module)
            [[ $# -ge 2 ]] || { echo "run-gosec: --module needs a module path" >&2; exit 2; }
            scan_mods+=("$2"); shift 2
            cur=""
            while [[ $# -gt 0 && "$1" != --* ]]; do
                cur+="${cur:+ }$1"
                shift
            done
            scan_pkgs+=("$cur") ;;
        *) echo "run-gosec: unknown argument: $1" >&2; exit 2 ;;
    esac
done

if [[ ${#scan_mods[@]} -eq 0 ]]; then
    scan_mods=("${MODULES[@]}")
    for _ in "${MODULES[@]}"; do scan_pkgs+=(""); done
fi

if [[ -z "${GOTOOLCHAIN:-}" ]]; then
    go_directive=$(awk '$1=="go"{print $2; exit}' "${REPO_ROOT}/pkg/go.mod")
    [[ -n "$go_directive" ]] || { echo "run-gosec: no go directive in pkg/go.mod" >&2; exit 2; }
    export GOTOOLCHAIN="go${go_directive}"
fi

gosec_dir="${XDG_CACHE_HOME:-${HOME}/.cache}/gosec/${GOSEC_VERSION}-${GOTOOLCHAIN}"
gosec_bin="${gosec_dir}/gosec"

if [[ ! -x "$gosec_bin" ]]; then
    echo "run-gosec: installing gosec@${GOSEC_VERSION} (${GOTOOLCHAIN}) -> ${gosec_dir}" >&2
    mkdir -p "$gosec_dir"
    GOBIN="$gosec_dir" go install "github.com/securego/gosec/v2/cmd/gosec@${GOSEC_VERSION}" || {
        echo "run-gosec: install failed (is Go on PATH?)" >&2
        exit 2
    }
fi

[[ -z "$sarif_dir" ]] || mkdir -p "$sarif_dir"

fail=0
for i in "${!scan_mods[@]}"; do
    mod="${scan_mods[$i]}"
    pkgs="${scan_pkgs[$i]:-./...}"
    args=(-exclude-dir=.legacy -exclude-dir=.dev-notes -exclude-dir=vendor)
    out=""
    if [[ -n "$sarif_dir" ]]; then
        out="${sarif_dir}/gosec-${mod//\//-}.sarif"
        rm -f "$out"
        args+=(-fmt sarif -out "$out")
    else
        # -quiet would also skip writing the SARIF file when there are no findings.
        args+=(-quiet)
    fi

    echo "gosec [${mod}]: scanning ${pkgs}" >&2
    # pkgs intentionally unquoted: space-separated package paths, no glob chars.
    # shellcheck disable=SC2086
    if ! (cd "${REPO_ROOT}/${mod}" && "$gosec_bin" "${args[@]}" $pkgs); then
        echo "::warning::gosec exited non-zero in ${mod} (findings or a scan error)" >&2
        fail=1
    fi
    if [[ -n "$out" && ! -f "$out" ]]; then
        echo "::error::gosec produced no SARIF output for ${mod}" >&2
        fail=1
    fi
done

exit $fail
