#!/bin/bash
# Test nookku.sh and the Nookku step of start.sh with a fake marker folder and a fake home. Run
# it from the repo root:
#   bash images/workspace/test/nookku_test.sh
# It writes only to a temporary folder. It exits 1 if a check fails.
set -u
unset NOOKKU_TESTS_DIR

here=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
marker=$work/marker
home=$work/home
mkdir -p "$home"
# Use copies that read the fake marker in place of /etc/koodam/nookku and the fake home in place
# of /home/dev.
script=$work/nookku.sh
sed -e "s|/etc/koodam/nookku|$marker|g" -e "s|/home/dev|$home|g" "$here/nookku.sh" >"$script"
# The Nookku step of start.sh: the lines from the comment of the step to the first "fi".
step=$work/step.sh
{
  printf 'home=%s\n' "$home"
  sed -n '/mounts \/etc\/koodam\/nookku/,/^fi$/p' "$here/start.sh" | sed "s|/etc/koodam/nookku|$marker|g"
} >"$step"
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
# shell. stdout and stderr go to files.
run() {
  local shell=$1 body=$2
  "$shell" -c ". \"$script\"; $body" sh >"$work/stdout" 2>"$work/stderr"
}

quiet() {
  test ! -s "$work/stdout" && test ! -s "$work/stderr"
}

check "start step: the step is in start.sh" grep -q 'mkdir -p -m 0700' "$step"

# 1. No marker: the start step makes no folder.
rmdir "$marker" 2>/dev/null
bash "$step"
check "start step, no marker: no folder" test ! -e "$home/nookku-tests"

# 2. A marker: the start step makes the folder with mode 0700. A second start does not fail.
mkdir -p "$marker"
bash "$step"
check "start step, marker: folder made" test -d "$home/nookku-tests"
check "start step, marker: mode 0700" test "$(stat -c %a "$home/nookku-tests" 2>/dev/null ||
  stat -f %Lp "$home/nookku-tests")" = 700
touch "$home/nookku-tests/kept"
bash "$step"
check "start step, second start: the tests stay" test -e "$home/nookku-tests/kept"

shells="sh bash"
command -v dash >/dev/null 2>&1 && shells="$shells dash"
for sh in $shells; do
  # 3. No marker: no variable, no output.
  rmdir "$marker" 2>/dev/null || rm -rf "$marker"
  run "$sh" 'test -z "${NOOKKU_TESTS_DIR+x}"'
  check "$sh no marker: not exported" test $? -eq 0
  check "$sh no marker: no output" quiet

  # 4. A marker: the variable is the folder on the home PVC, and it is exported.
  mkdir -p "$marker"
  run "$sh" "test \"\$NOOKKU_TESTS_DIR\" = \"$home/nookku-tests\" && sh -c 'test -n \"\$NOOKKU_TESTS_DIR\"'"
  check "$sh marker: exported" test $? -eq 0
  check "$sh marker: no output" quiet

  # 5. A value of the user stays.
  NOOKKU_TESTS_DIR=/tmp/mine "$sh" -c ". \"$script\"; test \"\$NOOKKU_TESTS_DIR\" = /tmp/mine" sh \
    >"$work/stdout" 2>"$work/stderr"
  check "$sh user value: not changed" test $? -eq 0
done

check "script: no echo or printf" test -z "$(grep -nE '^[^#]*\b(echo|printf)\b' "$here/nookku.sh")"

if [ "$fails" -gt 0 ]; then
  printf '%d checks failed\n' "$fails"
  exit 1
fi
printf 'all checks passed\n'
