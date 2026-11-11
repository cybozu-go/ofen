include ../../Makefile.common
include ../../Makefile.versions

PROJECT_DIR := $(CURDIR)/../../

KIND_CLUSTER_NAME := ofen
KIND_CONFIG := kind-config.yaml
export KUBECONFIG

.PHONY: help
help:
	@echo "Choose one of the following target"
	@echo
	@echo "setup      Setup tools"
	@echo "start      Start kind cluster and install accurate"
	@echo "test       Run e2e tests"
	@echo "logs       Save logs as logs.tar.gz"
	@echo "stop       Stop the kind cluster"

.PHONY: setup
setup:
	aqua policy allow ${PROJECT_DIR}/aqua-policy.yaml
	aqua install -c ${PROJECT_DIR}/aqua.yaml -l

.PHONY: start
start:
	$(MAKE) -C ../../ docker-build
	$(KIND) create cluster --name=$(KIND_CLUSTER_NAME) --config=$(KIND_CONFIG) --image=kindest/node:v$(E2ETEST_K8S_VERSION)@$(E2ETEST_KINDEST_NODE_IMAGE_HASH) --wait 1m
	$(MAKE) kind-load
	$(KUBECTL) apply -k ../../config/default
	$(KUBECTL) -n ofen-system wait --for=condition=available --timeout=180s --all deployments
	$(HELM) upgrade --create-namespace --namespace spegel --install spegel --version $(SPEGEL_VERSION) \
		oci://ghcr.io/spegel-org/helm-charts/spegel --wait

.PHONY: kind-load
kind-load:
	$(KIND) load docker-image $(IMG_PREFETCH_CONTROLLER) --name ${KIND_CLUSTER_NAME}
	$(KIND) load docker-image $(IMG_NODEIMAGESET_CONTROLLER) --name ${KIND_CLUSTER_NAME}

# The path of kubectl is passed to the tests with the KUBECTL environment variable.
# `aqua which` only resolves the path and does not install the package, so kubectl is executed once to install it.
.PHONY: test
test:
	$(KUBECTL) version --client > /dev/null
	env KUBECTL="$$(aqua which kubectl)" \
		go test -v -race . -ginkgo.v -ginkgo.fail-fast

.PHONY: logs
logs:
	rm -rf logs
	$(KIND) export logs --name=$(KIND_CLUSTER_NAME) ./logs

.PHONY: stop
stop:
	$(KIND) delete cluster --name=$(KIND_CLUSTER_NAME)
	-docker image rm $(IMG_PREFETCH_CONTROLLER) $(IMG_NODEIMAGESET_CONTROLLER)
