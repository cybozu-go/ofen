# Ofen Helm Chart

## How to use Ofen Helm repository

You need to add this repository to your Helm repositories:

```console
helm repo add ofen https://cybozu-go.github.io/ofen/
helm repo update
```

## Quick start

### Installing the Chart

To install the chart with the release name `ofen` using a dedicated namespace(recommended):

```console
$ helm install --create-namespace --namespace ofen-system ofen ofen/ofen
```

Specify parameters using `--set key=value[,key=value]` argument to `helm install`.

Alternatively a YAML file that specifies the values for the parameters can be provided like this:

```console
$ helm install --create-namespace --namespace ofen-system ofen -f values.yaml ofen/ofen
```

## Values

| Key                               | Type   | Default                                       | Description                                                                        |
| --------------------------------- | ------ | --------------------------------------------- | ---------------------------------------------------------------------------------- |
| crds.enabled                      | bool   | `true`                                        | Install and update CRDs as part of the Helm chart.                                 |
| crds.keep                         | bool   | `true`                                        | Keep existing CRDs during uninstallation.                                          |
| controller.replicas               | int    | `2`                                           | Number of replicas for the ofen-controller Deployment.                             |
| controller.image.repository       | string | `"ghcr.io/cybozu-go/ofen"`                    | ofen-controller image repository to use.                                           |
| controller.image.pullPolicy       | string | `"IfNotPresent"`                              | ofen-controller image pull policy.                                                 |
| controller.image.tag              | string | `""`                                          | ofen-controller image tag to use.                                                  |
| controller.leaderElection.enabled | bool   | `true`                                        | Enable leader election for the ofen-controller.                                    |
| controller.imagePullSecrets       | list   | `[]`                                          | Secrets for pulling the ofen-controller image from a private repository.           |
| controller.resources              | object | `{"requests":{"cpu":"100m","memory":"20Mi"}}` | Resource requests and limits for the ofen-controller Deployment.                   |
| controller.extraArgs              | list   | `[]`                                          | Additional command line arguments to pass to the ofen-controller binary.           |
| daemon.image.repository           | string | `"ghcr.io/cybozu-go/ofend"`                   | ofen-daemon image repository to use.                                               |
| daemon.image.pullPolicy           | string | `"IfNotPresent"`                              | ofen-daemon image pull policy.                                                     |
| daemon.image.tag                  | string | `""`                                          | ofen-daemon image tag to use.                                                      |
| daemon.imagePullSecrets           | list   | `[]`                                          | Secrets for pulling the ofen-daemon image from a private repository.               |
| daemon.resources                  | object | `{"requests":{"cpu":"100m","memory":"20Mi"}}` | Resource requests and limits for the ofen-daemon DaemonSet.                        |
| daemon.extraArgs                  | list   | `[]`                                          | Additional command line arguments to pass to the ofen-daemon binary.               |
| daemon.containerdSockPath         | string | `"/run/containerd/containerd.sock"`           | Path to the containerd socket.                                                     |
| daemon.containerdHostDirPath      | string | `"/etc/containerd/certs.d"`                   | Path to the host directory where containerd certificate configurations are stored. |
| allowRegistries                   | list   | `[]`                                          | Allow pulling images from some registries.                                         |

## Generate Manifests

You can use the `helm template` command to render manifests.

```console
$ helm template --namespace ofen ofen ofen/ofen
```

## CRD considerations

### Installing or updating CRDs

Ofen Helm Chart installs or updates CRDs by default. If you want to manage CRDs on your own, turn off the `crds.enabled` parameter.

### Removing CRDs

Helm does not remove the CRDs due to the [`helm.sh/resource-policy: keep` annotation](https://helm.sh/docs/howto/charts_tips_and_tricks/#tell-helm-not-to-uninstall-a-resource).
When uninstalling, please remove the CRDs manually.
