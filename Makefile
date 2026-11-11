include ./Makefile.common
include ./Makefile.versions

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

CONTAINER_TOOL ?= docker

DOCKER_BUILD_ARGS := --build-arg GOPROXY=$(shell go env GOPROXY)
ifneq ($(wildcard $(HOME)/.netrc),)
# To pass Takumi Guard credentials, mount the .netrc file to the container if it exists.
DOCKER_BUILD_ARGS += --secret id=netrc,src=$(HOME)/.netrc
endif

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

.PHONY: manifests
manifests: ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=controller-manager-role crd paths="./..." output:crd:artifacts:config=config/crd/bases
	echo '{{- if .Values.crds.enabled }}' > charts/ofen/templates/generated/crds/crds.yaml
	$(KUSTOMIZE) build config/kustomize-to-helm/overlays/crds | $(YQ) e "." - >> charts/ofen/templates/generated/crds/crds.yaml
	echo '{{- end }}' >> charts/ofen/templates/generated/crds/crds.yaml
	$(KUSTOMIZE) build config/kustomize-to-helm/overlays/templates | $(YQ) e "." -p yaml - > charts/ofen/templates/generated/generated.yaml

.PHONY: generate
generate: ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: check-generated
check-generated:
	$(MAKE) manifests generate k8s-client-gen
	git diff --exit-code --name-only

# refer to https://github.com/statnett/image-scanner-operator/pull/668/files
GO_MODULE = $(shell go list -m)
API_DIRS = $(shell find api -mindepth 1 -type d | sed "s|^|$(shell go list -m)/|" | paste -sd " " )
.PHONY: k8s-client-gen
k8s-client-gen:
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
test: manifests generate fmt vet ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" $(GINKGO) run --skip-file e2e.* -r --coverprofile cover.out -v
	$(KAPTEST) run config/vap/vap.test/kaptest.yaml

# Utilize Kind or modify the e2e tests to load the image locally, enabling compatibility with other vendors.
.PHONY: test-e2e  # Run the e2e tests against a Kind k8s instance that is spun up.
test-e2e:
	go test ./test/e2e/ -v -ginkgo.v

.PHONY: lint
lint: ## Run golangci-lint linter
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: ## Run golangci-lint linter and perform fixes
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
	$(CONTAINER_TOOL) build $(DOCKER_BUILD_ARGS) -t ${IMG_PREFETCH_CONTROLLER} -f ./dockerfiles/Dockerfile.imageprefetch-controller .
	$(CONTAINER_TOOL) build $(DOCKER_BUILD_ARGS) -t $(IMG_NODEIMAGESET_CONTROLLER) -f ./dockerfiles/Dockerfile.nodeimageset-controller .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG_PREFETCH_CONTROLLER}
	$(CONTAINER_TOOL) push ${IMG_NODEIMAGESET_CONTROLLER}

.PHONY: build-installer
build-installer: manifests generate ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	$(KUSTOMIZE) build config/default > dist/install.yaml

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) apply -f -

.PHONY: uninstall
uninstall: manifests ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/default | $(KUBECTL) apply -f -

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

##@ Dependencies

.PHONY: setup
setup: ## Allow aqua policy and install binaries.
	aqua policy allow ./aqua-policy.yaml
	aqua install -l

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
# The CLI tools managed by aqua are defined in Makefile.common.
GINKGO ?= go tool ginkgo
APPLYCONFIGURATION_GEN ?= go tool applyconfiguration-gen

##@ Maintenance

# Only versions released at least COOLDOWN_DAYS days ago are adopted.
# In the jq filters below, timestamps are in seconds, so days are converted with 86400 (= 60 * 60 * 24 seconds per day).
COOLDOWN_DAYS ?= 14

# usage: get-latest-gh OWNER/REPO [TAG_PREFIX]
# Get the highest version tag of the releases (excluding drafts and pre-releases) published at least COOLDOWN_DAYS days ago.
# If TAG_PREFIX is specified, only tags starting with it are considered.
# Only tags in the form of TAG_PREFIX + "[v]X.Y.Z" are considered, because some projects (e.g. spegel)
# do not mark release candidates such as "v0.8.0-rc.1" as pre-releases.
# The tags are compared as versions instead of the publication dates,
# because some projects (e.g. helm) publish patch releases of older major versions after newer ones.
define get-latest-gh
$(eval latest_gh := $(shell $(GH) release list --repo $1 --limit 100 --json tagName,publishedAt,isDraft,isPrerelease \
	--jq '[.[] | select(.isDraft | not) | select(.isPrerelease | not) | select((.publishedAt | fromdateiso8601) < (now - $(COOLDOWN_DAYS) * 86400)) | .tagName | select(startswith("$2")) | select(ltrimstr("$2") | test("^v?[0-9]+(\\.[0-9]+)*$$"))] | sort_by(ltrimstr("$2") | ltrimstr("v") | split(".") | map(tonumber)) | last // empty'))
$(if $(latest_gh),,$(error failed to get the latest release of $1))
endef

# usage: major-prefix VERSION
# Get the prefix of VERSION up to the major version (e.g. "v2." for "v2.11.4", "kustomize/v5." for "kustomize/v5.8.3").
# It is passed to get-latest-gh as TAG_PREFIX to keep the major version, because major version upgrades may
# have breaking changes and should be done manually. Note that minor version upgrades are adopted for v0.x.
major-prefix = $(shell echo '$1' | sed -E 's/^([^0-9]*[0-9]+\.).*/\1/')

# usage: aqua-package-version PACKAGE
# Get the current version of PACKAGE in aqua.yaml.
aqua-package-version = $(shell $(YQ) '.packages[].name | select(test("^$1@")) | sub("^$1@"; "")' aqua.yaml)

# usage: aqua-package-update-expr PACKAGE
# Get the yq expression to update PACKAGE in aqua.yaml to the latest version with the same major version.
# PACKAGE must be the same as the GitHub repository name.
aqua-package-update-expr = $(strip \
	$(call get-latest-gh,$1,$(call major-prefix,$(call aqua-package-version,$1))) \
	(.packages[] | select(.name | test("^$1@"))).name = "$1@$(latest_gh)")

# The yq expression to update the aqua standard registry in aqua.yaml to the latest version with the same major version.
aqua-registry-update-expr = $(strip \
	$(call get-latest-gh,aquaproj/aqua-registry,$(call major-prefix,$(AQUA_REGISTRY_VERSION))) \
	(.registries[] | select(.type == "standard")).ref = "$(latest_gh)")

# Packages in aqua.yaml updated by `make maintenance`.
# The others depend on the Kubernetes version and are updated manually. See docs/maintenance.md.
AQUA_MAINTAINED_PACKAGES := \
	cli/cli \
	mikefarah/yq \
	helm/helm \
	golangci/golangci-lint \
	kubernetes-sigs/kind \
	kubernetes-sigs/kustomize \
	rust-lang/mdBook \
	pfnet/kaptest

AQUA_REGISTRY_VERSION = $(shell $(YQ) '.registries[] | select(.type == "standard") | .ref' aqua.yaml)
AQUA_VERSION = $(shell sed -n -E 's/^ *aqua_version: (.*)$$/\1/p' .github/workflows/ci.yaml | head -n 1)

# update-aqua is completed first, because the other targets use the tools updated by it.
.PHONY: maintenance
maintenance: ## Update the tools and the dependencies for development and tests.
	$(MAKE) update-aqua
	$(MAKE) update-spegel

# GNU make expands the whole recipe before executing its first line.
# So, all the latest versions are retrieved with the current gh and yq before any file is modified.
# aqua.yaml is updated with a single yq command, because aqua cannot execute a new version of yq
# until its checksum is recorded in aqua-checksums.json.
.PHONY: update-aqua
update-aqua: login-gh ## Update the aqua standard registry, the packages in aqua.yaml and the aqua version in CI.
	$(YQ) -i '$(foreach p,$(AQUA_MAINTAINED_PACKAGES),$(call aqua-package-update-expr,$p) |) $(aqua-registry-update-expr)' aqua.yaml
	$(call get-latest-gh,aquaproj/aqua,$(call major-prefix,$(AQUA_VERSION)))
	sed -i -e 's/aqua_version: .*/aqua_version: $(latest_gh)/' .github/workflows/*.yaml
	aqua update-checksum -prune
	aqua install -l

.PHONY: update-spegel
update-spegel: login-gh ## Update the Spegel version used in e2e tests.
	$(call get-latest-gh,spegel-org/spegel,$(call major-prefix,v$(SPEGEL_VERSION)))
	sed -i -e 's/^SPEGEL_VERSION := .*/SPEGEL_VERSION := $(patsubst v%,%,$(latest_gh))/' Makefile.versions

.PHONY: login-gh
login-gh:
	if ! $(GH) auth status 2>/dev/null; then \
		echo; \
		echo '!! You need login to GitHub to proceed. Please follow the next command with "Authenticate Git with your GitHub credentials? (Y)".'; \
		echo; \
		$(GH) auth login -h github.com -p HTTPS -w; \
	fi

.PHONY: logout-gh
logout-gh:
	$(GH) auth logout

.PHONY: book
book:
	rm -rf docs/book
	cd docs; $(MDBOOK) build
