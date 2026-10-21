#!/bin/bash
# Test globals.sh with a temporary HOME and a fake ConfigMap folder. Run it from the repo root:
#   bash images/workspace/test/globals_test.sh
# It writes only to a temporary folder. It exits 1 if a check fails.
set -u

script=$(cd "$(dirname "$0")/.." && pwd)/globals.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
export HOME=$work/home
export KOODAM_GLOBALS_SRC=$work/globals
mkdir -p "$HOME"
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

# configmap writes a folder with the layout of the kubelet: the keys are links to ..data/KEY,
# and ..data is a link to a folder with a new name for each version. Each argument is KEY=VALUE.
version=0
configmap() {
  version=$((version + 1))
  local d=$KOODAM_GLOBALS_SRC v=..2026_10_21_00_00_0$version.000 kv
  mkdir -p "$d"
  find "$d" -mindepth 1 -maxdepth 1 -exec rm -rf {} +
  mkdir "$d/$v"
  for kv in "$@"; do
    printf '%s' "${kv#*=}" >"$d/$v/${kv%%=*}"
    ln -s "..data/${kv%%=*}" "$d/${kv%%=*}"
  done
  ln -s "$v" "$d/..data"
}

run() { bash "$script" 2>"$work/stderr"; }

# 1. A missing /etc/koodam/globals: no error, and no links.
run
check "missing folder: exit 0" test $? -eq 0
check "missing folder: no CLAUDE.md link" test ! -e "$HOME/.claude/CLAUDE.md"

# 2. The decode, and a bad path.
configmap 'CLAUDE.md=claude' 'AGENTS.md=agents' 'manifest.json={}' \
  'skills_sreview_sSKILL.md=review' 'skills_smy_uskill_s_u_uinit_u_u.py=init' \
  'skills_s.._sx=up' 'skills_sa_s._sx=dot' 'skills_sa__sx=empty' 'other.md=other' \
  'skills_sa_xb=badkey' 'skills_sflat.md=flat'
run
g=$HOME/.koodam/globals
check "decode: CLAUDE.md link" test "$(cat "$HOME/.claude/CLAUDE.md")" = claude
check "decode: AGENTS.md link" test "$(cat "$HOME/.codex/AGENTS.md")" = agents
check "decode: skill review" test "$(cat "$g/skills/review/SKILL.md")" = review
check "decode: _u and _s" test "$(cat "$g/skills/my_skill/__init__.py")" = init
check "decode: claude skill link" test "$(readlink "$HOME/.claude/skills/review")" = "$g/skills/review"
check "decode: codex skill link" test "$(readlink "$HOME/.codex/skills/my_skill")" = "$g/skills/my_skill"
check "decode: no manifest.json file" test ! -e "$g/manifest.json"
check "bad path: no .. segment" test ! -e "$g/skills/x" -a ! -e "$g/x"
check "bad path: no . segment" test ! -e "$g/skills/a"
check "bad path: not under skills" test ! -e "$g/other.md"
check "bad path: no file directly in skills" test ! -e "$g/skills/flat.md"
check "bad path: message on stderr" grep -q 'skills_s.._sx' "$work/stderr"
check "bad key: message on stderr" grep -q 'skills_sa_xb' "$work/stderr"

# 3. No change: quiet, and the tree stays the same.
before=$(readlink "$g")
run
check "no change: stderr is empty" test ! -s "$work/stderr"
check "no change: same tree" test "$(readlink "$g")" = "$before"

# 4. A user file is not overwritten, and other entries stay.
rm "$HOME/.codex/AGENTS.md"
printf 'mine' >"$HOME/.codex/AGENTS.md"
mkdir -p "$HOME/.codex/skills/.system" "$HOME/.claude/skills/own"
mkdir -p "$HOME/.claude/skills/review.user"
rm "$HOME/.claude/skills/review"
mkdir "$HOME/.claude/skills/review"
printf 'user skill' >"$HOME/.claude/skills/review/SKILL.md"
configmap 'CLAUDE.md=claude2' 'AGENTS.md=agents2' 'skills_sreview_sSKILL.md=review2'
run
check "user file: AGENTS.md kept" test "$(cat "$HOME/.codex/AGENTS.md")" = mine
check "user file: user skill folder kept" test "$(cat "$HOME/.claude/skills/review/SKILL.md")" = "user skill"
check "user file: message on stderr" grep -q 'not a link of koodam' "$work/stderr"
check "user file: CLAUDE.md updated" test "$(cat "$HOME/.claude/CLAUDE.md")" = claude2
check "user file: .system kept" test -d "$HOME/.codex/skills/.system"
check "user file: own skill kept" test -d "$HOME/.claude/skills/own"

# 5. A removed skill loses its links. A link of the user to another place stays.
ln -s /tmp "$HOME/.claude/skills/userlink"
configmap 'skills_sreview_sSKILL.md=review2' 'skills_smy_uskill_sSKILL.md=mine'
run
check "removed skill: link before" test -L "$HOME/.codex/skills/my_skill"
check "removed skill: one tree only" test "$(ls -d "$HOME"/.koodam/.globals-tree.* | wc -l)" -eq 1
configmap 'skills_sreview_sSKILL.md=review2'
run
check "removed skill: codex link removed" test ! -e "$HOME/.codex/skills/my_skill" -a ! -L "$HOME/.codex/skills/my_skill"
check "removed skill: claude link removed" test ! -L "$HOME/.claude/skills/my_skill"
check "removed skill: kept skill link" test "$(cat "$HOME/.codex/skills/review/SKILL.md")" = review2
check "removed skill: user link kept" test -L "$HOME/.claude/skills/userlink"

# 6. The ConfigMap goes away: the links of koodam go, the user files stay.
rm -rf "$KOODAM_GLOBALS_SRC"
run
check "missing again: exit 0" test $? -eq 0
check "missing again: CLAUDE.md link removed" test ! -L "$HOME/.claude/CLAUDE.md"
check "missing again: skill link removed" test ! -L "$HOME/.codex/skills/review"
check "missing again: AGENTS.md of the user kept" test "$(cat "$HOME/.codex/AGENTS.md")" = mine

# 7. ours: a link into $globals with a ".." segment is not a link of koodam, so it stays.
st=$HOME/.koodam
configmap 'skills_sreview_sSKILL.md=r'
ln -s "$g/../../.ssh/x" "$HOME/.claude/skills/evil"
ln -s "$g/skills/gone" "$HOME/.claude/skills/gone"
run
check "ours: .. target kept" test "$(readlink "$HOME/.claude/skills/evil")" = "$g/../../.ssh/x"
check "ours: own dangling link removed" test ! -L "$HOME/.claude/skills/gone"
rm "$HOME/.claude/skills/evil"

# 8. Globs: a dangling link of the user stays, an empty ConfigMap gives no literal "*" entry.
ln -s "$work/nothing" "$HOME/.codex/skills/dangling"
configmap
run
check "globs: dangling user link kept" test -L "$HOME/.codex/skills/dangling"
check "globs: no literal * entry" test ! -e "$HOME/.claude/skills/*" -a ! -L "$HOME/.claude/skills/*"
check "globs: skill link removed" test ! -L "$HOME/.claude/skills/review"
check "globs: exit 0 and quiet" test ! -s "$work/stderr"

# 9. Swap: a stale temp link does not stop the swap.
ln -s nowhere "$st/.globals-link.stale1"
configmap 'skills_sreview_sSKILL.md=r9'
run
check "swap: new tree" test "$(cat "$g/skills/review/SKILL.md")" = r9
check "swap: no new temp link" test "$(ls -a "$st" | grep -c '^.globals-link')" -eq 1

# 10. Stamp: a symlink at the stamp path is not written through.
rm -f "$st/.globals-stamp"
ln -s "$work/stamp-target" "$st/.globals-stamp"
configmap 'skills_sreview_sSKILL.md=r10'
run
check "stamp: target not written" test ! -e "$work/stamp-target"
check "stamp: regular file now" test -f "$st/.globals-stamp" -a ! -L "$st/.globals-stamp"
check "stamp: tree updated" test "$(cat "$g/skills/review/SKILL.md")" = r10
rm -f "$st/.globals-stamp"
mkdir -p "$work/stamp-dir"
ln -s "$work/stamp-dir" "$st/.globals-stamp"
configmap 'skills_sreview_sSKILL.md=r10b'
run
check "stamp: nothing moved into a linked folder" test -z "$(ls -A "$work/stamp-dir")"
check "stamp: regular file after a folder link" test -f "$st/.globals-stamp" -a ! -L "$st/.globals-stamp"

# 11. $globals that is not a link of koodam: keep it, do not link from it, exit 0.
#     The no-change path (same ..data) checks it too.
tree=$(readlink "$g")
rm "$g"
mkdir -p "$work/other/skills/foreign"
ln -s "$work/other" "$g"
run
check "globals: exit 0 (no change)" test $? -eq 0
check "globals: message (no change)" grep -q 'is not a link of koodam' "$work/stderr"
check "globals: link kept" test "$(readlink "$g")" = "$work/other"
check "globals: no foreign link" test ! -L "$HOME/.claude/skills/foreign"
configmap 'skills_sreview_sSKILL.md=r11'
run
check "globals: exit 0 (change)" test $? -eq 0
check "globals: message (change)" grep -q 'is not a link of koodam' "$work/stderr"
check "globals: link still kept" test "$(readlink "$g")" = "$work/other"
rm "$g"
mkdir "$g"
run
check "globals: exit 0 (folder)" test $? -eq 0
check "globals: folder kept" test -d "$g" -a ! -L "$g"
rmdir "$g"
ln -s "$tree" "$g"

# 12. Lock (only if flock is available, as in the image).
if command -v flock >/dev/null 2>&1; then
  lock=$st/.globals-lock
  rm -f "$lock"
  ln -s "$work/lock-target" "$lock"
  run
  check "lock: symlink refused" grep -q 'is a symlink' "$work/stderr"
  check "lock: target not made" test ! -e "$work/lock-target"
  rm "$lock"
  printf 'keep' >"$lock"
  flock "$lock" sleep 5 &
  holder=$!
  sleep 1
  t0=$(date +%s)
  run
  t1=$(date +%s)
  check "lock: held lock skips at once" test $((t1 - t0)) -lt 3
  check "lock: message" grep -q 'another run holds the lock' "$work/stderr"
  check "lock: no truncation" test "$(cat "$lock")" = keep
  kill "$holder" 2>/dev/null
  wait "$holder" 2>/dev/null
else
  printf 'skip lock checks: flock is not available\n'
fi

# 13. AGENTS.md and no CLAUDE.md: Claude Code reads AGENTS.md through a koodam import file.
# The sleep of section 12 can still hold the lock, so wait for it.
command -v flock >/dev/null 2>&1 && flock "$st/.globals-lock" true
imp=$g/claude-imports-agents.md
configmap 'AGENTS.md=a13'
run
check "import: CLAUDE.md link to the import" test "$(readlink "$HOME/.claude/CLAUDE.md")" = "$imp"
check "import: the import text" test "$(cat "$HOME/.claude/CLAUDE.md")" = '@AGENTS.md'
check "import: AGENTS.md next to the link" test "$(cat "$HOME/.claude/AGENTS.md")" = a13
check "import: AGENTS.md next to the target" test "$(cat "$(dirname "$(readlink -f "$HOME/.claude/CLAUDE.md")")/AGENTS.md")" = a13
check "import: codex AGENTS.md of the user kept" test "$(cat "$HOME/.codex/AGENTS.md")" = mine
configmap 'AGENTS.md=a13' 'CLAUDE.md=c13'
run
check "import: CLAUDE.md wins" test "$(cat "$HOME/.claude/CLAUDE.md")" = c13
check "import: no import file" test ! -e "$imp"
check "import: AGENTS.md link removed" test ! -e "$HOME/.claude/AGENTS.md" -a ! -L "$HOME/.claude/AGENTS.md"
configmap 'AGENTS.md=a13b'
run
check "import: back to the import" test "$(cat "$HOME/.claude/AGENTS.md")" = a13b
check "import: CLAUDE.md links to the import again" test "$(readlink "$HOME/.claude/CLAUDE.md")" = "$imp"
check "import: the import text again" test "$(cat "$HOME/.claude/CLAUDE.md")" = '@AGENTS.md'
rm "$HOME/.claude/CLAUDE.md"
printf 'my claude' >"$HOME/.claude/CLAUDE.md"
configmap 'AGENTS.md=a13c'
run
check "import: user CLAUDE.md kept" test "$(cat "$HOME/.claude/CLAUDE.md")" = "my claude"
check "import: no AGENTS.md link for a user CLAUDE.md" test ! -L "$HOME/.claude/AGENTS.md"
rm "$HOME/.claude/CLAUDE.md"
printf 'my agents' >"$HOME/.claude/AGENTS.md"
configmap 'AGENTS.md=a13d'
run
check "import: user AGENTS.md in .claude kept" test "$(cat "$HOME/.claude/AGENTS.md")" = "my agents"
rm "$HOME/.claude/AGENTS.md"
configmap 'AGENTS.md=a13e'
run
configmap
run
check "import: CLAUDE.md link removed" test ! -e "$HOME/.claude/CLAUDE.md" -a ! -L "$HOME/.claude/CLAUDE.md"
check "import: AGENTS.md link removed at the end" test ! -e "$HOME/.claude/AGENTS.md" -a ! -L "$HOME/.claude/AGENTS.md"

# 14. A home that is not writable: exit 0.
HOME=$work/nohome bash "$script" 2>/dev/null
check "no home: exit 0" test $? -eq 0

if [ "$fails" -gt 0 ]; then
  printf '%d checks failed\n' "$fails"
  exit 1
fi
printf 'all checks passed\n'
