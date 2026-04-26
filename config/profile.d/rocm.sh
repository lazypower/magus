# ROCm tools on PATH for interactive shells
if [ -d /opt/rocm/bin ]; then
    export PATH="/opt/rocm/bin:${PATH}"
fi

# environment.d only covers systemd services — propagate to login shells too
export HSA_OVERRIDE_GFX_VERSION=11.5.1
