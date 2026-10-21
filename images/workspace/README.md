# Workspace image

This image is the container of a Koodam Workspace pod. A user connects to it with `ssh` through `koodam tunnel`, and runs a harness in `tmux`.

## Contents

- The base is Debian 13 slim, pinned by digest.
- The packages are `openssh-server`, `tmux`, `git`, `bubblewrap`, `ca-certificates`, `bash` and `procps`.
- The user is `dev` (uid 1000, gid 1000), with the shell `/bin/bash` and a locked password. The image runs as uid 1000.
- `/etc/koodam/sshd_config` is the configuration of sshd. sshd listens on port 2222.
- `/usr/local/bin/koodam-workspace-start` is the start script (`start.sh`).
- `/etc/profile.d/koodam-gateway.sh` (`gateway.sh`) exports each file in `/etc/koodam/gateway` as an environment variable in a login shell. sshd does not give the container environment to an SSH session, so the gateway key is a file and not an environment variable.
- `/etc/profile.d/koodam-login.sh` (`login.sh`) exports `CLAUDE_CODE_OAUTH_TOKEN` in a login shell from the file `/etc/koodam/login/claude-code` (the token of `koodam login claude-code`). If the file is missing or empty, it does nothing. It does not replace a value that the user exported, and it never writes the token to the output. To test it, run `bash images/workspace/test/login_test.sh` from the root of the repo.
- `/usr/local/bin/koodam-project` (`koodam-project.sh`) lets Claude Code read the `AGENTS.md` and the `.agents/skills` of a project folder. It writes `CLAUDE.md` with the text `@AGENTS.md` and links `.claude/skills` to `../.agents/skills`, only if these do not exist. It adds the files that it made to the git exclude file. It never replaces a file and never fails. `/etc/profile.d/koodam-project.sh` (`project.sh`) runs it at each login in each folder of the home with `AGENTS.md` or `.agents/skills`. The harness of an Agent run calls the same script first. To test it, run `bash images/workspace/test/project_test.sh`.

The start script runs as uid 1000. It does these steps:

1. If `~/.profile` or `~/.bashrc` is missing (an empty PVC home), it copies the file from `/etc/skel`.
2. It makes `~/.local/bin`. The Debian `~/.profile` adds this folder to the login `PATH`.
3. It runs `koodam-globals`. The section "Global files" tells what this script does.
4. It runs `exec /usr/sbin/sshd -D -e -f /etc/koodam/sshd_config`.

sshd does not run as root, so it can log in only the user `dev`. It writes no PID file (`PidFile none`). It cannot write login records, and it writes a log line about this for each session. This log line is not an error.

## Global files

`/usr/local/bin/koodam-globals` (`globals.sh`) copies the files of `koodam profile push` from `/etc/koodam/globals` to the home. The start script runs it before sshd. `/etc/profile.d/koodam-globals.sh` (`profile-globals.sh`) runs it at each login, so a new push is seen without a pod restart.

1. If the target of `/etc/koodam/globals/..data` did not change, the script does not copy the files again. Then it writes nothing.
2. Else it decodes each key to a path: first `_s` becomes `/`, then `_u` becomes `_`. It skips `manifest.json` and the dot entries of the kubelet.
3. It refuses a key that is not valid, and a path that is not `CLAUDE.md`, `AGENTS.md` or `skills/NAME/FILE`. A path with an empty, `.` or `..` segment is not valid. It writes a message on stderr for each refused key.
4. It writes the files to a new folder `~/.koodam/.globals-tree.XXXXXX`. Then it changes the link `~/.koodam/globals` to the new folder in one rename, and removes the old folder.
5. It makes the links `~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md`, `~/.claude/skills/NAME` and `~/.codex/skills/NAME` to `~/.koodam/globals`. It makes one link for each skill and never replaces the folder `skills`, because Codex writes `~/.codex/skills/.system`.
6. If the globals have `AGENTS.md` and no `CLAUDE.md`, Claude Code must also read `AGENTS.md`. The script writes the file `claude-imports-agents.md` with the text `@AGENTS.md` in the tree. It links `~/.claude/CLAUDE.md` to this file, and `~/.claude/AGENTS.md` to `AGENTS.md` of the tree.
7. It removes each link that it made (a link into `~/.koodam/globals`) for a file or a skill that is no longer in the ConfigMap.

The script never replaces a file, a folder or a link of the user. If `~/.koodam/globals` is not a link to a `.globals-tree.*` folder, the script does not change it and makes no links. If another run holds the lock, the script stops at once. If `/etc/koodam/globals` is missing, the result is an empty tree. The script never fails: it writes each error on stderr and exits 0. Do not edit the files under `~/.koodam/globals`, because the next push replaces them.

To test the script with a temporary `HOME` and a fake ConfigMap folder, run `bash images/workspace/test/globals_test.sh` from the root of the repo.

## The pod contract

The controller mounts these paths. The pod passes the Pod Security `restricted` profile.

| Path in the pod | Source | Mode |
|---|---|---|
| `/home/dev` | the PVC from the StatefulSet `volumeClaimTemplates` (`spec.storage`) | read-write |
| `/etc/koodam/ssh/authorized_keys` | the ConfigMap `<workspace>-ssh`, key `authorized_keys` | read-only |
| `/etc/koodam/ssh/host/ssh_host_ed25519_key` | the Secret `<workspace>-ssh-host`, key `ssh_host_ed25519_key`, `defaultMode` 0440 with the pod `fsGroup` 1000 (owner root, group 1000) | read-only |
| `/etc/koodam/globals` | the ConfigMap `koodam-globals`, optional. `koodam-globals` copies it to `~/.koodam/globals` | read-only |
| `/etc/koodam/login` | the Secret `koodam-login`, optional. The key `claude-code` holds the token of `koodam login claude-code`. `defaultMode` 0440 | read-only |
| `/etc/koodam/gateway` | the Secret `<workspace>-gateway`, only if the Workspace has `spec.gateway`. One file for each variable: `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_MODEL`, `ANTHROPIC_DEFAULT_HAIKU_MODEL`, `ANTHROPIC_DEFAULT_SONNET_MODEL`, `ANTHROPIC_DEFAULT_OPUS_MODEL`, `ANTHROPIC_SMALL_FAST_MODEL`, `CLAUDE_CODE_SUBAGENT_MODEL`, `OPENAI_BASE_URL` and `OPENAI_API_KEY`. `defaultMode` 0440 | read-only |

The controller does not mount a shared memory folder. A shared memory PVC is planned in #91.

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
