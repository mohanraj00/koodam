#!/bin/sh
# koodam-project [FOLDER]: let Claude Code read the AGENTS.md and the skills of a project.
# The default folder is the current folder. The script does these steps:
# 1. If AGENTS.md exists and CLAUDE.md does not exist, it writes CLAUDE.md with "@AGENTS.md".
# 2. If .agents/skills is a folder and .claude/skills does not exist, it makes .claude/skills a
#    relative link to ../.agents/skills.
# 3. If the folder is in a git work tree, it adds each file that it made to info/exclude once.
# It never replaces or removes a file of the project. It writes nothing on success. It writes
# each warning on stderr and always exits 0, so it never stops the caller.

warn() { printf 'koodam-project: %s\n' "$*" >&2; }

dir=${1:-.}
if ! cd -- "$dir" 2>/dev/null; then
  warn "cannot open the folder $dir"
  exit 0
fi

made=
if [ -e AGENTS.md ] && [ ! -e CLAUDE.md ] && [ ! -L CLAUDE.md ]; then
  # noclobber (set -C) does not replace a file that another process made at the same time.
  if (set -C && printf '@AGENTS.md\n' >CLAUDE.md) 2>/dev/null; then
    made="$made CLAUDE.md"
  else
    warn "cannot write CLAUDE.md in $PWD"
  fi
fi

if [ -d .agents/skills ] && [ ! -e .claude/skills ] && [ ! -L .claude/skills ]; then
  if mkdir -p .claude 2>/dev/null && ln -s ../.agents/skills .claude/skills 2>/dev/null; then
    made="$made .claude/skills"
  else
    warn "cannot make the link .claude/skills in $PWD"
  fi
fi

if [ -z "$made" ] || ! command -v git >/dev/null 2>&1; then
  exit 0
fi
if [ "$(git rev-parse --is-inside-work-tree 2>/dev/null)" != true ]; then
  exit 0
fi
prefix=$(git rev-parse --show-prefix 2>/dev/null)
exclude=$(git rev-parse --git-path info/exclude 2>/dev/null)
if [ -z "$exclude" ]; then
  warn "cannot find the git exclude file of $PWD"
  exit 0
fi
for f in $made; do
  entry="/$prefix$f"
  if [ -f "$exclude" ] && grep -qxF -- "$entry" "$exclude" 2>/dev/null; then
    continue
  fi
  if ! { mkdir -p "$(dirname "$exclude")" && printf '%s\n' "$entry" >>"$exclude"; } 2>/dev/null; then
    warn "cannot add $entry to $exclude"
  fi
done
exit 0
