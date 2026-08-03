# Kubernetes Workload Lifecycle Manager

A Kubernetes controller that scales Deployments and StatefulSets through their `scale` subresources according to container-image Revision age and timezone-aware down windows. It never edits Pod templates or deletes Workloads, Pods, PVCs, or other application objects.

## Architecture and safety semantics

```text
Policy ConfigMap -> strict loader -> matcher -> Revision/schedule engine
Workload/HPA watches -> controller -> snapshot transaction -> ConfigMap StateStore
                                      |-> scale subresource
                                      `-> JSON logs, probes, Prometheus metrics
```

**Detailed design:** See [docs/design.md](docs/design.md) for the implementation-accurate architecture, reconciliation order, and core business flows.

The controller watches Deployments, StatefulSets, HPAs, and the policy ConfigMap. Events trigger reconciliation and a 30-second full reconciliation closes watch gaps. Policy matching requires kind plus at least one of label selector or name patterns; the unique highest-priority policy wins. A tie at the highest priority is rejected without state or scale mutation. Invalid policy updates are rejected atomically and the last valid policy snapshot remains active.

Only the elected leader reconciles. Every replica serves HTTP, but `/readyz` succeeds only when that replica is the leader, an initial valid policy exists, and state dependencies loaded successfully.

## Revision semantics

A Revision is the sorted canonical set of regular container `{name,image}` pairs. Init and ephemeral containers, replica changes, labels, resource versions, non-image Pod-template fields, `pod-template-hash`, and `controller-revision-hash` do not define Revision identity.

By default, omitting `lifecycle.revision.containers` tracks every regular container. Any application or sidecar image change then starts a new Revision baseline. A non-empty named list tracks only those containers, so an unlisted sidecar image change does not reset age. If a named container is absent, that Workload is skipped rather than partially tracked. UID, tracking definition, or Revision changes establish a new baseline; policy timing and label changes do not reset `firstSeenAt`.

## Prerequisites

- Go 1.24.0 for local builds
- `kubectl` with Kustomize support (or standalone `kustomize`) for manifest validation
- Podman 5.x for the documented local image and multi-architecture manifest path
- A Kubernetes cluster and credentials authorized to install the supplied RBAC

## Build and test

```sh
go test ./...
# Downloads pinned Kubernetes 1.34.0 envtest assets and requires integration execution.
make test-envtest
# Critical-package race checks and static Linux binaries for both supported architectures.
make test-race cross-build
make fmt vet test build manifests
# Full final acceptance, including required envtest and race/cross-build checks.
make acceptance
```

The envtest library is pinned to controller-runtime `v0.22.5`, aligned with Kubernetes 1.34, while the project keeps its Kubernetes modules at `v0.34.10`. The asset helper is pinned by exact pseudo-version in the Makefile. A plain `go test ./...` clearly skips the integration suite only when envtest binaries are not installed; an explicitly invalid `KUBEBUILDER_ASSETS` fails. `make test-envtest` fetches the pinned assets and does not permit that skip.

Proposal section 22 scenario coverage is indexed in [docs/testing-traceability.md](docs/testing-traceability.md).

The native binary is written to `bin/controller`; `make cross-build` writes `bin/controller-linux-amd64` and `bin/controller-linux-arm64`. It uses in-cluster Kubernetes credentials; local execution outside a Pod is not currently supported.
## Container images

The multi-stage container build compiles a static, trimmed Go binary from the repository's `Dockerfile` and copies it into a registry-independent `scratch` runtime as UID/GID 65532. The final image contains only the controller plus the builder's CA certificate bundle for Kubernetes HTTPS and IANA timezone database for policy evaluation. It writes no runtime files and is compatible with the manifest's `readOnlyRootFilesystem: true` setting. The Make targets default to `CONTAINER_ENGINE=podman`; override that variable only with a CLI that supports the same commands and local manifest semantics.

```sh
# Local architecture image plus non-root metadata inspection; neither pushes.
make image-build image-inspect IMG=localhost/workload-lifecycle:v0.1.0

# Build and inspect a local amd64+arm64 manifest list; does not push.
make image-multiarch image-multiarch-inspect \
  IMG=localhost/workload-lifecycle:v0.1.0 \
  MULTIARCH_IMG=localhost/workload-lifecycle:v0.1.0-multiarch

# Run every local container acceptance check.
make container-acceptance IMG=localhost/workload-lifecycle:v0.1.0

# Explicit publishing targets; run only when intended.
make image-push IMG=registry.example.com/platform/workload-lifecycle:v0.1.0
make image-multiarch-push \
  IMG=registry.example.com/platform/workload-lifecycle:v0.1.0 \
  MULTIARCH_IMG=localhost/workload-lifecycle:v0.1.0-multiarch
```

`CONTAINER_ENGINE`, `GO_VERSION`, `VERSION`, `REGISTRY`, `IMAGE_NAME`, `IMG`, `PLATFORM`, `PLATFORMS`, and `MULTIARCH_IMG` are explicit Make variables. The default engine is Podman and the default multi-architecture set is `linux/amd64,linux/arm64`. `image-multiarch` creates only a local manifest; only the targets whose names end in `-push` publish. Building and inspecting a foreign-architecture image is distinct from running it: runtime execution may require emulation configured in the Podman machine, but this controller's static cross-build and manifest validation do not require a successful foreign-architecture runtime smoke test.

## Deploy and remove

Review `config/base/policy-configmap.yaml` before deployment. The base installs namespace-scoped policy/state/Lease access, cluster-wide read/watch access for supported Workloads and HPAs, scale-only writes, the Service, and two controller replicas.

```sh
# Render and validate both base and sample manifests.
make manifests

# Apply config/base and set the Deployment image to IMG.
make deploy IMG=registry.example.com/platform/workload-lifecycle:v0.1.0

kubectl -n lifecycle-system rollout status deployment/workload-lifecycle-controller
kubectl apply -k config/samples

# Remove only the controller base. Samples are separate.
make undeploy
kubectl delete -k config/samples
```

`deploy` uses `kubectl apply -k config/base` followed by an explicit `kubectl set image`; use a digest-qualified `IMG` in controlled environments. To customize namespace, names, image, resources, or policies declaratively, create a Kustomize overlay rather than editing live objects. Keep `POD_NAMESPACE`, policy/state ConfigMap names, Lease namespace/name, RBAC resource names, and overlay names consistent.

## Policy configuration

Policies live in ConfigMap `lifecycle-system/workload-lifecycle-policies`, key `policies.yaml`. The format is strict `lifecycle.example.com/v1alpha1` / `WorkloadLifecyclePolicySet`; unknown fields, explicit nulls, empty selectors, and invalid values reject the entire update. `maxAge` defaults to `72h`; replica targets default to `0`. Targets must be non-negative.

```yaml
apiVersion: lifecycle.example.com/v1alpha1
kind: WorkloadLifecyclePolicySet
spec:
  policies:
    - name: application-container-only
      priority: 100
      target:
        kinds: [Deployment, StatefulSet]
        selector:
          matchLabels:
            lifecycle.example.com/policy: application-only
      lifecycle:
        maxAge: 168h
        revision:
          source: ContainerImages
          containers: [app] # omit this field to track all regular containers
      replicas:
        scheduledDown: 0
        expired: 0
```

The complete default-all and named-container examples, including selector and schedule forms, are in `config/base/policy-configmap.yaml`. Label Workloads or use name patterns to opt in; unmatched Workloads are observed but skipped.

### Name pattern matching

Targets support an optional `namePatterns` field containing Go RE2 regular expressions that match Workload names. Each pattern is automatically anchored for full-match semantics: a configured pattern `pfb\d+` matches only names that entirely satisfy the expression (equivalent to `^(?:pfb\d+)$`). At least one of `selector` or `namePatterns` must be present. When both are configured, matching is `kind AND (selector OR namePatterns)`.

```yaml
# Pure name-based matching without labels
- name: pfb-workloads
  priority: 100
  target:
    kinds: [Deployment, StatefulSet]
    namePatterns:
      - "pfb\\d+"
      - "staging-.*"
  lifecycle:
    maxAge: 72h
```

An empty name (which should not occur for valid Kubernetes resources) causes all name patterns to evaluate as not matched. Invalid regex syntax or empty pattern strings reject the entire PolicySet update at load time.

### Timezone and windows

Each schedule uses an IANA timezone such as `Asia/Shanghai`, not a fixed UTC offset. Persisted timestamps remain UTC RFC3339. Windows use local wall time and half-open `[start,end)` boundaries: the start minute is included and the end minute is excluded. For a cross-midnight window such as Monday 22:00 to 08:00, `startDays: [MON]` covers Monday 22:00 through Tuesday before 08:00. `allDay: true` covers the listed local start days. Daylight-saving transitions follow the selected IANA zone.

Lifecycle expiration takes priority over scheduled down, which takes priority over restoration, which takes priority over active/no-op behavior. `expiresAt` is dynamically `firstSeenAt + current maxAge`.

### Replica snapshot transaction

Before the first downscale, the controller re-reads `scale.spec.replicas`, persists that value as `replicaSnapshot`, and only then updates the scale subresource. The target is `min(configured target, snapshot)`, so lifecycle management never scales up while entering a down state. It does not recapture a snapshot while downscaled.

When a Workload becomes active, scale is restored first and the snapshot is cleared only after that update succeeds. Retries are therefore idempotent across crashes. A new image Revision for the same Workload UID retains an existing snapshot; a new UID discards the old Workload identity. If state is lost, the controller never guesses a restoration value.

### HPA Reject behavior

A Workload targeted by an `autoscaling/v2` HPA is rejected before any state or scale mutation. HPA discovery errors also fail closed. Use either HPA or this controller as the replica owner; do not configure both.

## Runtime configuration

| Environment variable | Default | Purpose |
|---|---|---|
| `POD_NAME` | Pod hostname | Leader-election identity prefix |
| `POD_NAMESPACE` | `lifecycle-system` | Policy/state namespace and default Lease namespace |
| `POLICY_CONFIGMAP` | `workload-lifecycle-policies` | Policy ConfigMap name |
| `STATE_CONFIGMAP` | `workload-lifecycle-state` | State ConfigMap name |
| `LEADER_ELECTION_NAMESPACE` | `POD_NAMESPACE` | Lease namespace |
| `LEADER_ELECTION_NAME` | `workload-lifecycle-controller` | Lease name |
| `OBSERVABILITY_ADDR` | `:8080` | HTTP listen address |

The binary accepts no command-line flags. Configuration changes must keep the Deployment environment and RBAC names synchronized.

## Observability

The HTTP Service exposes port 8080:

- `/healthz`: process/HTTP liveness; returns 200 while serving.
- `/readyz`: leader and dependency readiness; returns 200 only on the active leader.
- `/metrics`: Prometheus text endpoint.

Metrics use the `workload_lifecycle_controller_` prefix: `reconciliations_total`, `decisions_total`, `errors_total`, `policy_reloads_total`, `policy_ready`, `ready`, `state_store_bytes`, `state_store_near_limit`, and `state_store_exceeded`. Labels are bounded and never include individual Workload names. Logs are JSON and include workload identity, selected policy, bounded reason, result, desired replicas, and error details where applicable.

Leader election uses Lease `lifecycle-system/workload-lifecycle-controller` with a 15-second lease, 10-second renew deadline, and 2-second retry period. SIGTERM/SIGINT cancels reconciliation, releases leadership, and allows up to 10 seconds for graceful HTTP/controller shutdown; the manifest grants 30 seconds.
## Security and RBAC

The Deployment runs as UID/GID 65532 with no privilege escalation, all Linux capabilities dropped, `RuntimeDefault` seccomp, and a read-only root filesystem. The `scratch` runtime image contains only the static controller, CA trust bundle, and IANA timezone data copied from the pinned builder.

Cluster RBAC permits `get/list/watch` on Deployments and StatefulSets, `get/update` only on their `scale` subresources, and `get/list/watch` on HPAs. Namespace RBAC permits policy/state ConfigMap reads and watches, state ConfigMap create/update, and Lease create/get/update. Kubernetes cannot constrain `create` by resource name, which is why the namespace-scoped create grants are separate. The controller receives no Workload update/patch/delete or Pod/PVC delete permission.

## State and upgrade compatibility

State is stored in `lifecycle-system/workload-lifecycle-state`, key `states.json`, as a strict document with `formatVersion: 1`. Invalid or unsupported state is never treated as empty. The single-ConfigMap store warns at 80% of Kubernetes' 1 MiB data limit and rejects writes beyond the limit; states with active snapshots are retained.

Before an upgrade, back up the state and policy ConfigMaps, check release compatibility with state format 1, render manifests, and use an immutable image digest. Roll one compatible replica into leadership, verify `/readyz`, logs, state-capacity metrics, and a no-op reconciliation, then finish the rollout. Do not downgrade to a binary that cannot read the current format. Do not delete or hand-reset state to force an upgrade: active snapshots are required for safe restoration.

## Troubleshooting

See [docs/troubleshooting.md](docs/troubleshooting.md) for policy rejection, readiness, HPA conflicts, state capacity/corruption, RBAC and scale errors, watch reconnects, leader election, and evidence-based safe recovery. The primary invariant is: **never guess a lost replica snapshot**.
