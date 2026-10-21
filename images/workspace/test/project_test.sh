#!/bin/bash
# Test koodam-project.sh and project.sh in temporary folders. Run it from the repo root:
#   bash images/workspace/test/project_test.sh
# It runs the script with sh, bash and dash (if dash is installed). It writes only to a
# temporary folder. It exits 1 if a check fails. If git is missing, it skips the git checks.
set -u

dir=$(cd "$(dirname "$0")/.." && pwd)
script=$dir/koodam-project.sh
work=$(mktemp -d)
trap 'chmod -R u+w "$work"; rm -rf "$work"' EXIT
export HOME=$work/home
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

# run runs the script with the shell $1 on the folder $2. It keeps the exit code in rc.
run() {
  "$1" "$script" "$2" >"$work/stdout" 2>"$work/stderr"
  rc=$?
}
quiet() { test "$rc" -eq 0 && test ! -s "$work/stdout" && test ! -s "$work/stderr"; }
warned() { test "$rc" -eq 0 && test ! -s "$work/stdout" && test -s "$work/stderr"; }
lines() { grep -cxF -- "$2" "$1"; }

has_git=
if command -v git >/dev/null 2>&1; then
  has_git=1
else
  printf 'note git is missing, so the test skips the git checks\n'
fi

shells="sh bash"
command -v dash >/dev/null 2>&1 && shells="$shells dash"
n=0
for sh in $shells; do
  # new makes a new empty project folder in p.
  new() { n=$((n + 1)); p=$work/p$n; mkdir -p "$p"; }

  new; echo agents >"$p/AGENTS.md"; run "$sh" "$p"
  check "$sh: AGENTS.md only: quiet" quiet
  check "$sh: AGENTS.md only: CLAUDE.md" test "$(cat "$p/CLAUDE.md")" = "@AGENTS.md"
  check "$sh: AGENTS.md only: no .claude" test ! -e "$p/.claude"

  new; echo agents >"$p/AGENTS.md"; echo mine >"$p/CLAUDE.md"; run "$sh" "$p"
  check "$sh: CLAUDE.md exists: quiet" quiet
  check "$sh: CLAUDE.md exists: no change" test "$(cat "$p/CLAUDE.md")" = mine

  new; echo agents >"$p/AGENTS.md"; ln -s missing "$p/CLAUDE.md"; run "$sh" "$p"
  check "$sh: dangling CLAUDE.md: quiet" quiet
  check "$sh: dangling CLAUDE.md: no change" test "$(readlink "$p/CLAUDE.md")" = missing
  check "$sh: dangling CLAUDE.md: no target" test ! -e "$p/missing"

  new; mkdir -p "$p/.agents/skills/review"; run "$sh" "$p"
  check "$sh: skills only: quiet" quiet
  check "$sh: skills only: link" test "$(readlink "$p/.claude/skills")" = ../.agents/skills
  check "$sh: skills only: link works" test -d "$p/.claude/skills/review"
  check "$sh: skills only: no CLAUDE.md" test ! -e "$p/CLAUDE.md"

  new; mkdir -p "$p/.agents/skills" "$p/.claude/skills/mine"; run "$sh" "$p"
  check "$sh: .claude/skills exists: quiet" quiet
  check "$sh: .claude/skills exists: no change" test ! -L "$p/.claude/skills" -a -d "$p/.claude/skills/mine"

  new; mkdir -p "$p/.agents/skills" "$work/out$n"; ln -s "$work/out$n" "$p/.claude"; run "$sh" "$p"
  check "$sh: .claude is a link: warning, exit 0" warned
  check "$sh: .claude is a link: no write through it" test -z "$(ls -A "$work/out$n")"

  new; mkdir -p "$p/AGENTS.md"; run "$sh" "$p"
  check "$sh: AGENTS.md is a folder: quiet" quiet
  check "$sh: AGENTS.md is a folder: no CLAUDE.md" test ! -e "$p/CLAUDE.md"

  for name in "a b" "-x"; do
    q=$work/q$n/$name; mkdir -p "$q"; echo agents >"$q/AGENTS.md"; run "$sh" "$q"
    check "$sh: folder '$name': CLAUDE.md" test -f "$q/CLAUDE.md"
  done

  new; mkdir -p "$p/c" "$work/cd$n/c"; echo agents >"$p/c/AGENTS.md"
  (cd "$p" && CDPATH=$work/cd$n run "$sh" c)
  check "$sh: CDPATH: the right folder" test -f "$p/c/CLAUDE.md" -a ! -e "$work/cd$n/c/CLAUDE.md"

  new; echo agents >"$p/AGENTS.md"; (cd "$p" && run "$sh" "")
  check "$sh: current folder: CLAUDE.md" test -f "$p/CLAUDE.md"

  new; echo agents >"$p/AGENTS.md"; run "$sh" "$p"
  check "$sh: not a git folder: quiet" quiet

  if [ -n "$has_git" ]; then
    new; git init -q "$p"; echo agents >"$p/AGENTS.md"; mkdir -p "$p/.agents/skills"
    run "$sh" "$p"; check "$sh: git run 1: quiet" quiet
    run "$sh" "$p"; check "$sh: git run 2: quiet" quiet
    rm "$p/CLAUDE.md"
    run "$sh" "$p"; check "$sh: git run 3: quiet" quiet
    ex=$p/.git/info/exclude
    check "$sh: git: CLAUDE.md once" test "$(lines "$ex" /CLAUDE.md)" -eq 1
    check "$sh: git: .claude/skills once" test "$(lines "$ex" /.claude/skills)" -eq 1
    check "$sh: git: status hides the files" test "$(git -C "$p" status --porcelain --untracked-files=all)" = "?? AGENTS.md"

    new; git init -q "$p"; echo agents >"$p/AGENTS.md"; echo keep >"$work/target$n"
    rm -f "$p/.git/info/exclude"; ln -s "$work/target$n" "$p/.git/info/exclude"; run "$sh" "$p"
    check "$sh: exclude is a link: warning, exit 0" warned
    check "$sh: exclude is a link: no append" test "$(cat "$work/target$n")" = keep

    new; git init -q "$p"; mkdir -p "$p/sub"; echo agents >"$p/sub/AGENTS.md"; run "$sh" "$p/sub"
    check "$sh: git subfolder: entry" test "$(lines "$p/.git/info/exclude" /sub/CLAUDE.md)" -eq 1
  fi

  if [ "$(id -u)" -ne 0 ]; then
    new; echo agents >"$p/AGENTS.md"; mkdir -p "$p/.agents/skills"; chmod a-w "$p"; run "$sh" "$p"
    check "$sh: read-only folder: warning, exit 0" warned
    check "$sh: read-only folder: no CLAUDE.md" test ! -e "$p/CLAUDE.md"
  fi

  run "$sh" "$work/none"
  check "$sh: missing folder: warning, exit 0" warned
done
[ "$(id -u)" -eq 0 ] && printf 'note root ignores the file mode, so the test skips the read-only checks\n'

# project.sh runs the script only in the direct child folders of the home with AGENTS.md or
# .agents/skills. Use a copy that runs this script in place of /usr/local/bin/koodam-project.
profile=$work/project.sh
sed "s|/usr/local/bin/koodam-project|$script|g" "$dir/project.sh" >"$profile"
chmod +x "$script" 2>/dev/null
mkdir -p "$HOME/a" "$HOME/b/.agents/skills" "$HOME/.hidden" "$HOME/c/deep" "$HOME/d"
for f in a .hidden c/deep; do echo agents >"$HOME/$f/AGENTS.md"; done
if [ -x "$script" ]; then
  sh -c ". \"$profile\"" >"$work/stdout" 2>"$work/stderr"
  rc=$?
  check "profile: quiet" quiet
  check "profile: a has CLAUDE.md" test -f "$HOME/a/CLAUDE.md"
  check "profile: b has the link" test -L "$HOME/b/.claude/skills"
  check "profile: skips a dot folder" test ! -e "$HOME/.hidden/CLAUDE.md"
  check "profile: one level only" test ! -e "$HOME/c/deep/CLAUDE.md"
  check "profile: d has no change" test -z "$(ls -A "$HOME/d")"
else
  printf 'note %s is not executable, so the test skips the profile checks\n' "$script"
fi

if [ "$fails" -gt 0 ]; then
  printf '%d checks failed\n' "$fails"
  exit 1
fi
printf 'all checks passed\n'
