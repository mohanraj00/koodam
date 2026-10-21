#!/bin/bash
# Copy the global files of `koodam profile push` from the ConfigMap koodam-globals to the home,
# and link them for Claude Code and Codex. The README of this folder has the rules.
#
# The start script and each login shell run this script. It never fails: it writes each error to
# stderr and exits 0. If the ConfigMap did not change, it does not copy the files again.
#
# Environment (for the tests): HOME, and KOODAM_GLOBALS_SRC (default /etc/koodam/globals).

src=${KOODAM_GLOBALS_SRC:-/etc/koodam/globals}
state=$HOME/.koodam
globals=$state/globals
stampfile=$state/.globals-stamp
lockfile=$state/.globals-lock
# import is a file of the tree that imports AGENTS.md for Claude Code. It is not a valid key path,
# so no key can replace it.
import=claude-imports-agents.md

warn() { printf 'koodam-globals: %s\n' "$*" >&2; }

# decode_path gives the path of a ConfigMap key, or returns 1 for a key that is not valid.
# The CLI changes each "_" to "_u", then each "/" to "_s". Thus each "_" in a key starts an
# escape, and this order of the replacements is correct.
decode_path() {
  local key=$1
  [[ $key =~ ^([-.a-zA-Z0-9]|_[us])+$ ]] || return 1
  key=${key//_s//}
  printf '%s' "${key//_u/_}"
}

# check_path returns 0 if a decoded path is CLAUDE.md, AGENTS.md or skills/NAME/FILE, with no
# empty, "." or ".." segment.
check_path() {
  local p=$1 seg
  case $p in
    CLAUDE.md | AGENTS.md) return 0 ;;
    skills/*/*) ;;
    *) return 1 ;;
  esac
  local IFS=/
  for seg in $p; do
    case $seg in '' | . | ..) return 1 ;; esac
  done
  case $p in */ | *//*) return 1 ;; esac
  return 0
}

# ours returns 0 if the path is a symlink that this script made. Its target (not resolved) must
# start with "$globals/" and must have no ".." segment.
ours() {
  local t
  [ -L "$1" ] || return 1
  t=$(readlink "$1") || return 1
  case $t in "$globals"/*) ;; *) return 1 ;; esac
  case /$t/ in */../*) return 1 ;; esac
  return 0
}

# globals_ok returns 0 if $globals is missing, or is a link that this script made: a link to a
# .globals-tree.* folder next to it. Else the script does not use or replace $globals.
globals_ok() {
  local t
  if [ -L "$globals" ]; then
    t=$(readlink "$globals") || return 1
    case $t in
      */*) return 1 ;;
      .globals-tree.?*) return 0 ;;
    esac
    return 1
  fi
  [ ! -e "$globals" ]
}

# link makes the symlink dst -> target. It replaces only a symlink that this script made, and
# never a file or a folder of the user.
link() {
  local target=$1 dst=$2
  if [ -L "$dst" ] && [ "$(readlink "$dst")" = "$target" ]; then
    return 0
  fi
  if [ -e "$dst" ] || [ -L "$dst" ]; then
    if ! ours "$dst"; then
      [ "$changed" = 1 ] && warn "skip $dst: it is not a link of koodam, so koodam keeps it"
      return 0
    fi
    rm -f "$dst" || return 0
  fi
  mkdir -p "${dst%/*}" && ln -s "$target" "$dst" || warn "cannot link $dst"
}

# swap points the symlink $globals to the new tree $1 in one rename, then removes the old trees.
swap() {
  local new=$1 tmp d
  tmp=$(mktemp -u "$state/.globals-link.XXXXXX") || return 1
  ln -s "${new##*/}" "$tmp" || return 1
  if ! mv -T "$tmp" "$globals" 2>/dev/null && ! mv -fh "$tmp" "$globals" 2>/dev/null; then
    rm -f "$tmp"
    return 1
  fi
  for d in "$state"/.globals-tree.*; do
    [ -e "$d" ] || [ -L "$d" ] || continue
    [ "$d" = "$new" ] || rm -rf "$d"
  done
}

# rebuild copies each valid key of $src to a new tree, then swaps the tree.
rebuild() {
  local new key p f
  new=$(mktemp -d "$state/.globals-tree.XXXXXX") || return 1
  if [ -d "$src" ]; then
    for f in "$src"/*; do
      [ -e "$f" ] || [ -L "$f" ] || continue
      key=${f##*/}
      # Kubernetes adds the ..data link and the ..<time> folder. The glob skips dot entries.
      [ "$key" = manifest.json ] && continue
      [ -f "$f" ] || continue
      if ! p=$(decode_path "$key"); then
        warn "skip the key $key: it is not a valid key of koodam profile push"
        continue
      fi
      if ! check_path "$p"; then
        warn "skip the key $key: the path $p is not CLAUDE.md, AGENTS.md or skills/NAME/FILE"
        continue
      fi
      if ! { mkdir -p "$new/$(dirname "$p")" && cat "$f" >"$new/$p"; } 2>/dev/null; then
        warn "skip the key $key: cannot write $p"
      fi
    done
  fi
  # If the globals have AGENTS.md and no CLAUDE.md, Claude Code reads AGENTS.md through an import.
  if [ -f "$new/AGENTS.md" ] && [ ! -e "$new/CLAUDE.md" ]; then
    printf '@AGENTS.md\n' >"$new/$import" || warn "cannot write $import"
  fi
  swap "$new" || { warn "cannot replace $globals"; rm -rf "$new"; return 1; }
}

# relink makes the links for the current tree, and removes each link of koodam that has no file.
relink() {
  local f dir name
  if [ -f "$globals/CLAUDE.md" ]; then
    link "$globals/CLAUDE.md" "$HOME/.claude/CLAUDE.md"
  elif [ -f "$globals/$import" ]; then
    link "$globals/$import" "$HOME/.claude/CLAUDE.md"
  fi
  [ -f "$globals/AGENTS.md" ] && link "$globals/AGENTS.md" "$HOME/.codex/AGENTS.md"
  # The import "@AGENTS.md" is relative to the CLAUDE.md file. The link ~/.claude/AGENTS.md
  # makes it work for the link path, and the tree makes it work for the resolved path.
  if [ "$(readlink "$HOME/.claude/CLAUDE.md" 2>/dev/null)" = "$globals/$import" ]; then
    link "$globals/AGENTS.md" "$HOME/.claude/AGENTS.md"
  elif ours "$HOME/.claude/AGENTS.md"; then
    rm -f "$HOME/.claude/AGENTS.md"
  fi
  for f in "$HOME/.claude/CLAUDE.md" "$HOME/.claude/AGENTS.md" "$HOME/.codex/AGENTS.md"; do
    ours "$f" && [ ! -e "$f" ] && rm -f "$f"
  done
  # Link each skill one by one. Codex writes ~/.codex/skills/.system, so never replace the folder.
  for dir in "$HOME/.claude/skills" "$HOME/.codex/skills"; do
    for f in "$globals"/skills/*/; do
      [ -d "$f" ] || continue
      f=${f%/}
      name=${f##*/}
      link "$globals/skills/$name" "$dir/$name"
    done
    [ -d "$dir" ] || continue
    for f in "$dir"/* "$dir"/.[!.]*; do
      [ -e "$f" ] || [ -L "$f" ] || continue
      ours "$f" && [ ! -e "$f" ] && rm -f "$f"
    done
  done
}

main() {
  [ -n "$HOME" ] && [ -d "$HOME" ] && [ -w "$HOME" ] || { warn "the home is not writable"; return 0; }
  mkdir -p "$state" || return 0

  # Two logins can run at the same time. If flock is available, let one run do the work, and
  # skip at once if another run holds the lock. Open the lock file with no truncation.
  if command -v flock >/dev/null 2>&1; then
    if [ -L "$lockfile" ]; then
      warn "skip: $lockfile is a symlink"
      return 0
    fi
    exec 9>>"$lockfile" || return 0
    flock -n 9 || { warn "skip: another run holds the lock"; return 0; }
  fi

  # If $globals is not a link of this script, keep it, and do not link from it.
  if ! globals_ok; then
    warn "skip: $globals is not a link of koodam, so koodam does not change it"
    return 0
  fi

  # The kubelet changes the target of ..data for each new version of the ConfigMap. If the
  # target did not change, keep the tree. A folder with no ..data (a test) always gives a rebuild.
  local stamp=missing old= changed tmp
  if [ -d "$src" ]; then
    stamp=$(readlink "$src/..data" 2>/dev/null) || stamp=
  fi
  [ -f "$stampfile" ] && [ ! -L "$stampfile" ] && old=$(cat "$stampfile" 2>/dev/null)
  changed=0
  if [ -z "$stamp" ] || [ "$stamp" != "$old" ] || [ ! -d "$globals" ]; then
    changed=1
    rebuild || return 0
    # Write the stamp through a new file and a rename. Never write through a symlink.
    if [ -d "$stampfile" ] && [ ! -L "$stampfile" ]; then
      warn "skip the stamp: $stampfile is a folder"
    elif [ -n "$stamp" ] && tmp=$(mktemp "$state/.globals-stamp.XXXXXX"); then
      # mv into a symlink to a folder moves the file into the folder, so remove the link first.
      [ -L "$stampfile" ] && rm -f "$stampfile"
      printf '%s\n' "$stamp" >"$tmp" && mv -f "$tmp" "$stampfile" || rm -f "$tmp"
    else
      rm -f "$stampfile"
    fi
  fi
  relink
}

main "$@"
exit 0
