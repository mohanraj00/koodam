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

# 7. A home that is not writable: exit 0.
HOME=$work/nohome bash "$script" 2>/dev/null
check "no home: exit 0" test $? -eq 0

if [ "$fails" -gt 0 ]; then
  printf '%d checks failed\n' "$fails"
  exit 1
fi
printf 'all checks passed\n'
