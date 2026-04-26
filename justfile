image_name  := "magus"
image_tag   := "latest"
registry    := "ghcr.io/lazypower"

# Image references
os_ref      := registry + "/" + image_name + ":" + image_tag
ollama_ref  := registry + "/" + image_name + "-ollama:" + image_tag
llama_ref   := registry + "/" + image_name + "-llama-cpp:" + image_tag
vllm_ref    := registry + "/" + image_name + "-vllm:" + image_tag

# --- OS substrate ---

# Build the bootc OS image
build:
    podman build \
        --tag {{ image_name }}:{{ image_tag }} \
        --tag {{ os_ref }} \
        -f Containerfile \
        .

# --- Compute containers ---

# Build the Ollama container (Vulkan + coopmat)
ollama:
    podman build \
        --tag {{ image_name }}-ollama:{{ image_tag }} \
        --tag {{ ollama_ref }} \
        -f containers/ollama/Containerfile \
        containers/ollama

# Build the llama.cpp container (Vulkan + coopmat)
llama-cpp:
    podman build \
        --tag {{ image_name }}-llama-cpp:{{ image_tag }} \
        --tag {{ llama_ref }} \
        -f containers/llama-cpp/Containerfile \
        containers/llama-cpp

# Build the vLLM container (ROCm)
vllm:
    podman build \
        --tag {{ image_name }}-vllm:{{ image_tag }} \
        --tag {{ vllm_ref }} \
        -f containers/vllm/Containerfile \
        containers/vllm

# Build everything
all: build ollama llama-cpp vllm

# --- Push ---

# Push OS image to registry
push: build
    podman push {{ os_ref }}

# Push Ollama container to registry
push-ollama: ollama
    podman push {{ ollama_ref }}

# Push llama.cpp container to registry
push-llama-cpp: llama-cpp
    podman push {{ llama_ref }}

# Push vLLM container to registry
push-vllm: vllm
    podman push {{ vllm_ref }}

# Push everything
push-all: push push-ollama push-llama-cpp push-vllm

# --- Utilities ---

# Run hadolint on the Containerfile
lint:
    podman run --rm -i docker.io/hadolint/hadolint < Containerfile

# Transpile Butane config to Ignition JSON
ignition:
    butane --files-dir config/butane --strict \
        config/butane/magus.bu > ignition.json

# Remove generated files
clean:
    rm -f ignition.json
