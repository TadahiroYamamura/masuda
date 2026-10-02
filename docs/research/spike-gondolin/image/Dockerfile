# Spike 1 guest: Ubuntu userspace with the native (Bun-compiled) Claude Code,
# tmux and git. Gondolin turns this into rootfs.ext4 via oci.image; the kernel,
# initramfs and sandbox helpers come from Gondolin's own Alpine boot layer.
FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl git tmux openssh-server sudo python3 jq \
      e2fsprogs procps iproute2 less \
 && rm -rf /var/lib/apt/lists/*
# ubuntu (uid 1000) exists in ubuntu:24.04. Passwordless sudo only for the spike
# so the guest side can be poked at; masuda's real image will not have it.
RUN echo 'ubuntu ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/spike && chmod 440 /etc/sudoers.d/spike
RUN mkdir -p /workspace /masuda && chown ubuntu:ubuntu /workspace /masuda
USER ubuntu
WORKDIR /home/ubuntu
RUN curl -fsSL https://claude.ai/install.sh | bash
ENV PATH=/home/ubuntu/.local/bin:$PATH
RUN python3 -c "import json,pathlib; p=pathlib.Path.home()/'.claude.json'; d=json.loads(p.read_text()) if p.exists() else {}; d['hasCompletedOnboarding']=True; d.setdefault('projects',{}).setdefault('/workspace',{})['hasTrustDialogAccepted']=True; p.write_text(json.dumps(d,indent=2))"
USER root
