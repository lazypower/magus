FROM quay.io/fedora/fedora-bootc:43

# System packages — GPU stack, firmware, container runtime, brew deps
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
        podman \
        skopeo \
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

# System configuration
COPY config/environment.d/10-rocm.conf /usr/lib/environment.d/10-rocm.conf
COPY config/profile.d/rocm.sh          /etc/profile.d/rocm.sh
COPY config/profile.d/brew.sh          /etc/profile.d/brew.sh
COPY config/profile.d/ollama.sh        /etc/profile.d/ollama.sh
COPY config/udev/70-amdgpu.rules       /usr/lib/udev/rules.d/70-amdgpu.rules
COPY config/modprobe.d/amdgpu.conf     /usr/lib/modprobe.d/amdgpu.conf

# Auto-update schedule — Tuesdays at 4am Central
COPY config/systemd/bootc-fetch-apply-updates.timer.d/schedule.conf \
     /usr/lib/systemd/system/bootc-fetch-apply-updates.timer.d/schedule.conf

# Quadlet container service definitions
COPY config/quadlets/ollama.container    /usr/share/containers/systemd/ollama.container
COPY config/quadlets/vllm.container      /usr/share/containers/systemd/vllm.container

# Validate bootc image structure
RUN bootc container lint
