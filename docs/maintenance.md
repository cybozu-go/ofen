# Maintenance

## How to update supported Kubernetes

ofen supports the three latest Kubernetes versions. To update the supported Kubernetes versions, please follow the steps below:

1. Update `ENVTEST_K8S_VERSION` in `Makefile`.
2. Update `E2ETEST_K8S_VERSION` and `E2ETEST_KINDEST_NODE_IMAGE_HASH` in `test/e2e/Makefile`.
3. Update `k8s.io/*` and `sigs.k8s.io/controller-runtime` packages in `go.mod`.
4. Update `aqua.yaml` by running `aqua update --select-version`.
5. Run `aqua update-checksum` to update `aqua-checksums.json`.
