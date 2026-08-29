# --- Stage 1: build the baked management agents ---
# Two Go binaries the substrate carries: magus (workload reconciler) and labmap
# (host discovery). Both live in their own repos and build in-tree from their
# GitHub canonical remotes — each module's declared path is gitea-internal
# (RFC1918, unreachable from the GitHub build runners), but an in-tree build
# only needs the source, so the path mismatch is irrelevant. Pin each to a
# commit for reproducible images and bump deliberately. Static linux/amd64
# binaries, stripped, no toolchain in the final image.
FROM docker.io/library/golang:1.26.6 AS agents

ARG MAGUS_CLI_REPO=https://github.com/lazypower/magus-cli
# renovate: datasource=github-commits depName=lazypower/magus-cli branch=main
ARG MAGUS_CLI_REF=667c1e4d524775e4e8767e65abf132d1f46ba283
RUN git clone "${MAGUS_CLI_REPO}" /src/magus \
    && git -C /src/magus checkout "${MAGUS_CLI_REF}" \
    && cd /src/magus \
    && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
       go build -ldflags="-s -w" -o /out/magus ./cmd/magus

# labmap — host discovery agent. LLM-focused magus bakes it (chosen over a day-2
# container: `labmap serve` is a host agent, cleaner as a native binary than a
# socket-mounted container). Substrate-owned: the fleet policy deny-lists
# labmap-agent.* so magus never manages it as a workload.
ARG LABMAP_REPO=https://github.com/lazypower/labmap
# renovate: datasource=github-commits depName=lazypower/labmap branch=main
ARG LABMAP_REF=294d4aecb2219be808ce1d4908024b0a566b0c1e
RUN git clone "${LABMAP_REPO}" /src/labmap \
    && git -C /src/labmap checkout "${LABMAP_REF}" \
    && cd /src/labmap \
    && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
       go build -ldflags="-s -w" -o /out/labmap .

# --- Stage 2: bootc OS image ---
FROM quay.io/fedora/fedora-coreos:stable

# System packages — GPU stack, firmware, brew deps
# FCOS already ships: podman, skopeo, systemd, sshd, ignition, coreos-installer
RUN --mount=type=cache,target=/var/cache/libdnf5 \
    dnf install -y \
        rocm-runtime \
        rocm-core \
        rocm-smi \
        rocminfo \
        mesa-vulkan-drivers \
        vulkan-loader \
        vulkan-tools \
        linux-firmware \
        fwupd \
        power-profiles-daemon \
        ramalama \
        htop \
        tmux \
        git \
        curl \
        wget \
        zsh \
        gcc \
        gcc-c++ \
        make \
        procps-ng \
        file \
    && dnf clean all

# Hostname
RUN echo "magus" > /etc/hostname

# Passwordless sudo for wheel group
RUN echo '%wheel ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/wheel-nopasswd \
    && chmod 0440 /etc/sudoers.d/wheel-nopasswd

# Default Brewfile
COPY Brewfile /usr/share/magus/Brewfile

# System configuration
COPY config/environment.d/10-rocm.conf /usr/lib/environment.d/10-rocm.conf
COPY config/profile.d/rocm.sh          /etc/profile.d/rocm.sh
COPY config/profile.d/brew.sh          /etc/profile.d/brew.sh
COPY config/profile.d/ollama.sh        /etc/profile.d/ollama.sh
COPY config/udev/70-amdgpu.rules       /usr/lib/udev/rules.d/70-amdgpu.rules
COPY config/modprobe.d/amdgpu.conf     /usr/lib/modprobe.d/amdgpu.conf

# First-boot provisioning (oneshot services)
COPY config/systemd/magus-provision-user.service /usr/lib/systemd/system/magus-provision-user.service
COPY config/systemd/magus-provision-data.service /usr/lib/systemd/system/magus-provision-data.service
COPY config/systemd/magus-provision-brew.service /usr/lib/systemd/system/magus-provision-brew.service
RUN systemctl enable magus-provision-user.service magus-provision-data.service magus-provision-brew.service

# Auto-update schedule — Tuesdays at 4am Central
COPY config/systemd/bootc-fetch-apply-updates.timer.d/schedule.conf \
     /usr/lib/systemd/system/bootc-fetch-apply-updates.timer.d/schedule.conf

# Container tag refresh for quadlets labelled AutoUpdate=registry, daily at 2am
# Central.
COPY config/systemd/podman-auto-update.timer.d/schedule.conf \
     /usr/lib/systemd/system/podman-auto-update.timer.d/schedule.conf

# Both update paths ship disabled by preset, so configuring their timers is not
# enough. Enable them explicitly or the drop-ins above tune a timer that never
# runs. zincati is masked instead: it is the stock FCOS updater and cannot act
# on a deployment sourced from a custom bootc registry image, so it only logs
# "not found in the update graph" every few minutes while reporting active.
RUN systemctl enable bootc-fetch-apply-updates.timer podman-auto-update.timer \
 && systemctl mask zincati.service

# Quadlet container service definitions
COPY config/quadlets/ollama.container    /usr/share/containers/systemd/ollama.container
COPY config/quadlets/vllm.container      /usr/share/containers/systemd/vllm.container

# Management agents — baked, immutable. /usr/bin because this is a golden image.
# magus is the workload reconciler; labmap is the host discovery agent.
COPY --from=agents /out/magus  /usr/bin/magus
COPY --from=agents /out/labmap /usr/bin/labmap

# labmap host agent — `labmap serve` on :9999. Substrate-owned, NOT a magus
# workload (the fleet policy deny-lists labmap-agent.*). An optional persisted
# agent API key arrives at install via /etc/core/labmap.env — never baked.
COPY config/systemd/labmap-agent.service /usr/lib/systemd/system/labmap-agent.service

# core-reconcile layer — the workload GitOps loop. This host is a core-fleet
# member: core-reconcile pulls hosts/magus/workload.bu from core-fleet and runs
# `magus apply` on a timer. Infra specifics stay out of the image — the fleet
# repo pointer arrives at install via Ignition (/etc/core/reconcile.env), never
# baked here.
#
# policy.yaml is the magus authority's FAIL-CLOSED FALLBACK only. The live
# authority (fleet default + per-host overrides) lives in core-fleet, resolved
# per-host by core-reconcile; this baked copy is used solely when a host can't
# yet reach the fleet. Keep its rules in lockstep with core-fleet's root
# policy.yaml (bump both together).
COPY config/magus/policy.yaml             /etc/magus/policy.yaml
COPY config/scripts/core-reconcile.sh     /usr/libexec/core-reconcile
COPY config/systemd/core-reconcile.service /usr/lib/systemd/system/core-reconcile.service
COPY config/systemd/core-reconcile.timer   /usr/lib/systemd/system/core-reconcile.timer
RUN chmod 0755 /usr/libexec/core-reconcile \
    && systemctl enable core-reconcile.timer labmap-agent.service

# Validate bootc image structure
RUN bootc container lint
