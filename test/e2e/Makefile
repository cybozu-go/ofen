include ../../Makefile.common
E2ETEST_K8S_VERSION  := 1.34.3
SPEGEL_VERSION := 0.6.0
KIND_VERSION := 0.31.0

PROJECT_DIR := $(CURDIR)/../../
BIN_DIR := $(PROJECT_DIR)/bin

CURL := curl -sSLf
KUBECTL := $(BIN_DIR)/kubectl
KIND := $(BIN_DIR)/kind

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
	mkdir -p $(BIN_DIR)
	$(CURL) -o  $(BIN_DIR)/kubectl https://dl.k8s.io/release/v$(E2ETEST_K8S_VERSION)/bin/linux/amd64/kubectl && chmod a+x $(BIN_DIR)/kubectl
	GOBIN=$(BIN_DIR) go install sigs.k8s.io/kind@v$(KIND_VERSION)

.PHONY: start
start: setup
	$(MAKE) -C ../../ docker-build
	$(KIND) create cluster --name=$(KIND_CLUSTER_NAME) --config=$(KIND_CONFIG) --image=kindest/node:v$(E2ETEST_K8S_VERSION) --wait 1m
	$(MAKE) kind-load
	$(KUBECTL) apply -k ../../config/default
	$(KUBECTL) -n ofen-system wait --for=condition=available --timeout=180s --all deployments
	helm upgrade --create-namespace --namespace spegel --install spegel --set image.tag=$(SPEGEL_VERSION) \
		oci://ghcr.io/spegel-org/helm-charts/spegel --wait

.PHONY: kind-load
kind-load:
	$(KIND) load docker-image $(IMG_PREFETCH_CONTROLLER) --name ${KIND_CLUSTER_NAME}
	$(KIND) load docker-image $(IMG_NODEIMAGESET_CONTROLLER) --name ${KIND_CLUSTER_NAME}

.PHONY: test
test:
	go test -v -race . -ginkgo.v -ginkgo.fail-fast

.PHONY: logs
logs:
	rm -rf logs
	$(KIND) export logs --name=$(KIND_CLUSTER_NAME) ./logs

.PHONY: stop
stop:
	$(KIND) delete cluster --name=$(KIND_CLUSTER_NAME)
	-docker image rm image-prefetch:dev
