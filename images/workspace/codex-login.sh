#!/bin/bash
# Copy the Codex login only if the home has no login. Keep local token refreshes.
# No credential enters a shell variable or the output.
set +x
exec 2>/dev/null
umask 077
src=/etc/koodam/login/codex
dir=$HOME/.codex
# A custom Codex home belongs to the user. Do not write to it.
[ -z "${CODEX_HOME:-}" ] || [ "$CODEX_HOME" = "$dir" ] || exit 0
[ -f "$src" ] && [ -r "$src" ] && [ -s "$src" ] || exit 0
[ ! -L "$dir" ] || exit 0
mkdir -p "$dir" || exit 0
chmod 700 "$dir" || exit 0
[ ! -e "$dir/auth.json" ] && [ ! -L "$dir/auth.json" ] || exit 0
tmp=$(mktemp "$dir/.login.XXXXXX") || exit 0
trap 'rm -f "$tmp"' EXIT
cat "$src" >"$tmp" || exit 0
chmod 600 "$tmp" || exit 0
# The hard link publishes the complete file and cannot replace an existing file.
# -T also refuses a folder or a link to a folder at the destination.
ln -T "$tmp" "$dir/auth.json" || true
