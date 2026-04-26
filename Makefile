IMAGE_NAME  := magus
IMAGE_TAG   := latest
REGISTRY    := ghcr.io/lazypower

# Image references
OS_REF      := $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG)
OLLAMA_REF  := $(REGISTRY)/$(IMAGE_NAME)-ollama:$(IMAGE_TAG)
LLAMA_REF   := $(REGISTRY)/$(IMAGE_NAME)-llama-cpp:$(IMAGE_TAG)
VLLM_REF    := $(REGISTRY)/$(IMAGE_NAME)-vllm:$(IMAGE_TAG)

.PHONY: build ollama llama-cpp vllm all \
        push push-ollama push-llama-cpp push-vllm push-all \
        lint ignition clean

# --- OS substrate ---

build:
	podman build \
		--tag $(IMAGE_NAME):$(IMAGE_TAG) \
		--tag $(OS_REF) \
		-f Containerfile \
		.

# --- Compute containers ---

ollama:
	podman build \
		--tag $(IMAGE_NAME)-ollama:$(IMAGE_TAG) \
		--tag $(OLLAMA_REF) \
		-f containers/ollama/Containerfile \
		containers/ollama

llama-cpp:
	podman build \
		--tag $(IMAGE_NAME)-llama-cpp:$(IMAGE_TAG) \
		--tag $(LLAMA_REF) \
		-f containers/llama-cpp/Containerfile \
		containers/llama-cpp

vllm:
	podman build \
		--tag $(IMAGE_NAME)-vllm:$(IMAGE_TAG) \
		--tag $(VLLM_REF) \
		-f containers/vllm/Containerfile \
		containers/vllm

all: build ollama llama-cpp vllm

# --- Push ---

push: build
	podman push $(OS_REF)

push-ollama: ollama
	podman push $(OLLAMA_REF)

push-llama-cpp: llama-cpp
	podman push $(LLAMA_REF)

push-vllm: vllm
	podman push $(VLLM_REF)

push-all: push push-ollama push-llama-cpp push-vllm

# --- Utilities ---

lint:
	podman run --rm -i docker.io/hadolint/hadolint < Containerfile

ignition:
	butane --files-dir config/butane --strict \
		config/butane/magus.bu > ignition.json

clean:
	rm -f ignition.json
