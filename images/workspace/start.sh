#!/bin/bash
# Start the Workspace. This script runs as the user dev (uid 1000).
set -euo pipefail

home=/home/dev

# On the first start, the PVC home is empty. Copy the login files from /etc/skel.
for f in .profile .bashrc; do
  if [ ! -e "$home/$f" ]; then
    cp "/etc/skel/$f" "$home/$f"
  fi
done

# The Debian ~/.profile adds ~/.local/bin to PATH only if the folder exists at login.
mkdir -p "$home/.local/bin"

exec /usr/sbin/sshd -D -e -f /etc/koodam/sshd_config
