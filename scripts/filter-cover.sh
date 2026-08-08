#!/usr/bin/env bash
# Filters a Go coverage profile to exclude paths listed in .coverignore.
# Usage: filter-cover.sh coverage.out > coverage-filtered.out
#
# .coverignore uses gocover.io-style glob patterns (one per line, '#' comments).
# Lines whose file path starts with any ignore prefix are dropped, so generated
# and upstream-ported infrastructure that is intentionally not unit-tested here
# doesn't drag down the coverage threshold for trace's own code.
set -euo pipefail

in="${1:?usage: filter-cover.sh coverage.out > out}"

# Build a set of ignore prefixes.
ignores=()
while IFS= read -r line; do
  case "$line" in
    ''|'#'*) continue ;;
  esac
  # Strip trailing '*' and '/' to get a prefix, keep everything before '**'.
  pat="${line//\*\*/}"
  pat="${pat%/}"
  ignores+=("$pat")
done < "$(dirname "$0")/../.coverignore"

awk -v ign="${ignores[*]}" '
BEGIN { n = split(ign, arr, " "); for (i=1;i<=n;i++) pre[i]=arr[i]; np=n }
/^mode:/ { print; next }
{
  file = $1; sub(/:.*/, "", file)
  for (i=1;i<=np;i++) if (index(file, pre[i]) > 0) { next }
  print
}
' "$in"
