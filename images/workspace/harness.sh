# Koodam: add the harness folder to the login PATH. The controller mounts the harnesses of
# spec.harnesses read-only in /koodam/bin. The Debian ~/.profile runs later and puts
# ~/.local/bin first, so a harness that the user installs in the home has priority.
if [ -d /koodam/bin ]; then
  case ":$PATH:" in
    *:/koodam/bin:*) ;;
    *) PATH="/koodam/bin:$PATH"; export PATH ;;
  esac
fi
