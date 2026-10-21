# Export the gateway key of the Workspace in a login shell. The controller mounts one file for
# each variable in /etc/koodam/gateway if the Workspace has spec.gateway. A new login reads the
# files again, so it gets the key after a key change. This file never writes the key to the output.
# Stop the shell trace while the key is in a variable, then restore it. A trace shows values.
case $- in
  *x*) koodam_gw_x=1; set +x ;;
  *) koodam_gw_x= ;;
esac
if [ -d /etc/koodam/gateway ]; then
  for koodam_gw_f in /etc/koodam/gateway/*; do
    koodam_gw_n=${koodam_gw_f##*/}
    case $koodam_gw_n in
      '' | [0-9]* | *[!A-Z0-9_]*) continue ;;
    esac
    [ -f "$koodam_gw_f" ] && export "$koodam_gw_n=$(cat "$koodam_gw_f")"
  done
fi
unset koodam_gw_f koodam_gw_n
if [ -n "$koodam_gw_x" ]; then
  unset koodam_gw_x
  set -x
else
  unset koodam_gw_x
fi
