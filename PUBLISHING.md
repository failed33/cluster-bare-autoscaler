# Published packages

This fork packages upstream CBA for installations that need prebuilt images
and a Helm chart. It is based on upstream commit
`748dc5d302b8a728c84a165c81c969b901b4f2e0`, with power lifecycle fixes from
`v0.8.5-fork.2` onward. Upstream's MIT license and copyright
notice are retained in the repository, every image, and the chart.

GitHub Actions tests the source, then publishes these packages for a `v*` tag:

| Package | Registry path |
| --- | --- |
| Controller | `ghcr.io/failed33/cluster-bare-autoscaler/controller` |
| Metrics agent | `ghcr.io/failed33/cluster-bare-autoscaler/metrics-exporter` |
| WOL agent | `ghcr.io/failed33/cluster-bare-autoscaler/wol-agent` |
| Shutdown agent | `ghcr.io/failed33/cluster-bare-autoscaler/poweroff-manager` |
| Helm chart | `oci://ghcr.io/failed33/cluster-bare-autoscaler/charts/cluster-bare-autoscaler` |

All images support `linux/amd64` and `linux/arm64`. They are compiled from the
same commit using `Dockerfile.release`, run as numeric non-root users, and
include source/revision labels, provenance, and an SBOM. The shutdown image
keeps UID/GID 1050 for compatibility with upstream's host socket service.

Image tags include the `v` prefix, such as `v0.8.5-fork.2`. Chart versions omit
it, such as `0.8.5-fork.2`. The chart uses its `appVersion` as the image tag
unless an image tag is explicitly overridden. The chart publishes only after
all four image builds succeed. Consumers should pin a release or digest.

## Install with Flux

Use a Flux `OCIRepository` for the chart and a `HelmRelease` referencing it:

```yaml
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata:
  name: cba
  namespace: flux-system
spec:
  interval: 1h
  url: oci://ghcr.io/failed33/cluster-bare-autoscaler/charts/cluster-bare-autoscaler
  ref:
    tag: 0.8.5-fork.2
  layerSelector:
    mediaType: application/vnd.cncf.helm.chart.content.v1.tar+gzip
    operation: copy
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: cba
  namespace: flux-system
spec:
  interval: 1h
  targetNamespace: cluster-bare-autoscaler
  install:
    createNamespace: true
  chartRef:
    kind: OCIRepository
    name: cba
  values:
    config:
      dryRun: true
```

The default chart enables the controller and metrics agent. It disables both
power agents and sets `shutdownMode` and `powerOnMode` to `disabled`.
Dry-run is an upstream simulation mode, not a Kubernetes read-only permission
boundary. Review upstream behavior before granting it access to a cluster.

Before enabling power management, configure the managed-node labels, WOL MAC
annotation and broadcast address, and install this fork's acknowledged shutdown socket
service on eligible hosts. Set `nodeSelector` or `affinity` for the controller
and `wolAgent.nodeSelector` or `wolAgent.affinity` for the WOL agent so both stay
on always-on nodes. Restrict `shutdownDaemonset.nodeSelector` to eligible hosts.
The WOL agent uses host networking; a Kubernetes NetworkPolicy alone may not
restrict access to its HTTP endpoint.

The chart fixes upstream's duplicate YAML key, stale image tags and
`clusterEvalMode` key, supplies discovery namespaces from the release namespace,
and restarts the controller when its configuration changes. It also exposes
placement controls and uses the WOL agent's own tolerations and pull settings.

Publishing does not establish that automatic shutdown is safe. Validate drain
completion, storage detachment, host shutdown grace, and cold boot separately.
This fork does not add scheduler-pending-pod scaling.

## Power lifecycle fixes in fork.2

- Drain retries PDB denials, waits for pod disappearance, then waits for the
  node's volumes-in-use list and VolumeAttachments to clear. `drainTimeout`
  defaults to 15 minutes. Timeout or API failure aborts shutdown and uncordons.
- Unfinished Jobs, unmanaged pods, non-DaemonSet extended-resource workloads,
  and pods annotated `cluster-autoscaler.kubernetes.io/safe-to-evict: "false"`
  veto draining. CPU load does not establish that such work is idle.
- Shutdown state must be persisted before the host request. Failed requests
  clear that state and restore scheduling. Dry-run performs no power actions.
- The host must acknowledge `shutdown\n` with `accepted\n`; the old upstream
  socket service is incompatible. The updated `poweroff-daemonset/systemd.yaml`
  installs the protocol and schedules poweroff five seconds after acceptance.
- Boot cooldown is persisted in `cba.dev/booted-at` and survives controller
  restarts. Recovery allows two minutes for an accepted shutdown to begin.
- Remaining-node usage is included in resource checks. Missing load samples
  abort aggregation. Chart upgrades replace the sole controller without overlap.

This remains a load-based autoscaler, not a scheduler simulator. Evictable
services must be able to run on the remaining nodes. Keep persistent storage
replicas off optional workers and validate host kubelet shutdown grace and
logind inhibition independently. Host-network WOL needs network access control.


## Release

1. Merge the desired upstream changes and review the packaging diff.
2. Run `make vet unit test-integration`, `actionlint`, and `helm lint --strict helm`.
3. Update `helm/Chart.yaml` for the release, commit, and push a matching tag:
   `git tag v0.8.5-fork.2 && git push origin v0.8.5-fork.2`.
4. Wait for the `Test and publish` workflow to succeed.
5. On first publication, set each of the five GHCR packages to **Public** in
   its GitHub package settings. A public source repository does not make a
   new GHCR package public. This is a one-time step per package.
6. Verify anonymous image access for both architectures and pull/render the
   OCI chart before updating the consuming Flux configuration.

The workflow uses its scoped `GITHUB_TOKEN` for publication. It needs no
personal registry token or Codecov secret. Publishing is limited to tags;
pull requests and pushes to `main` run tests without publishing packages.
