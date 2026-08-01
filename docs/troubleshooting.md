# Troubleshooting

Commands below assume the shipped namespace and object names. Override them if your overlay changes `lifecycle-system`, `workload-lifecycle-controller`, `workload-lifecycle-policies`, or `workload-lifecycle-state`. Local container commands use Podman; Make defaults `CONTAINER_ENGINE` to `podman`.

Start with these non-mutating checks:

```sh
kubectl -n lifecycle-system get pods,deploy,svc,cm,lease
kubectl -n lifecycle-system logs deploy/workload-lifecycle-controller --all-containers --tail=200
kubectl -n lifecycle-system get cm workload-lifecycle-policies -o yaml
kubectl -n lifecycle-system get cm workload-lifecycle-state -o jsonpath='{.data.states\.json}'
kubectl auth can-i --as=system:serviceaccount:lifecycle-system:workload-lifecycle-controller get deployments.apps --all-namespaces
kubectl auth can-i --as=system:serviceaccount:lifecycle-system:workload-lifecycle-controller update deployments.apps/scale --all-namespaces
```

Port-forward one Pod for diagnostics. A follower may return 503 from readiness by design.

```sh
kubectl -n lifecycle-system port-forward pod/<pod-name> 8080:8080
curl -fsS http://127.0.0.1:8080/healthz
curl -i http://127.0.0.1:8080/readyz
curl -fsS http://127.0.0.1:8080/metrics
```

## Policy reload rejected or policy conflict

Symptoms include `policy reload event` with `reload-rejected`, `policy-conflict`, `no-policy-snapshot`, and `workload_lifecycle_controller_policy_ready 0`. The decoder rejects unknown fields, explicit nulls, unsupported kinds/sources/operators, empty selectors or named-container lists, invalid durations/time zones/windows, and duplicate names. A new invalid ConfigMap does not replace an existing valid snapshot; on first startup it prevents readiness.

Render and inspect the actual payload at `data.policies.yaml`. Compare it with `config/base/policy-configmap.yaml`; make one correction and reapply. For conflicts, list every matching policy and change selectors or priorities until exactly one highest-priority policy remains. Do not delete state to resolve a policy error.

## No readiness

`/healthz` only proves the process and HTTP server are alive. `/readyz` requires leadership, a valid policy snapshot, and a successfully loaded state document. Check the Lease holder, policy logs, and state errors:

```sh
kubectl -n lifecycle-system get lease workload-lifecycle-controller -o yaml
kubectl -n lifecycle-system get endpoints workload-lifecycle-controller
kubectl -n lifecycle-system logs deploy/workload-lifecycle-controller | grep -E 'policy|state|leader|leadership'
```

With two replicas, only the current leader is ready; this is expected. If no replica becomes ready, verify Lease RBAC and that `states.json` is valid format version 1.
## HPA conflict

The controller deliberately uses Reject behavior for any Deployment or StatefulSet targeted by an `autoscaling/v2` HPA. It fails closed if the cluster-wide HPA lookup itself fails. No baseline, snapshot, or scale update is created while rejected.

```sh
kubectl get hpa -A
kubectl -n <workload-namespace> describe hpa <name>
```

Choose one replica owner. Remove the Workload from lifecycle policy matching, or remove/migrate the HPA. Do not repeatedly force replicas: the two controllers would fight over the same scale subresource.

## Invalid state or capacity pressure

Symptoms include readiness 503 at startup, `invalid state document`, `unsupported state format version`, missing `states.json`, or capacity metrics approaching one:

- `workload_lifecycle_controller_state_store_bytes{scope="state|total"}`
- `workload_lifecycle_controller_state_store_near_limit`
- `workload_lifecycle_controller_state_store_exceeded`

The state decoder is intentionally strict and will not replace malformed or unsupported data with an empty document. Before any repair, stop mutation and take an exact backup:

```sh
kubectl -n lifecycle-system scale deploy/workload-lifecycle-controller --replicas=0
kubectl -n lifecycle-system get cm workload-lifecycle-state -o yaml > workload-lifecycle-state.backup.yaml
```

Validate JSON structure and `formatVersion`; compare with the state schema and the empty example in `config/base/state-configmap.yaml`. Near 80% of the 1 MiB ConfigMap data limit, investigate stale entries and plan a supported storage migration. Entries with a `replicaSnapshot` are safety-critical and must not be removed by ordinary cleanup.

**Never guess a missing or corrupt `replicaSnapshot`, and never replace state with `{"formatVersion":1,"states":{}}` while managed workloads may be downscaled.** Determine intended replicas from an authoritative deployment source, GitOps history, owner confirmation, or a verified state backup. Restore workloads manually only from that evidence. Then restore a known-good complete state document or deliberately remove affected policy labels before restarting the controller.

## Scale or RBAC errors

Look for `forbidden`, `conflict retries exhausted`, `update scale`, or ConfigMap/Lease errors. Confirm the ServiceAccount and exact subresource permissions:

```sh
kubectl -n lifecycle-system get deploy workload-lifecycle-controller -o jsonpath='{.spec.template.spec.serviceAccountName}'
kubectl auth can-i --as=system:serviceaccount:lifecycle-system:workload-lifecycle-controller get statefulsets.apps --all-namespaces
kubectl auth can-i --as=system:serviceaccount:lifecycle-system:workload-lifecycle-controller update statefulsets.apps/scale --all-namespaces
kubectl auth can-i --as=system:serviceaccount:lifecycle-system:workload-lifecycle-controller update configmap/workload-lifecycle-state -n lifecycle-system
kubectl auth can-i --as=system:serviceaccount:lifecycle-system:workload-lifecycle-controller update lease/workload-lifecycle-controller -n lifecycle-system
```

Reapply `config/base` if RBAC drifted. Do not grant broad Deployment/StatefulSet update, patch, delete, or Pod/PVC deletion permissions. Kubernetes conflicts are retried with bounded backoff; persistent conflicts usually indicate another replica owner or external state writer.
## Watch disconnects

Deployment, StatefulSet, HPA, and policy watches can close during API-server restarts, network interruption, or resource-version expiry. The controller logs the disconnect, waits one second, recreates the watch, and still performs a full reconciliation every 30 seconds. Brief disconnects normally need no intervention.

Investigate repeated disconnects with API-server health, network policy, proxy/load-balancer idle timeouts, client throttling, and `get/list/watch` RBAC. Do not restart all replicas simultaneously unless necessary; periodic reconciliation is the safety net after connectivity returns.

## Leader election

Only the Lease holder mutates state and scales workloads. Followers serve `/healthz` and `/metrics` but return 503 on `/readyz`. If no leader is elected, inspect Lease events and permissions:

```sh
kubectl -n lifecycle-system describe lease workload-lifecycle-controller
kubectl -n lifecycle-system get pods -l app.kubernetes.io/name=workload-lifecycle-controller -o wide
kubectl -n lifecycle-system logs deploy/workload-lifecycle-controller | grep -E 'leader|leadership|Lease|forbidden'
```

Verify all replicas use unique `POD_NAME` values and the same Lease namespace/name. The defaults are a 15-second lease duration, 10-second renew deadline, and 2-second retry period. Leadership loss stops the mutation-capable controller and triggers process termination; the Deployment should restart it.

## Safe recovery workflow

1. Freeze automation: scale the controller Deployment to zero and pause any other replica manager or GitOps reconciliation that would obscure evidence.
2. Back up the policy and state ConfigMaps and record current `scale.spec.replicas`, Workload UIDs, images, and HPA targets.
3. Classify the failure: policy-only, RBAC/API connectivity, valid-state capacity, or invalid/lost state.
4. Repair policy/RBAC/connectivity without editing state whenever possible.
5. For state recovery, use only a verified backup or authoritative desired-replica source. Never infer a snapshot from the currently downscaled value.
6. If a snapshot cannot be recovered, remove the Workload from policy control, have its owner restore an authoritative replica count manually, and document the loss before removing or rebuilding only that state entry through an approved migration.
7. Start one controller replica, wait for leadership and readiness, inspect logs/state/scale behavior, then restore the normal replica count.

A safe recovery preserves ordering: snapshot persistence precedes downscale; successful restore precedes snapshot removal. Manual actions must not invert that ordering.

## Local Podman image checks

The repository's normal container workflow uses Podman and never publishes unless a target ending in `-push` is selected. Confirm the engine and machine before acceptance:

```sh
podman version
podman info
make image-build image-inspect IMG=localhost/workload-lifecycle:acceptance
make image-multiarch image-multiarch-inspect \
  IMG=localhost/workload-lifecycle:acceptance \
  MULTIARCH_IMG=localhost/workload-lifecycle:acceptance-multiarch
```

If Podman cannot connect, start or recreate the Podman machine appropriate to the host, then rerun the non-publishing targets. The final stage is a registry-independent `scratch` image containing the static controller, the builder CA bundle, and the builder IANA timezone database; it intentionally has no shell or package manager. Use `podman image inspect`, rather than shelling into the image, to confirm its numeric `65532:65532` user and entrypoint. A successful multi-architecture build and manifest inspection proves that `linux/amd64` and `linux/arm64` image entries exist; it does not prove that the host can execute the foreign architecture. Runtime execution requires optional emulation and is not part of image build/inspection acceptance. Use `CONTAINER_ENGINE=<compatible-cli>` only when that CLI implements the same local build, image-inspect, and manifest behavior.

## Upgrade and rollback incidents

Before changing controller versions, back up `workload-lifecycle-state` and render the exact manifests. Current state format is strictly `formatVersion: 1`; binaries reject unknown versions rather than silently rebuilding. Do not downgrade to a release that cannot read the stored format. Roll out one compatible image, wait for leader readiness, and verify metrics before proceeding.

If an upgrade cannot become ready, preserve the state ConfigMap, inspect policy/state format errors, and roll back only to a version known to read that format. Never solve an upgrade failure by deleting state. See the main [README](../README.md) for deployment and compatibility details.
