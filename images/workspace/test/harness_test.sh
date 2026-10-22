#!/bin/bash
# Test harness.sh with a fake harness folder. Run it from the repo root:
#   bash images/workspace/test/harness_test.sh
# It writes only to a temporary folder. It exits 1 if a check fails.
set -u

src=$(cd "$(dirname "$0")/.." && pwd)/harness.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
dir=$work/bin
# Use a copy of harness.sh that reads the fake folder in place of /koodam/bin.
script=$work/harness.sh
sed "s|/koodam/bin|$dir|g" "$src" >"$script"
fails=0

check() {
  local name=$1
  shift
  if "$@"; then
    printf 'ok   %s\n' "$name"
  else
    printf 'FAIL %s\n' "$name"
    fails=$((fails + 1))
  fi
}

# path prints the PATH after the script runs in a new shell ($1: sh or bash) with PATH=$2.
path() {
  PATH=$2 "$1" -c ". \"$script\"; printf '%s' \"\$PATH\""
}

for sh in sh bash; do
  base=/usr/bin:/bin
  check "$sh: no folder, no change" test "$(path "$sh" "$base")" = "$base"
  mkdir -p "$dir"
  check "$sh: folder is first" test "$(path "$sh" "$base")" = "$dir:$base"
  check "$sh: folder only one time" test "$(path "$sh" "$dir:$base")" = "$dir:$base"
  rmdir "$dir"
done

if [ "$fails" -ne 0 ]; then
  exit 1
fi
