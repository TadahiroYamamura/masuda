# A guest image that can run Docker, for a masuda privileged command
# (ADR-0053) whose job needs a Docker daemon -- testcontainers-style
# integration tests being the case this exists for.
#
# This is a template: `masuda image add <entry> --template docker`
# materializes it into your repository as .masuda/images/<entry>/Dockerfile,
# and from then on it is yours to edit -- in particular, install whatever
# toolchain your command needs (see the marked section at the bottom).
#
# It deliberately does NOT derive from masuda's own sandbox base image
# (ADR-0054). A VM built from this image runs your command as root with a
# working Docker daemon, and it is destroyed as soon as that command
# finishes; it never receives your Claude credentials, masuda's MCP relay,
# or an SSH key. Keeping it to the packages that boot a VM and run Docker
# keeps that exposure as small as the job allows -- the AI session's own VM
# is a different image entirely, and stays root-less and Docker-less.
FROM ubuntu:24.04

# systemd/systemd-sysv: PID1 in the guest (this image is never `docker run`;
#   masuda converts it to an ext4 rootfs -- ADR-0044).
# systemd-resolved: its own package since Ubuntu 24.04, and installs here
#   only because it is named -- everything below uses --no-install-recommends,
#   so nothing pulls it in implicitly the way masuda's base image does.
# kmod: the guest's systemd-udevd loads virtiofs.ko, which masuda injects
#   into the rootfs at build time, to mount the shares below.
# iptables: dockerd refuses to set up its bridge network without it.
# docker.io: the Docker daemon this whole image exists to provide. Ubuntu's
#   own package rather than Docker's apt repository, so this file needs no
#   third-party key handling to be readable and auditable.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      systemd systemd-sysv systemd-resolved kmod ca-certificates iptables docker.io git \
 && rm -rf /var/lib/apt/lists/*

# /workspace: a per-run *copy* of the workspace tree, not the live one --
#   anything written here dies with the VM unless the command's declaration
#   lists it under "outputs".
# /masuda-results: where the run's exit code, log, and collected outputs are
#   handed back to the host.
RUN mkdir -p /workspace /masuda-results

RUN printf '%s\n' \
      'workspace /workspace virtiofs defaults,nofail 0 0' \
      'masuda-results /masuda-results virtiofs defaults,nofail 0 0' \
      >> /etc/fstab

# DHCP from masuda's host-side bridge, same as the main sandbox VM: the
# command may need to pull container images or fetch dependencies, and that
# traffic goes through the same declared+approved egress allowlist
# (Issue #11) as everything else.
RUN mkdir -p /etc/systemd/network \
 && printf '%s\n' '[Match]' 'Name=eth0' '' '[Network]' 'DHCP=yes' \
      > /etc/systemd/network/20-dhcp.network

# /etc/resolv.conf carries whatever `docker build` baked in, scoped to the
# build host's network namespace, and nothing re-mounts it at VM boot. The
# sysinit-layer placement is required, not stylistic (ADR-0051): at the
# multi-user layer systemd detects an ordering cycle against
# systemd-resolved and silently drops resolved's start job.
RUN cat > /etc/systemd/system/resolv-conf.service <<'EOF'
[Unit]
Description=Point /etc/resolv.conf at systemd-resolved's stub
DefaultDependencies=no
Before=sysinit.target systemd-resolved.service
ConditionPathIsSymbolicLink=!/etc/resolv.conf

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'rm -f /etc/resolv.conf && ln -s /run/systemd/resolve/stub-resolv.conf /etc/resolv.conf'
RemainAfterExit=yes

[Install]
WantedBy=sysinit.target
EOF

# `systemctl enable` only writes symlinks on disk, so it works inside
# `docker build` with no systemd running.
RUN systemctl enable systemd-networkd.service systemd-resolved.service resolv-conf.service docker.service

# ---------------------------------------------------------------------------
# Add your command's toolchain below.
#
# masuda ships no language toolchains here on purpose: what a privileged
# command needs is your project's business, and every package added here is
# also part of what runs with root in this VM. Install exactly what the
# declared command needs -- e.g.:
#
#   RUN apt-get update && apt-get install -y --no-install-recommends golang-go \
#    && rm -rf /var/lib/apt/lists/*
# ---------------------------------------------------------------------------

WORKDIR /workspace
