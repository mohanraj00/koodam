# Export the gateway key of the Workspace in a login shell. The controller mounts one file for
# each variable in /etc/koodam/gateway if the Workspace has spec.gateway. A new login reads the
# files again, so it gets the key after a key change.
if [ -d /etc/koodam/gateway ]; then
  for f in /etc/koodam/gateway/*; do
    n=${f##*/}
    case $n in
      '' | *[!A-Z0-9_]*) continue ;;
    esac
    [ -f "$f" ] && export "$n=$(cat "$f")"
  done
  unset f n
fi
