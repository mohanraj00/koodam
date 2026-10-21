#!/bin/bash
# Test login.sh with a fake login folder. Run it from the repo root:
#   bash images/workspace/test/login_test.sh
# It writes only to a temporary folder. It exits 1 if a check fails. The token is a fake value
# that the test makes at run time. The test never prints the token.
set -u
unset CLAUDE_CODE_OAUTH_TOKEN

src=$(cd "$(dirname "$0")/.." && pwd)/login.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
dir=$work/login
mkdir -p "$dir"
# Use a copy of login.sh that reads the fake folder in place of /etc/koodam/login.
script=$work/login.sh
sed "s|/etc/koodam/login|$dir|g" "$src" >"$script"
fake=fake-$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')
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

# run sources the script in a new shell ($1: sh or bash), then runs the check in $2 in the same
# shell. The check gets the fake value as $1. stdout and stderr go to files.
run() {
  local shell=$1 body=$2
  "$shell" -c ". \"$script\"; $body" sh "$fake" >"$work/stdout" 2>"$work/stderr"
}

# quiet: stdout and stderr are empty, and the fake value is in neither of them.
quiet() {
  test ! -s "$work/stdout" && test ! -s "$work/stderr" \
    && ! grep -qF "$fake" "$work/stdout" "$work/stderr"
}

shells="sh bash"
command -v dash >/dev/null 2>&1 && shells="$shells dash"
for sh in $shells; do
  # 1. A missing file: no variable, no output.
  rm -f "$dir/claude-code"
  run "$sh" 'test -z "${CLAUDE_CODE_OAUTH_TOKEN+x}"'
  check "$sh missing file: not exported" test $? -eq 0
  check "$sh missing file: no output" quiet

  # 2. A file with a trailing newline: the value without the newline.
  printf '%s\n' "$fake" >"$dir/claude-code"
  run "$sh" 'test "$CLAUDE_CODE_OAUTH_TOKEN" = "$1"'
  check "$sh file: exported without newline" test $? -eq 0
  check "$sh file: no output" quiet
  run "$sh" 'env | grep -qxF "CLAUDE_CODE_OAUTH_TOKEN=$1"'
  check "$sh file: in the environment of a child" test $? -eq 0
  check "$sh file: no output from the child check" quiet
  run "$sh" 'test -z "${koodam_login_f+x}${koodam_login_v+x}"'
  check "$sh file: temporary variables unset" test $? -eq 0

  # 3. A file with no newline.
  printf '%s' "$fake" >"$dir/claude-code"
  run "$sh" 'test "$CLAUDE_CODE_OAUTH_TOKEN" = "$1"'
  check "$sh no newline: exported" test $? -eq 0
  check "$sh no newline: no output" quiet

  # 4. A value that the user exported stays.
  CLAUDE_CODE_OAUTH_TOKEN=mine run "$sh" 'test "$CLAUDE_CODE_OAUTH_TOKEN" = mine'
  check "$sh user value: kept" test $? -eq 0
  check "$sh user value: no output" quiet

  # 5. An empty file: no variable, no output.
  : >"$dir/claude-code"
  run "$sh" 'test -z "${CLAUDE_CODE_OAUTH_TOKEN+x}"'
  check "$sh empty file: not exported" test $? -eq 0
  check "$sh empty file: no output" quiet

  # 6. A file that the user cannot read: no variable, no output. root can read all files.
  if [ "$(id -u)" -ne 0 ]; then
    printf '%s\n' "$fake" >"$dir/claude-code"
    chmod 000 "$dir/claude-code"
    run "$sh" 'test -z "${CLAUDE_CODE_OAUTH_TOKEN+x}"'
    check "$sh unreadable file: not exported" test $? -eq 0
    check "$sh unreadable file: no output" quiet
    chmod 600 "$dir/claude-code"
  fi

  # 7. A folder in place of the file: no variable, no output.
  rm -f "$dir/claude-code"
  mkdir "$dir/claude-code"
  run "$sh" 'test -z "${CLAUDE_CODE_OAUTH_TOKEN+x}"'
  check "$sh folder: not exported" test $? -eq 0
  check "$sh folder: no output" quiet
  rmdir "$dir/claude-code"
  # 9. A shell with the trace on: the trace never shows the value, and the trace stays on.
  printf '%s\n' "$fake" >"$dir/claude-code"
  "$sh" -c 'set -x; . "$0"; case $- in *x*) ;; *) exit 3 ;; esac' "$script" >"$work/stdout" 2>"$work/stderr"
  check "$sh trace: the trace stays on" test $? -eq 0
  check "$sh trace: no value in the trace" sh -c '! grep -qF "$1" "$2" "$3"' sh "$fake" "$work/stdout" "$work/stderr"
done

# 8. The script turns the trace on only to restore it (check 9 tests this), and has no echo.
check "script: set -x only to restore the trace" test "$(grep -c 'set -x\|set -o xtrace' "$src")" -eq 1
check "script: no echo or printf" test -z "$(grep -nE '^[^#]*\b(echo|printf)\b' "$src")"

if [ "$fails" -gt 0 ]; then
  printf '%d checks failed\n' "$fails"
  exit 1
fi
printf 'all checks passed\n'
