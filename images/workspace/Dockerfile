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
RUN chmod 0755 /usr/local/bin/koodam-workspace-start
COPY globals.sh /usr/local/bin/koodam-globals
COPY profile-globals.sh /etc/profile.d/koodam-globals.sh
RUN chmod 0755 /usr/local/bin/koodam-globals

USER 1000:1000
WORKDIR /home/dev
EXPOSE 2222
CMD ["/usr/local/bin/koodam-workspace-start"]
