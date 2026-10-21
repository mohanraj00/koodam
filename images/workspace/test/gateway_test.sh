#!/bin/bash
# Test gateway.sh with a fake gateway folder. Run it from the repo root:
#   bash images/workspace/test/gateway_test.sh
# It writes only to a temporary folder. It exits 1 if a check fails. The keys are fake values
# that the test makes at run time. The test never prints a key.
set -u
unset KOODAM_T_ONE KOODAM_T_TWO

src=$(cd "$(dirname "$0")/.." && pwd)/gateway.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
dir=$work/gateway
# Use a copy of gateway.sh that reads the fake folder in place of /etc/koodam/gateway.
script=$work/gateway.sh
sed "s|/etc/koodam/gateway|$dir|g" "$src" >"$script"
fake1=fake-$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')
fake2=fake-$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')
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

# run sources the script in a new shell ($1: sh, bash or dash), then runs the check in $2 in the
# same shell. The check gets the fake values as $1 and $2. stdout and stderr go to files.
run() {
  local shell=$1 body=$2
  "$shell" -c ". \"$script\"; $body" sh "$fake1" "$fake2" >"$work/stdout" 2>"$work/stderr"
}

# quiet: stdout and stderr are empty, and no fake value is in them.
quiet() {
  test ! -s "$work/stdout" && test ! -s "$work/stderr" && no_value
}

# no_value: no fake value is in stdout or stderr.
no_value() {
  ! grep -qF -e "$fake1" -e "$fake2" "$work/stdout" "$work/stderr"
}

shells="sh bash"
command -v dash >/dev/null 2>&1 && shells="$shells dash"
for sh in $shells; do
  # 1. No folder: no variable, no output.
  rm -rf "$dir"
  run "$sh" 'test -z "${KOODAM_T_ONE+x}${KOODAM_T_TWO+x}"'
  check "$sh no folder: not exported" test $? -eq 0
  check "$sh no folder: no output" quiet

  # 2. 2 valid files: 2 variables with the values, also in the environment of a child.
  mkdir -p "$dir"
  printf '%s\n' "$fake1" >"$dir/KOODAM_T_ONE"
  printf '%s' "$fake2" >"$dir/KOODAM_T_TWO"
  run "$sh" 'test "$KOODAM_T_ONE" = "$1" && test "$KOODAM_T_TWO" = "$2"'
  check "$sh 2 files: exported" test $? -eq 0
  check "$sh 2 files: no output" quiet
  run "$sh" 'env | grep -qxF "KOODAM_T_ONE=$1" && env | grep -qxF "KOODAM_T_TWO=$2"'
  check "$sh 2 files: in the environment of a child" test $? -eq 0
  check "$sh 2 files: no output from the child check" quiet

  # 3. Temporary variables are unset.
  run "$sh" 'test -z "${koodam_gw_f+x}${koodam_gw_n+x}${koodam_gw_x+x}"'
  check "$sh 2 files: temporary variables unset" test $? -eq 0

  # 4. A bad file name is skipped. The valid files are still exported.
  printf '%s\n' "$fake1" >"$dir/bad-name"
  printf '%s\n' "$fake1" >"$dir/lower"
  printf '%s\n' "$fake1" >"$dir/9START"
  run "$sh" 'test "$KOODAM_T_TWO" = "$2" && ! env | grep -q "^bad-name=\|^lower=\|^9START="'
  check "$sh bad name: skipped" test $? -eq 0
  check "$sh bad name: no output" quiet
  rm -f "$dir/bad-name" "$dir/lower" "$dir/9START"

  # 5. A shell with the trace on: the trace never shows a value, and the trace stays on.
  "$sh" -c 'set -x; . "$0"; case $- in *x*) ;; *) exit 3 ;; esac
    set +x; test "$KOODAM_T_ONE" = "$1" || exit 4' "$script" "$fake1" >"$work/stdout" 2>"$work/stderr"
  check "$sh trace: the trace stays on and the key is exported" test $? -eq 0
  check "$sh trace: no value in the trace" no_value

  # 6. A shell with the trace off: the trace stays off.
  run "$sh" 'case $- in *x*) exit 3 ;; esac'
  check "$sh no trace: the trace stays off" test $? -eq 0
done

# 7. The script turns the trace on only to restore it (check 5 tests this), and has no echo.
check "script: set -x only to restore the trace" test "$(grep -c 'set -x\|set -o xtrace' "$src")" -eq 1
check "script: no echo or printf" test -z "$(grep -nE '^[^#]*\b(echo|printf)\b' "$src")"

if [ "$fails" -gt 0 ]; then
  printf '%d checks failed\n' "$fails"
  exit 1
fi
printf 'all checks passed\n'
