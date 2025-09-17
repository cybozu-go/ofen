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

| Key                                  | Type   | Default                                       | Description                                                                        |
| ------------------------------------ | ------ | --------------------------------------------- | ---------------------------------------------------------------------------------- |
| crds.enabled                         | bool   | `true`                                        | Install and update CRDs as part of the Helm chart.                                 |
| crds.keep                            | bool   | `true`                                        | Keep CRDs when uninstalling the chart.                                             |
| controller.replicas                  | int    | `2`                                           | Number of replicas for the ofen-controller Deployment.                             |
| controller.image.repository          | string | `"ghcr.io/cybozu-go/ofen"`                    | The ofen-controller image repository to use.                                       |
| controller.image.pullPolicy          | string | `"IfNotPresent"`                              | The ofen-controller image pull policy.                                             |
| controller.image.tag                 | string | `""`                                          | The ofen-controller image tag to use.                                              |
| controller.imagePullSecrets          | list   | `[]`                                          | Secrets for pulling the ofen-controller image from a private repository.           |
| controller.resources                 | object | `{"requests":{"cpu":"100m","memory":"20Mi"}}` | Resource requests and limits for the ofen-controller Deployment.                   |
| controller.extraArgs                 | list   | `[]`                                          | Additional command line arguments to pass to the ofen-controller binary.           |
| controller.nodeSelector              | object | `{}`                                          | NodeSelector used by the ofen-controller.                                          |
| controller.affinity                  | object | `{}`                                          | Affinity used by the ofen-controller.                                              |
| controller.tolerations               | list   | `[]`                                          | Tolerations used by the ofen-controller.                                           |
| controller.topologySpreadConstraints | list   | `[]`                                          | Topology spread constraints used by the ofen-controller.                           |
| controller.priorityClassName         | string | `""`                                          | PriorityClass used by the ofen-controller.                                         |
| daemon.image.repository              | string | `"ghcr.io/cybozu-go/ofend"`                   | The ofen-daemon image repository to use.                                           |
| daemon.image.pullPolicy              | string | `"IfNotPresent"`                              | The ofen-daemon image pull policy.                                                 |
| daemon.image.tag                     | string | `""`                                          | The ofen-daemon image tag to use.                                                  |
| daemon.imagePullSecrets              | list   | `[]`                                          | Secrets for pulling the ofen-daemon image from a private repository.               |
| daemon.resources                     | object | `{"requests":{"cpu":"100m","memory":"20Mi"}}` | Resource requests and limits for the ofen-daemon DaemonSet.                        |
| daemon.extraArgs                     | list   | `[]`                                          | Additional command line arguments to pass to the ofen-daemon binary.               |
| daemon.containerdSockPath            | string | `"/run/containerd/containerd.sock"`           | Path to the containerd socket.                                                     |
| daemon.containerdHostDirPath         | string | `"/etc/containerd/certs.d"`                   | Path to the host directory where containerd certificate configurations are stored. |
| daemon.nodeSelector                  | object | `{}`                                          | Node labels for scheduling the ofen-daemon.                                        |
| daemon.affinity                      | object | `{}`                                          | Affinity used by the ofen-daemon.                                                  |
| daemon.tolerations                   | list   | `[]`                                          | Tolerations used by the ofen-daemon.                                               |
| daemon.topologySpreadConstraints     | list   | `[]`                                          | Topology spread constraints used by the ofen-daemon.                               |
| daemon.priorityClassName             | string | `""`                                          | PriorityClass used by the ofen-daemon.                                             |
| allowRegistries                      | list   | `[]`                                          | Allow pulling images from specified registries.                                    |

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
