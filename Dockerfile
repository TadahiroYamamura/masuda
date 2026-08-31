# Builder stage: masuda's own CLI binary, so orchestrator/*.py (Issue #35's
# phase A) can shell out to `masuda internal state ...` inside the container
# instead of embedding its own MCP client. The rest of this image has no Go
# toolchain at all -- this stage exists purely to produce the one binary the
# final stage copies out, and is discarded after.
FROM golang:1.26 AS masuda-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/masuda ./cmd/masuda

FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

# Node.js 22 (LTS) + system tools
# build-essential: a C toolchain is needed across all language variants, not
# just one -- Go's `go test -race`/cgo, Python packages without prebuilt
# wheels, and Node native addons (node-gyp) all fall back to compiling from
# source. Baking it in here once (ADR-0022) avoids repeating the same RUN
# line in docker/{go,python,typescript}/Dockerfile.
#
# inotify-tools is deliberately NOT installed here -- the gate-wait step it
# used to back (ADR-0017's single blocking `inotifywait` call) was replaced
# by a `mcp__masuda-gate__wait_for_gate_resolution` MCP tool call, backed by the
# workspace's state daemon rather than a watched file (Issue #35).
#
# systemd + kmod are for the VM boot path only (Issue #31 M5-1) -- Docker
# never invokes systemd as PID1 (ENTRYPOINT stays entrypoint.sh below), so
# installing them has no effect on the Docker path. kmod (modprobe/depmod)
# is what lets systemd-udevd auto-load virtiofs.ko when the VM's virtio-fs
# PCI device is detected; without it there's no working module autoload and
# a VM boot would need its own ad hoc module-loading step instead.
#
# sudo is also VM boot path only (Issue #31 M5-6) -- VMBackend.Stop() SSHes
# in and runs `sudo systemctl poweroff` for a clean guest shutdown before
# tearing down the VM process, since an abrupt kill was confirmed live to
# corrupt the disk image (no chance for the guest to unmount/sync). A
# plain `systemctl poweroff` without root was tried first and denied --
# logind's default polkit policy only allows it for an "active" (seat-
# attached) session, which an SSH session isn't. The /etc/sudoers.d rule
# below is scoped to poweroff only, not general sudo access, and only
# takes effect for a process invoked as ubuntu -- Docker's ENTRYPOINT
# doesn't grant that shell any credential to sudo with, so this has no
# practical effect there either.
#
# openssh-server is also VM boot path only (Issue #31 M5-5) -- `masuda chat`
# has no `docker exec` equivalent for a VM, so it SSHes in instead. Its own
# apt postinst runs `ssh-keygen -A` once at image build time, baking host
# keys into this image that every container/VM built from it would share;
# deleting them here means each VM instead gets its own, generated fresh on
# first boot by runtime/ssh-host-keys.service (confirmed live: ssh.service
# itself has no such regeneration built in -- with the keys just missing, it
# fails outright, so this isn't optional). This is about the *server's* host
# key identity, a different key from the *client* authorized_keys key
# discussed below, and not a secret (it authenticates the VM to the
# connecting client, not the other way around).
RUN apt-get update \
 && apt-get install -y ca-certificates curl gnupg build-essential \
 && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
 && apt-get install -y nodejs python3 python3-venv tmux ttyd git systemd systemd-sysv kmod openssh-server sudo \
 && rm -f /etc/ssh/ssh_host_* \
 && echo 'ubuntu ALL=(root) NOPASSWD: /usr/bin/systemctl poweroff' > /etc/sudoers.d/masuda-vm-poweroff \
 && chmod 0440 /etc/sudoers.d/masuda-vm-poweroff \
 && rm -rf /var/lib/apt/lists/*

# Claude CLI
RUN npm install -g @anthropic-ai/claude-code

# ubuntu user (uid=1000) already exists in ubuntu:24.04.
# Using it avoids root restriction and matches host file ownership on mounted ~/.claude/.credentials.json (mode 600)
#
# masuda's own control files (venv/orchestrator/runtime) live under /opt/masuda, not
# /workspace: /workspace is reserved as the bind-mount point for the target repository's
# worktree (masuda sandbox start mounts a different worktree there per run), and a bind
# mount replaces the mount point's entire contents — anything baked in at /workspace would
# be shadowed the moment a worktree is mounted over it. /masuda-state is a second,
# separate bind-mount point for that workspace's state directory (roadmap step 7):
# masuda's own TASK.md/PLAN.md/gate markers/etc. live there instead of in /workspace, so
# they never show up in the target repository's own `git status`.
RUN mkdir -p /workspace /masuda-state /opt/masuda /home/ubuntu/.claude \
 && chown ubuntu:ubuntu /workspace /masuda-state /opt/masuda /home/ubuntu/.claude

WORKDIR /opt/masuda

# Python venv + LangGraph (baked into image)
COPY --chown=ubuntu:ubuntu requirements.txt ./
RUN python3 -m venv venv \
 && venv/bin/pip install --no-cache-dir -r requirements.txt

# Project files
# runtime/CLAUDE.md (loop protocol) is intentionally not baked in here — it belongs at
# ~/.claude/CLAUDE.md, placed at container startup by masuda sandbox start (masuda CLI's
# job, not the image build), so it can be iterated on without rebuilding the image.
# See docs/adr/0007-loop-protocol-claude-md-in-user-scope.md
COPY --chown=ubuntu:ubuntu orchestrator/ orchestrator/
COPY --chown=ubuntu:ubuntu runtime/entrypoint.sh runtime/start_claude.sh runtime/merge_claude_settings.py runtime/
RUN chmod +x runtime/start_claude.sh runtime/entrypoint.sh runtime/merge_claude_settings.py

# VM boot path only (Issue #31 M5-1) -- see runtime/masuda-loop.service and
# runtime/fstab.vm's own comments for why these are inert under Docker.
# `systemctl enable` only edits a symlink on disk; it doesn't need systemd
# actually running, so it's safe inside `docker build`.
COPY runtime/masuda-loop.service /etc/systemd/system/masuda-loop.service
COPY runtime/fstab.vm /tmp/fstab.vm
COPY runtime/vm-dhcp.network /etc/systemd/network/20-dhcp.network
COPY runtime/ssh-host-keys.service /etc/systemd/system/ssh-host-keys.service
COPY runtime/resolv-conf.service /etc/systemd/system/resolv-conf.service
# ttyd.service: the ttyd apt package enables its own unit by default
# (127.0.0.1:7681, -O login) -- masuda doesn't use it, entrypoint.sh starts
# its own ttyd on :7682 instead, so disable the package's to avoid running a
# second, unused ttyd nobody asked for.
RUN cat /tmp/fstab.vm >> /etc/fstab \
 && rm /tmp/fstab.vm \
 && systemctl enable masuda-loop.service \
 && systemctl enable systemd-networkd.service \
 && systemctl enable systemd-resolved.service \
 && systemctl enable ssh.service \
 && systemctl enable ssh-host-keys.service \
 && systemctl enable resolv-conf.service \
 && systemctl disable ttyd.service

# masuda CLI binary (see the masuda-builder stage above) -- orchestrator/*.py
# shells out to `masuda internal state ...` to reach this workspace's state
# daemon over its UDS socket at /masuda-state/daemon.sock.
COPY --from=masuda-builder /out/masuda /usr/local/bin/masuda

USER ubuntu

# Install Claude native binary as ubuntu user (baked into image)
ENV PATH="/home/ubuntu/.local/bin:$PATH"
RUN claude install

# Register (but don't install anything from) the official plugin marketplace,
# so language-variant images (Dockerfile.go, Dockerfile.python, ...) built
# `FROM` this one only need a plain `claude plugin install <name>@claude-
# plugins-official`, not a marketplace add too (roadmap step 8). Confirmed
# empirically that both `marketplace add` and `plugin install` need no
# Claude Code auth at all -- they're just a `git clone` of the (public)
# marketplace repo and a copy out of its cache, so this is safe to bake in
# at build time despite ADR-0001's no-API-billing constraint (nothing here
# talks to the Anthropic API). Plugin/marketplace state lands in
# ~/.claude/settings.json and ~/.claude/plugins/, both of which stay exactly
# as baked: nothing from the host reaches the guest's Claude configuration.
# The only thing that crosses the boundary is the `claude setup-token` OAuth
# token, handed over as CLAUDE_CODE_OAUTH_TOKEN via a separate read-only
# share (internal/sandbox/claudetoken.go, runtime/entrypoint.sh).
#
# This used to read "neither is one of the two files sandbox.Start
# bind-mounts from the host (~/.claude.json, ~/.claude/.credentials.json)",
# which stopped being true when ADR-0044 removed the Docker execution
# runtime -- a VM guest is a different kernel and shares no such files. It
# is called out rather than quietly deleted because that stale sentence was
# read as current at least once and produced a wrong analysis (Issue #45).
RUN claude plugin marketplace add anthropics/claude-plugins-official

# ttyd web terminal port (mapped to a per-container host port by masuda sandbox start,
# since multiple sandboxes run in parallel)
EXPOSE 7682

WORKDIR /workspace

ENTRYPOINT ["/opt/masuda/runtime/entrypoint.sh"]
