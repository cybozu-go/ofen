E2ETEST_K8S_VERSION  := 1.31.6
CERT_MANAGER_VERSION := 1.15.0
SPEGEL_VERSION := 0.0.28
KIND_VERSION := 0.27.0

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
start:
	$(MAKE) -C ../../ docker-build
	$(KIND) create cluster --name=$(KIND_CLUSTER_NAME) --config=$(KIND_CONFIG) --image=kindest/node:v$(E2ETEST_K8S_VERSION) --wait 1m
	$(KIND) load docker-image image-prefetch:dev --name=$(KIND_CLUSTER_NAME)
	$(KUBECTL) apply -f https://github.com/jetstack/cert-manager/releases/download/v$(CERT_MANAGER_VERSION)/cert-manager.yaml
	$(KUBECTL) -n cert-manager wait --for=condition=available --timeout=180s --all deployments
	$(KUBECTL) apply -k ../../config/default
	$(KUBECTL) -n ofen-system wait --for=condition=available --timeout=180s --all deployments
	helm upgrade --create-namespace --namespace spegel --install --version v$(SPEGEL_VERSION) \
		spegel oci://ghcr.io/spegel-org/helm-charts/spegel --wait

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
