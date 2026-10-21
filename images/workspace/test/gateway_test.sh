#!/bin/bash
# Test gateway.sh with a fake gateway folder. Run it from the repo root:
#   bash images/workspace/test/gateway_test.sh
# It writes only to a temporary folder. It exits 1 if a check fails. The keys are fake values
# that the test makes at run time. The test never prints a key.
set -u
unset ANTHROPIC_AUTH_TOKEN OPENAI_API_KEY CLAUDE_CODE_SUBAGENT_MODEL LD_PRELOAD BASH_ENV

src=$(cd "$(dirname "$0")/.." && pwd)/gateway.sh
work=$(mktemp -d)
trap 'chmod -R u+rwx "$work" 2>/dev/null; rm -rf "$work"' EXIT
dir=$work/gateway
# Use a copy of gateway.sh that reads the fake folder in place of /etc/koodam/gateway.
script=$work/gateway.sh
sed "s|/etc/koodam/gateway|$dir|g" "$src" >"$script"
fake1=fake-$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')
fake2=fake-$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')
fake3=fake-$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')
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
# same shell. The check gets the fake values as $1, $2 and $3. stdout and stderr go to files.
run() {
  local shell=$1 body=$2
  "$shell" -c ". \"$script\"; $body" sh "$fake1" "$fake2" "$fake3" >"$work/stdout" 2>"$work/stderr"
}

# quiet: stdout and stderr are empty, and no fake value is in them.
quiet() {
  test ! -s "$work/stdout" && test ! -s "$work/stderr" && no_value
}

# no_value: no fake value is in stdout or stderr.
no_value() {
  ! grep -qF -e "$fake1" -e "$fake2" -e "$fake3" "$work/stdout" "$work/stderr"
}

shells="sh bash"
command -v dash >/dev/null 2>&1 && shells="$shells dash"
for sh in $shells; do
  # 1. No folder: no variable, no output.
  rm -rf "$dir"
  run "$sh" 'test -z "${ANTHROPIC_AUTH_TOKEN+x}${OPENAI_API_KEY+x}"'
  check "$sh no folder: not exported" test $? -eq 0
  check "$sh no folder: no output" quiet

  # 2. An empty folder: the glob does not match. No variable, no output.
  mkdir -p "$dir"
  run "$sh" 'test -z "${ANTHROPIC_AUTH_TOKEN+x}${OPENAI_API_KEY+x}${koodam_gw_f+x}${koodam_gw_n+x}"'
  check "$sh empty folder: not exported, temporary variables unset" test $? -eq 0
  check "$sh empty folder: no output" quiet

  # 3. 2 valid files: 2 variables with the values, also in the environment of a child.
  printf '%s\n' "$fake1" >"$dir/ANTHROPIC_AUTH_TOKEN"
  printf '%s' "$fake2" >"$dir/OPENAI_API_KEY"
  run "$sh" 'test "$ANTHROPIC_AUTH_TOKEN" = "$1" && test "$OPENAI_API_KEY" = "$2"'
  check "$sh 2 files: exported" test $? -eq 0
  check "$sh 2 files: no output" quiet
  run "$sh" 'env | grep -qxF "ANTHROPIC_AUTH_TOKEN=$1" && env | grep -qxF "OPENAI_API_KEY=$2"'
  check "$sh 2 files: in the environment of a child" test $? -eq 0
  check "$sh 2 files: no output from the child check" quiet

  # 4. Temporary variables are unset.
  run "$sh" 'test -z "${koodam_gw_f+x}${koodam_gw_n+x}${koodam_gw_x+x}"'
  check "$sh 2 files: temporary variables unset" test $? -eq 0

  # 5. A name that the controller does not write is skipped. An allowed name is exported. The
  # check uses only shell built-ins, because a bad PATH stops env and grep.
  for n in PATH LD_PRELOAD BASH_ENV HOME IFS ENV bad-name lower 9START ANTHROPIC_ ANTHROPIC_lower \
    OPENAI_A-B; do
    printf '%s\n' "$fake3" >"$dir/$n"
  done
  printf '%s\n' "$fake2" >"$dir/CLAUDE_CODE_SUBAGENT_MODEL"
  run "$sh" 'test "$CLAUDE_CODE_SUBAGENT_MODEL" = "$2" || exit 4
    test -z "${ANTHROPIC_+x}${ANTHROPIC_lower+x}${LD_PRELOAD+x}${BASH_ENV+x}${ENV+x}" || exit 5
    case "$PATH|$HOME|$IFS" in *"$3"*) exit 6 ;; esac'
  check "$sh bad names: skipped, allowed name exported" test $? -eq 0
  check "$sh bad names: no output" quiet
  for n in PATH LD_PRELOAD BASH_ENV HOME IFS ENV bad-name lower 9START ANTHROPIC_ ANTHROPIC_lower \
    OPENAI_A-B CLAUDE_CODE_SUBAGENT_MODEL; do
    rm -f "$dir/$n"
  done

  # 6. A file that the user cannot read: no variable, no output. root can read all files.
  if [ "$(id -u)" -ne 0 ]; then
    chmod 000 "$dir/OPENAI_API_KEY"
    run "$sh" 'test "$ANTHROPIC_AUTH_TOKEN" = "$1" && test -z "${OPENAI_API_KEY+x}"'
    check "$sh unreadable file: not exported" test $? -eq 0
    check "$sh unreadable file: no output" quiet
    chmod 600 "$dir/OPENAI_API_KEY"
  fi

  # 7. A shell with the trace on: the trace never shows a value, and the trace stays on.
  "$sh" -c 'set -x; . "$0"; case $- in *x*) ;; *) exit 3 ;; esac
    set +x; test "$ANTHROPIC_AUTH_TOKEN" = "$1" || exit 4' "$script" "$fake1" >"$work/stdout" 2>"$work/stderr"
  check "$sh trace: the trace stays on and the key is exported" test $? -eq 0
  check "$sh trace: stderr has trace output" grep -q '^+' "$work/stderr"
  check "$sh trace: no value in the trace" no_value

  # 8. A shell with the trace off: the trace stays off, and no output.
  run "$sh" 'case $- in *x*) exit 3 ;; esac'
  check "$sh no trace: the trace stays off" test $? -eq 0
  check "$sh no trace: no output" quiet
done

# 9. The script turns the trace on only to restore it (check 7 tests this), and has no echo.
check "script: set -x only to restore the trace" test "$(grep -cE 'set -x|set -o xtrace' "$src")" -eq 1
check "script: no echo or printf" \
  test -z "$(grep -nE '^[^#]*(^|[^A-Za-z0-9_])(echo|printf)([^A-Za-z0-9_]|$)' "$src")"

if [ "$fails" -gt 0 ]; then
  printf '%d checks failed\n' "$fails"
  exit 1
fi
printf 'all checks passed\n'
