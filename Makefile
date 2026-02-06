include ./Makefile.common
ENVTEST_K8S_VERSION = 1.34.1

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

CONTAINER_TOOL ?= docker

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

.PHONY: manifests
manifests: controller-gen kustomize yq ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=controller-manager-role crd paths="./..." output:crd:artifacts:config=config/crd/bases
	echo '{{- if .Values.crds.enabled }}' > charts/ofen/templates/generated/crds/crds.yaml	
	$(KUSTOMIZE) build config/kustomize-to-helm/overlays/crds | $(YQ) e "." - >> charts/ofen/templates/generated/crds/crds.yaml
	echo '{{- end }}' >> charts/ofen/templates/generated/crds/crds.yaml
	kustomize build config/kustomize-to-helm/overlays/templates | yq e "."  -p yaml - > charts/ofen/templates/generated/generated.yaml

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."
	
.PHONY: check-generate
check-generate:
	$(MAKE) manifests generate k8s-client-gen
	git diff --exit-code --name-only

# refer to https://github.com/statnett/image-scanner-operator/pull/668/files
GO_MODULE = $(shell go list -m)
API_DIRS = $(shell find api -mindepth 1 -type d | sed "s|^|$(shell go list -m)/|" | paste -sd " " )
.PHONY: k8s-client-gen
k8s-client-gen: applyconfiguration-gen models-schema
	@echo "generating"
	$(MODELS_SCHEMA) > /tmp/schema.json
	rm -rf internal/client/applyconfigurations
	$(APPLYCONFIGURATION_GEN) \
        --go-header-file hack/boilerplate.go.txt \
		--output-dir internal/applyconfigurations \
		--output-pkg $(GO_MODULE)/internal/applyconfigurations \
		--openapi-schema /tmp/schema.json \
		$(API_DIRS)
	rm -f /tmp/schema.json

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet envtest ginkgo kaptest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" $(GINKGO) run --skip-file e2e.* -r --coverprofile cover.out -v
	$(KAPTEST) run config/vap/vap.test/kaptest.yaml

# Utilize Kind or modify the e2e tests to load the image locally, enabling compatibility with other vendors.
.PHONY: test-e2e  # Run the e2e tests against a Kind k8s instance that is spun up.
test-e2e:
	go test ./test/e2e/ -v -ginkgo.v

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	$(GOLANGCI_LINT) run --fix

.PHONY: build
build: manifests generate fmt vet k8s-client-gen ## Build manager binary.
	go build -o bin/imageprefetch-controller cmd/imageprefetch-controller/main.go
	go build -o bin/nodeimageset-controller cmd/nodeimageset-controller/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build -t ${IMG_PREFETCH_CONTROLLER} -f ./dockerfiles/Dockerfile.imageprefetch-controller .
	$(CONTAINER_TOOL) build -t $(IMG_NODEIMAGESET_CONTROLLER) -f ./dockerfiles/Dockerfile.nodeimageset-controller .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG_PREFETCH_CONTROLLER}
	$(CONTAINER_TOOL) push ${IMG_NODEIMAGESET_CONTROLLER}

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	$(KUSTOMIZE) build config/default > dist/install.yaml

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/default | $(KUBECTL) apply -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUBECTL ?= kubectl
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
GINKGO = $(LOCALBIN)/ginkgo
APPLYCONFIGURATION_GEN = $(LOCALBIN)/applyconfiguration-gen
MODELS_SCHEMA = $(LOCALBIN)/models-schema
KAPTEST ?= $(LOCALBIN)/kaptest
YQ ?= $(LOCALBIN)/yq

## Tool Versions
KUSTOMIZE_VERSION ?= v5.8.1
CONTROLLER_TOOLS_VERSION ?= v0.20.0
ENVTEST_VERSION ?= release-0.23
GOLANGCI_LINT_VERSION ?= 2.9.0
GINKGO_VERSION ?= v2.28.1
CODE_GENERATOR_VERSION ?= v0.35.1
MODELS_SCHEMA_VERSION ?= v1.35.1
KAPTEST_VERSION ?= v0.1.2
YQ_VERSION ?= v4.52.2

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	curl -sLO "https://github.com/golangci/golangci-lint/releases/download/v$(GOLANGCI_LINT_VERSION)/golangci-lint-$(GOLANGCI_LINT_VERSION)-linux-amd64.tar.gz" && \
	tar -xzf golangci-lint-$(GOLANGCI_LINT_VERSION)-linux-amd64.tar.gz --strip-components=1 -C $(LOCALBIN)  golangci-lint-$(GOLANGCI_LINT_VERSION)-linux-amd64/golangci-lint && \
	rm -f golangci-lint-$(GOLANGCI_LINT_VERSION)-linux-amd64.tar.gz

.PHONY: ginkgo
ginkgo: $(GINKGO) ## Download ginkgo locally if necessary.
$(GINKGO): $(LOCALBIN)
	$(call go-install-tool,$(GINKGO),github.com/onsi/ginkgo/v2/ginkgo,$(GINKGO_VERSION))

.PHONY: models-schema
models-schema: $(LOCALBIN) ## Download models-schema locally if necessary.
	test -s $(LOCALBIN)/models-schema || \
	( \
		mkdir -p /tmp/work-models-schema/pkg/generated/openapi/cmd/models-schema && \
		curl -sSfL https://github.com/kubernetes/kubernetes/archive/$(MODELS_SCHEMA_VERSION).tar.gz | tar -C /tmp/work-models-schema -xzf - --strip-components=1 &&\
		cd /tmp/work-models-schema/pkg/generated/openapi/cmd/models-schema &&\
		GOBIN=$(LOCALBIN) go install \
	)

.PHONY: applyconfiguration-gen
applyconfiguration-gen: $(APPLYCONFIGURATION_GEN) ## Download applyconfiguration-gen locally if necessary.
$(APPLYCONFIGURATION_GEN): $(LOCALBIN)
	$(call go-install-tool,$(APPLYCONFIGURATION_GEN),k8s.io/code-generator/cmd/applyconfiguration-gen,$(CODE_GENERATOR_VERSION))

.PHONY: kaptest
kaptest: $(KAPTEST) ## Download kaptest locally if necessary.
$(KAPTEST): $(LOCALBIN)
	curl -sLO "https://github.com/pfnet/kaptest/releases/download/${KAPTEST_VERSION}/kaptest_Linux_x86_64.tar.gz" && \
	tar -xzf kaptest_Linux_x86_64.tar.gz -C $(LOCALBIN) kaptest && \
	rm -f kaptest_Linux_x86_64.tar.gz

.PHONY: yq
yq: $(YQ) ## Download yq locally if necessary.
$(YQ): $(LOCALBIN)
	curl -sLO "https://github.com/mikefarah/yq/releases/download/$(YQ_VERSION)/yq_linux_amd64.tar.gz" && \
	tar -xzf yq_linux_amd64.tar.gz ./yq_linux_amd64 && \
	mv yq_linux_amd64 $(YQ) && \
	chmod +x $(YQ) && \
	rm -f yq_linux_amd64.tar.gz

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f $(1) || true ;\
GOBIN=$(LOCALBIN) go install $${package} ;\
mv $(1) $(1)-$(3) ;\
} ;\
ln -sf $(1)-$(3) $(1)
endef

# Tool versions
MDBOOK_VERSION = 0.5.2
MDBOOK := $(LOCALBIN)/mdbook

.PHONY: book
book: $(MDBOOK)
	rm -rf docs/book
	cd docs; $(MDBOOK) build

$(MDBOOK):
	mkdir -p $(LOCALBIN)
	curl -fsL https://github.com/rust-lang/mdBook/releases/download/v$(MDBOOK_VERSION)/mdbook-v$(MDBOOK_VERSION)-x86_64-unknown-linux-gnu.tar.gz | tar -C $(LOCALBIN) -xzf -
