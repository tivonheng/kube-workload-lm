SHELL := /bin/sh

GO ?= go
CONTAINER_ENGINE ?= podman
KUBECTL ?= kubectl
GO_VERSION ?= 1.24.0
VERSION ?= v0.1.0
REGISTRY ?= gitlab.glb.osl-nucleus.com/devops
IMAGE_NAME ?= kube-workload-lifecycle-manager
IMG ?= $(REGISTRY)/$(IMAGE_NAME):$(VERSION)
PLATFORM ?= linux/$(shell $(GO) env GOARCH)
PLATFORMS ?= linux/amd64,linux/arm64
MULTIARCH_IMG ?= $(IMG)-multiarch
ENVTEST_K8S_VERSION ?= 1.34.0
SETUP_ENVTEST_VERSION ?= v0.0.0-20260125163108-a19ec76a3c5d
BUILD_DIR ?= bin
BINARY ?= $(BUILD_DIR)/controller
KUSTOMIZE_DIR ?= config/base
NAMESPACE ?= lifecycle-system
DEPLOYMENT ?= workload-lifecycle-controller

.PHONY: all fmt vet test test-envtest test-race build cross-build manifests validate acceptance image-build image-inspect image-push image-multiarch image-multiarch-inspect image-multiarch-push container-acceptance deploy undeploy clean

all: validate

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

test-envtest:
	set -eu; assets="$$( $(GO) run sigs.k8s.io/controller-runtime/tools/setup-envtest@$(SETUP_ENVTEST_VERSION) use -p path $(ENVTEST_K8S_VERSION))"; KUBEBUILDER_ASSETS="$$assets" $(GO) test ./internal/integration -count=1 -v

test-race:
	$(GO) test -race ./internal/controller ./internal/policy ./internal/state ./internal/workload

build:
	mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 $(GO) build -mod=readonly -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o $(BINARY) ./cmd/controller

cross-build:
	mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -mod=readonly -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o $(BUILD_DIR)/controller-linux-amd64 ./cmd/controller
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -mod=readonly -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o $(BUILD_DIR)/controller-linux-arm64 ./cmd/controller

manifests:
	./hack/validate-manifests.sh

validate: fmt vet test build manifests

acceptance: fmt vet test test-envtest test-race build cross-build manifests container-acceptance

image-build:
	$(CONTAINER_ENGINE) build --platform $(PLATFORM) --build-arg GO_VERSION=$(GO_VERSION) --tag $(IMG) .

image-inspect:
	$(CONTAINER_ENGINE) image inspect $(IMG)
	@test "$$($(CONTAINER_ENGINE) image inspect --format '{{.Config.User}}' $(IMG))" = "65532:65532" || (echo "image user must be 65532:65532" >&2; exit 1)

image-push:
	$(CONTAINER_ENGINE) image push $(IMG)

image-multiarch:
	-$(CONTAINER_ENGINE) manifest rm $(MULTIARCH_IMG)
	$(CONTAINER_ENGINE) build --platform $(PLATFORMS) --build-arg GO_VERSION=$(GO_VERSION) --manifest $(MULTIARCH_IMG) .

image-multiarch-inspect:
	$(CONTAINER_ENGINE) manifest inspect $(MULTIARCH_IMG)
	@$(CONTAINER_ENGINE) manifest inspect $(MULTIARCH_IMG) | $(GO) run ./hack/verify-manifest.go

image-multiarch-push:
	$(CONTAINER_ENGINE) manifest push --all $(MULTIARCH_IMG) $(IMG)

container-acceptance: image-build image-inspect image-multiarch image-multiarch-inspect

deploy: manifests
	$(KUBECTL) apply -k $(KUSTOMIZE_DIR)
	$(KUBECTL) --namespace $(NAMESPACE) set image deployment/$(DEPLOYMENT) controller=$(IMG)

undeploy:
	$(KUBECTL) delete -k $(KUSTOMIZE_DIR) --ignore-not-found=true

clean:
	rm -rf $(BUILD_DIR)
