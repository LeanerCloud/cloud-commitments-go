#!/usr/bin/env bash
# scripts/gosec-hook.sh
#
# Pre-commit gosec hook: runs gosec on only the Go packages that contain staged
# files, resolved to their owning module.  Fast by design: never scans the whole
# repo; each commit triggers at most one gosec invocation per affected module.
#
# Rule set, gosec version and toolchain live in scripts/run-gosec.sh, shared
# with `make security-scan-go` and CI. This script only maps staged files to
# (module, package) pairs.
#
# Called by pre-commit with pass_filenames: true and files: \.go$.
# Exits 0 when no staged .go files survive filtering (deleted / testdata).
# Exits 1 on any gosec finding; exits 2 on setup failure.

set -euo pipefail

RUN_GOSEC="$(dirname "$0")/run-gosec.sh"

# Module roots in longest-prefix order, owned by run-gosec.sh.
MODULE_DIRS="$("$RUN_GOSEC" --list-modules | tr '\n' ' ')"

# ---- helpers ----------------------------------------------------------------

# Print the module root (relative to repo root) that owns a given relative file
# path, or empty string when the file belongs to the root module.
module_for() {
    local f="$1" mod
    for mod in $MODULE_DIRS; do
        case "$f" in
            "$mod"/*) printf '%s' "$mod"; return ;;
        esac
    done
    printf ''
}

# ---- main -------------------------------------------------------------------

[[ $# -eq 0 ]] && exit 0

# Filter: skip deleted files and files under testdata/.
live_files=()
for f in "$@"; do
    [[ -f "$f" ]] || continue
    case "$f" in
        */testdata/*) continue ;;
        testdata/*)   continue ;;
    esac
    live_files+=("$f")
done

[[ ${#live_files[@]} -eq 0 ]] && exit 0

# Build "module|package" pairs from the live file list.
# Package path is relative to the owning module root, in ./pkg notation.
pairs_raw=()
for f in "${live_files[@]}"; do
    mod=$(module_for "$f")
    pkg_dir=$(dirname "$f")

    if [[ -n "$mod" ]]; then
        # Strip the module prefix (plus the separating slash).
        rel="${pkg_dir:$((${#mod}+1))}"
        [[ -z "$rel" ]] && rel="."   # file sits directly in the module root
    else
        rel="$pkg_dir"               # root module; pkg_dir is already relative
    fi

    # Normalise to go-tool notation.
    if [[ "$rel" == "." ]]; then
        pkg="."
    else
        pkg="./$rel"
    fi

    pairs_raw+=("${mod}|${pkg}")
done

# Deduplicate.
pairs_sorted=$(printf '%s\n' "${pairs_raw[@]}" | sort -u)

# Enumerate unique module roots.
mods=$(printf '%s\n' "$pairs_sorted" | cut -d'|' -f1 | sort -u)

run_args=()
while IFS= read -r mod; do
    pkgs=$(printf '%s\n' "$pairs_sorted" \
        | awk -F'|' -v m="$mod" '$1==m{print $2}' \
        | tr '\n' ' ')
    # An empty module means a file outside every module: nothing to scan.
    [[ -n "$mod" ]] || continue
    read -r -a pkg_list <<< "$pkgs"
    run_args+=(--module "$mod" "${pkg_list[@]}")
done <<< "$mods"

[[ ${#run_args[@]} -eq 0 ]] && exit 0
exec "$RUN_GOSEC" "${run_args[@]}"
