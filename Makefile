# Copyright 2025 Gorizond.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#     http://www.apache.org/licenses/LICENSE-2.0
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions
# and limitations under the License.

SHELL := /usr/bin/env bash -o pipefail
.SHELLFLAGS := -ec

TAG ?= dev
IMAGE_REGISTRY ?= ghcr.io/gorizond
IMAGE_NAME ?= capi-vip-allocator
IMAGE ?= $(IMAGE_REGISTRY)/$(IMAGE_NAME)

ARCH ?= $(shell go env GOARCH)
ALL_ARCH ?= amd64 arm64

BIN_DIR ?= bin
BIN ?= $(BIN_DIR)/$(IMAGE_NAME)

RELEASE_DIR ?= out
LD_FLAGS ?= -s -w

.PHONY: all
all: build

##@ Development

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: fmt vet ## Run tests.
	go test ./...

.PHONY: lint
lint: ## Run golangci-lint against code.
	golangci-lint run ./...

.PHONY: generate
generate: ## Run go generate against code (no-op if not used).
	go generate ./...

.PHONY: manifests
manifests: ## Placeholder for manifest generation.
	@echo "No manifests to generate."

.PHONY: build
build: fmt vet ## Build the controller binary.
	mkdir -p $(BIN_DIR)
	go build -trimpath -o $(BIN) ./cmd/capi-vip-allocator

##@ Docker

.PHONY: docker-build
docker-build: ## Build docker image for the current architecture.
	DOCKER_BUILDKIT=1 docker build --build-arg ARCH=$(ARCH) -t $(IMAGE)-$(ARCH):$(TAG) .

.PHONY: docker-build-all
docker-build-all: $(addprefix docker-build-,$(ALL_ARCH)) ## Build docker images for all supported architectures.

.PHONY: docker-build-%
docker-build-%:
	$(MAKE) ARCH=$* docker-build

.PHONY: docker-push
docker-push: ## Push docker image for the current architecture.
	docker push $(IMAGE)-$(ARCH):$(TAG)

.PHONY: docker-push-all
docker-push-all: $(addprefix docker-push-,$(ALL_ARCH)) docker-push-manifest ## Push docker images and multi-arch manifest.

.PHONY: docker-push-%
docker-push-%:
	$(MAKE) ARCH=$* docker-push

.PHONY: docker-push-manifest
docker-push-manifest: ## Create and push a multi-arch manifest.
	docker manifest create --amend $(IMAGE):$(TAG) $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(IMAGE)\-&:$(TAG)~g")
	@for arch in $(ALL_ARCH); do docker manifest annotate --arch $${arch} $(IMAGE):$(TAG) $(IMAGE)-$${arch}:$(TAG); done
	docker manifest push --purge $(IMAGE):$(TAG)

##@ Release

.PHONY: clean-release
clean-release: ## Remove release artifacts.
	rm -rf $(RELEASE_DIR)

.PHONY: release
release: clean-release ## Build release archives for all supported architectures.
	mkdir -p $(RELEASE_DIR)
	@for arch in $(ALL_ARCH); do \
		output="$(IMAGE_NAME)-linux-$${arch}"; \
		GOOS=linux GOARCH=$${arch} go build -trimpath -ldflags "$(LD_FLAGS)" -o $(RELEASE_DIR)/$${output} ./cmd/capi-vip-allocator; \
		( cd $(RELEASE_DIR) && tar -czf $${output}.tar.gz $${output} && rm $${output} ); \
	done
	cd $(RELEASE_DIR) && shasum -a 256 * > checksums.txt
