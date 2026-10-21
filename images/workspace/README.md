# Workspace image

This image is the container of a Koodam Workspace pod. A user connects to it with `ssh` through `koodam tunnel`, and runs a harness in `tmux`.

## Contents

- The base is Debian 13 slim, pinned by digest.
- The packages are `openssh-server`, `tmux`, `git`, `bubblewrap`, `ca-certificates`, `bash` and `procps`.
- The user is `dev` (uid 1000, gid 1000), with the shell `/bin/bash` and a locked password. The image runs as uid 1000.
- `/etc/koodam/sshd_config` is the configuration of sshd. sshd listens on port 2222.
- `/usr/local/bin/koodam-workspace-start` is the start script (`start.sh`).

The start script runs as uid 1000. It does these steps:

1. If `~/.profile` or `~/.bashrc` is missing (an empty PVC home), it copies the file from `/etc/skel`.
2. It makes `~/.local/bin`. The Debian `~/.profile` adds this folder to the login `PATH`.
3. It runs `exec /usr/sbin/sshd -D -e -f /etc/koodam/sshd_config`.

sshd does not run as root, so it can log in only the user `dev`. It writes no PID file (`PidFile none`). It cannot write login records, and it writes a log line about this for each session. This log line is not an error.

A later PR adds the skill links from `/etc/koodam/globals` to the start script, when the format of `koodam-globals` is decided.

## The pod contract

The controller mounts these paths. The pod passes the Pod Security `restricted` profile.

| Path in the pod | Source | Mode |
|---|---|---|
| `/home/dev` | the PVC from the StatefulSet `volumeClaimTemplates` (`spec.storage`) | read-write |
| `/etc/koodam/ssh/authorized_keys` | the ConfigMap `<workspace>-ssh`, key `authorized_keys` | read-only |
| `/etc/koodam/ssh/host/ssh_host_ed25519_key` | the Secret `<workspace>-ssh-host`, key `ssh_host_ed25519_key`, `defaultMode` 0440 with the pod `fsGroup` 1000 (owner root, group 1000) | read-only |
| `/etc/koodam/globals` | the ConfigMap `koodam-globals`, optional | read-only |
| `/etc/koodam/login` | the Secret `koodam-login`, optional | read-only |
| `/etc/koodam/memory` | the PVC `koodam-memory`, optional | read-write |

sshd uses `Port 2222`, public key login only, `AllowUsers dev`, `MaxSessions 10`, SFTP (`internal-sftp`), `AllowTcpForwarding local` and `StrictModes no`. The mounted files belong to root, so `StrictModes` must be off.

The readiness probe is a TCP check on port 2222.

Note: uid 1000 must be able to read the host key. Set `fsGroup: 1000` in the pod security context and `defaultMode: 0440` on the Secret volume. Then the host key file has the owner root and the group 1000, and the kubelet gives the group 1000 write access to the PVC.

## Build

```sh
docker build -t koodam-workspace:dev images/workspace
```

## Run locally

Make throwaway keys with `ssh-keygen` in a temporary folder. Do not use real keys. Put the host key and `authorized_keys` in a folder with the layout of `/etc/koodam/ssh`, then run:

```sh
docker run --rm --user 1000:1000 --cap-drop ALL --security-opt no-new-privileges \
  --read-only --tmpfs /tmp --tmpfs /home/dev:uid=1000,gid=1000 \
  -v "$PWD/ssh:/etc/koodam/ssh:ro" -p 127.0.0.1:2222:2222 koodam-workspace:dev
ssh -p 2222 -i client dev@127.0.0.1 'tmux -V; git --version; bwrap --version'
```
