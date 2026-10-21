# Export the Claude Code login of `koodam login claude-code` in a login shell. The controller
# mounts the optional Secret koodam-login at /etc/koodam/login. A new login reads the file again,
# so it gets the token after a new `koodam login`. This file never writes the token to the output.
# If the user already exported CLAUDE_CODE_OAUTH_TOKEN, this file does not change it.
# Stop the shell trace while the token is in a variable, then restore it. A trace shows values.
case $- in
  *x*) koodam_login_x=1; set +x ;;
  *) koodam_login_x= ;;
esac
koodam_login_f=/etc/koodam/login/claude-code
if [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] && [ -f "$koodam_login_f" ] && [ -r "$koodam_login_f" ] \
  && [ -s "$koodam_login_f" ]; then
  koodam_login_v=$(cat "$koodam_login_f" 2>/dev/null)
  if [ -n "$koodam_login_v" ]; then
    export CLAUDE_CODE_OAUTH_TOKEN="$koodam_login_v"
  fi
fi
unset koodam_login_f koodam_login_v
if [ -n "$koodam_login_x" ]; then
  unset koodam_login_x
  set -x
else
  unset koodam_login_x
fi
