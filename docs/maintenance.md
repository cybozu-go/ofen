# Maintenance

## Prerequisites

CLI tools for development and tests are managed by [aqua](https://aquaproj.github.io/) in `aqua.yaml`.
Their checksums are recorded in `aqua-checksums.json` and verified on installation.
The Makefiles execute the tools via `aqua exec`, so install aqua in advance and put the `aqua` command in a directory included in `PATH`.
See [Install aqua](https://aquaproj.github.io/docs/install) for details.
Then run `make setup` to install the tools.

## How to update tools and dependencies

Run the following command regularly.

```console
$ make maintenance
```

This updates the following:

- gh, helm, golangci-lint, yq, kind, kustomize, mdBook, kaptest and the aqua standard registry in `aqua.yaml`, and `aqua-checksums.json`
- aqua version in `.github/workflows/*.yaml`
- `SPEGEL_VERSION` in `Makefile.versions`

Only versions released at least 14 days ago are adopted.
The cooldown period can be changed with `COOLDOWN_DAYS` (e.g. `make maintenance COOLDOWN_DAYS=7`).

The major versions are kept, because major version upgrades may have breaking changes.
Upgrade them manually (see [Notes on editing aqua.yaml manually](#notes-on-editing-aquayaml-manually)).
Note that minor version upgrades are adopted for `v0.x`.

The other packages in `aqua.yaml` (controller-gen, setup-envtest, kubectl and models-schema) depend on the Kubernetes version.
Update them with the following procedure.

## How to update supported Kubernetes

ofen supports the three latest Kubernetes versions. To update the supported Kubernetes versions, please follow the steps below:

1. Update `ENVTEST_K8S_VERSION` in `Makefile.versions`.
2. Update `E2ETEST_K8S_VERSION` and `E2ETEST_KINDEST_NODE_IMAGE_HASH` in `Makefile.versions`.
   Specify the version of `kindest/node` supported by the kind written in `aqua.yaml`.
3. Update `k8s.io/*` and `sigs.k8s.io/controller-runtime` packages in `go.mod`.
4. Update the following packages in `aqua.yaml` manually.
   - `kubernetes-sigs/controller-tools/controller-gen`: the version supporting the Kubernetes version
   - `kubernetes-sigs/controller-runtime/setup-envtest`: the same version as controller-runtime in `go.mod`
   - `kubernetes/kubernetes/kubectl`: the same minor version as `E2ETEST_K8S_VERSION`
   - `kubernetes/kubernetes/pkg/generated/openapi/cmd/models-schema`: the same version as `k8s.io/api` in `go.mod`
5. Update `aqua-checksums.json`. See also [Notes on editing aqua.yaml manually](#notes-on-editing-aquayaml-manually).
   ```console
   $ aqua update-checksum -prune
   ```

## Notes on editing aqua.yaml manually

- Write the version as the tag name, including its prefix if any (e.g. `kubernetes-sigs/kustomize@kustomize/v5.8.3`).
- You can select a version interactively with `aqua update -s <package>` (e.g. `aqua update -s kubernetes/kubernetes/kubectl`).
  Do not run `aqua update` without arguments, because it updates all packages to the latest versions ignoring the cooldown period and the major versions.
- Since `require_checksum` is enabled, packages without checksums fail to install.
  After editing, update `aqua-checksums.json` with `aqua update-checksum -prune`.
