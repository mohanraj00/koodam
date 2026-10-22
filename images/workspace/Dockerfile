# The Workspace image of Koodam. The README of this folder has the pod contract.
# Pin the base image by the digest of its multi-platform index.
FROM debian:13-slim@sha256:62bd423aeee912639ca8ee681c2b762cb3e25137e0a92b20be455898a9f034ad

# Install the tools, and remove the host keys of the package. The controller mounts the host key.
# The user dev has a locked password ('*'), so only a public key login is possible.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      openssh-server tmux git bubblewrap ca-certificates bash procps \
 && rm -rf /var/lib/apt/lists/* \
 && rm -f /etc/ssh/ssh_host_* \
 && groupadd -g 1000 dev \
 && useradd -m -u 1000 -g 1000 -s /bin/bash dev \
 && usermod -p '*' dev \
 && mkdir -p /etc/koodam/ssh/host

COPY sshd_config /etc/koodam/sshd_config
COPY start.sh /usr/local/bin/koodam-workspace-start
COPY gateway.sh /etc/profile.d/koodam-gateway.sh
COPY login.sh /etc/profile.d/koodam-login.sh
COPY codex-login.sh /usr/local/bin/koodam-codex-login
COPY profile-codex-login.sh /etc/profile.d/koodam-codex-login.sh
RUN chmod 0755 /usr/local/bin/koodam-codex-login
RUN chmod 0755 /usr/local/bin/koodam-workspace-start
COPY globals.sh /usr/local/bin/koodam-globals
COPY profile-globals.sh /etc/profile.d/koodam-globals.sh
RUN chmod 0755 /usr/local/bin/koodam-globals
COPY koodam-project.sh /usr/local/bin/koodam-project
COPY project.sh /etc/profile.d/koodam-project.sh
# The controller mounts the harnesses of spec.harnesses in /koodam/bin. The links are in the
# PATH of each SSH command, also with no login shell. A link with no target is not a command.
RUN ln -s /koodam/bin/claude /usr/local/bin/claude \
 && ln -s /koodam/bin/codex /usr/local/bin/codex
RUN chmod 0755 /usr/local/bin/koodam-project

USER 1000:1000
WORKDIR /home/dev
EXPOSE 2222
CMD ["/usr/local/bin/koodam-workspace-start"]
