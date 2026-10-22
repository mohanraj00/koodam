#!/bin/bash
# Test only synthetic login data in a temporary home. Run on Linux.
set -euo pipefail
src=$(cd "$(dirname "$0")/.." && pwd)/codex-login.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
export HOME=$work/home
unset CODEX_HOME
mkdir -p "$HOME" "$work/login"
sed "s|/etc/koodam/login/codex|$work/login/codex|g" "$src" >"$work/copy.sh"
run() { bash "$work/copy.sh" >"$work/out" 2>"$work/err"; }
quiet() { test ! -s "$work/out" && test ! -s "$work/err"; }
mode() { test "$(stat -c %a "$1")" = "$2"; }
run
quiet
test ! -e "$HOME/.codex"
# Make the fake data at run time. Never print it.
printf '{"synthetic":"%s"}' "$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')" >"$work/login/codex"
run
quiet
cmp -s "$work/login/codex" "$HOME/.codex/auth.json"
mode "$HOME/.codex" 700
mode "$HOME/.codex/auth.json" 600
# A local refresh stays after another login and after a Secret update.
printf 'local-refresh' >"$HOME/.codex/auth.json"
run
quiet
test "$(cat "$HOME/.codex/auth.json")" = local-refresh
printf 'secret-update' >"$work/login/codex"
run
quiet
test "$(cat "$HOME/.codex/auth.json")" = local-refresh
rm "$HOME/.codex/auth.json"
# A custom Codex home stays untouched.
CODEX_HOME=$work/custom run
test ! -e "$work/custom"
test ! -e "$HOME/.codex/auth.json"
# Refuse links and folders at the destination.
ln -s "$work/absent" "$HOME/.codex/auth.json"
run
quiet
test ! -e "$work/absent"
rm "$HOME/.codex/auth.json"
mkdir "$HOME/.codex/auth.json"
run
quiet
test -z "$(ls -A "$HOME/.codex/auth.json")"
rmdir "$HOME/.codex/auth.json" "$HOME/.codex"
mkdir "$work/other"
ln -s "$work/other" "$HOME/.codex"
run
quiet
test -z "$(ls -A "$work/other")"
rm "$HOME/.codex"
# Parallel copies publish a complete file. Trace output contains no login data.
for i in {1..8}; do bash "$work/copy.sh" >"$work/out.$i" 2>"$work/err.$i" & done
wait
for i in {1..8}; do test ! -s "$work/out.$i"; test ! -s "$work/err.$i"; done
cmp -s "$work/login/codex" "$HOME/.codex/auth.json"
test -z "$(find "$HOME/.codex" -name '.login.*' -print)"
rm "$HOME/.codex/auth.json"
bash -x "$work/copy.sh" >"$work/out" 2>"$work/err"
if grep -qF secret-update "$work/out" "$work/err"; then exit 1; fi
cmp -s "$work/login/codex" "$HOME/.codex/auth.json"
printf 'Codex login checks passed\n'
