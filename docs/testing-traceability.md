# Proposal §22 test traceability

This matrix is the acceptance index for `proposal.md` section 22. Test names are
stable Go test identifiers; `Envtest/...` denotes a subtest of
`TestEnvtestKubernetesIntegration`. Fake-client tests assert API action shape,
while envtest tests assert real API-server behavior.

## Revision lifecycle

| ID | Scenario | Evidence |
|---|---|---|
| R1 | First discovery tracks all regular containers and creates state | `TestReconcileIdentitySeparatesRevisionAndTrackingChanges`, `TestWorkloadConversionReadsOnlyAllowedFields` |
| R2 | Unchanged image set preserves `firstSeenAt` | `TestReconcileIdentitySeparatesRevisionAndTrackingChanges` |
| R3 | Any regular-container image change creates a Revision in default mode | `TestCalculateRevisionAllContainersIsCanonical`, `TestReconcileIdentitySeparatesRevisionAndTrackingChanges` |
| R4 | Named `app` tracking ignores sidecar image changes | `TestCalculateRevisionNamedContainersIgnoreSidecar` |
| R5 | Missing named container skips scaling with error | `TestCalculateRevisionNamedContainersIgnoreSidecar` |
| R6 | Replica/resourceVersion/non-image template changes do not reset age | `TestWorkloadConversionReadsOnlyAllowedFields`, `TestReconcileIdentitySeparatesRevisionAndTrackingChanges` |
| R7 | Tracking-definition and image changes are distinguished | `TestReconcileIdentitySeparatesRevisionAndTrackingChanges` |
| R8 | UID replacement resets lifecycle and discards old snapshot | `TestSnapshotIdentityRules/new_UID_discards_old_snapshot_and_captures_live_scale` |
| R9 | Restart reloads Revision state and snapshot | `Envtest/watch_reconciliation_and_restart_restoration` |
| R10 | Expired Revision remains down until a new Revision | `TestExpiredRevisionStaysDownUntilNewRevisionRestoresSnapshot` |

## Time windows

| ID | Scenario | Evidence |
|---|---|---|
| T1 | Same-day window | `TestMatchWindowShapesAndBoundaries` |
| T2 | Cross-midnight window | `TestMatchWindowShapesAndBoundaries` |
| T3 | Weekend/all-day window | `TestMatchWindowShapesAndBoundaries` |
| T4 | Overlap selects stable sorted name | `TestMatchOverlapsUseStableNameAndTimezone` |
| T5 | Start boundary is included | `TestMatchWindowShapesAndBoundaries/start-inclusive` |
| T6 | End boundary is excluded | `TestMatchWindowShapesAndBoundaries/end-exclusive` |
| T7 | Per-policy timezone changes wall-clock result | `TestMatchOverlapsUseStableNameAndTimezone` |
| T8 | DST start gap and repeated fall hour | `TestMatchDSTSpringGapDoesNotCompensate`, `TestMatchDSTRepeatedHour` |
| T9 | Expiration outranks schedule/active behavior | `TestDecideReplicaStateMachine/expired-precedes-window` |
## Replica snapshot and restoration

| ID | Scenario | Evidence |
|---|---|---|
| S1 | Persist snapshot 3 before downscale to 0 | `TestDownscaleReadsLiveAndPersistsSnapshotBeforeScale` |
| S2 | Snapshot write failure prevents scale | `TestSnapshotSaveFailurePreventsDownscale` |
| S3 | Window exit restores snapshot, not a fixed active value | `TestRestoreScalesBeforeClearingAndRetriesSafely` |
| S4 | Restore scale succeeds before snapshot clear | `TestRestoreScalesBeforeClearingAndRetriesSafely` |
| S5 | Clear failure retries restore idempotently | `TestClearFailureLeavesRetryableSnapshot` |
| S6 | External replica apply does not overwrite snapshot and is corrected | `TestDownscaleFailureRetainsOriginalSnapshotAcrossRetry` |
| S7 | Scheduled-to-expired transition does not recapture | `TestDecideReplicaStateMachine/scheduled-keeps-snapshot`, `TestExpiredRevisionStaysDownUntilNewRevisionRestoresSnapshot` |
| S8 | Expired lifecycle does not restore in active time | `TestExpiredRevisionStaysDownUntilNewRevisionRestoresSnapshot` |
| S9 | New Revision outside window restores snapshot | `TestExpiredRevisionStaysDownUntilNewRevisionRestoresSnapshot` |
| S10 | New Revision inside window stays down, then restores | `TestSnapshotIdentityRules/same_UID_new_revision_preserves_snapshot`, `TestExpiredRevisionStaysDownUntilNewRevisionRestoresSnapshot` |
| S11 | Active without snapshot does not write replicas | `TestDecideReplicaStateMachine/active-preserves` |
| S12 | Down target above current replicas never scales up | `TestDecideReplicaStateMachine/down-never-scales-up`, `TestDownscaleOrderingProperty` |
| S13 | Restart restores from durable snapshot | `Envtest/watch_reconciliation_and_restart_restoration` |
| S14 | Missing state never guesses a restore value | `Envtest/lost_snapshot_is_never_guessed` |

## Policy matching

| ID | Scenario | Evidence |
|---|---|---|
| P1 | `matchLabels` | `TestMatchChoosesHighestPriorityAndKind` |
| P2 | `In` | `TestMatchSupportsKubernetesSelectorOperators` |
| P3 | `NotIn` | `TestMatchSupportsKubernetesSelectorOperators` |
| P4 | `Exists` | `TestMatchSupportsKubernetesSelectorOperators` |
| P5 | `DoesNotExist` | `TestMatchSupportsKubernetesSelectorOperators` |
| P6 | No matching policy is ignored | `TestMatchChoosesHighestPriorityAndKind` |
| P7 | Highest priority wins | `TestMatchChoosesHighestPriorityAndKind` |
| P8 | Highest-priority tie rejects scaling | `TestMatchRejectsHighestPriorityTie` |
| P9 | `target.kinds` is enforced | `TestMatchChoosesHighestPriorityAndKind` |
| P10 | Policy switch without identity change preserves age | `TestReconcileIdentitySeparatesRevisionAndTrackingChanges` |
| P11 | Invalid update is atomic and retains last good snapshot | `TestManagerKeepsLastGoodSnapshot`, `Envtest/ConfigMap_policy_watch_and_state_persistence` |
## Workload behavior

| ID | Scenario | Evidence |
|---|---|---|
| W1 | Deployment scale and restore | `TestScaleGatewayUpdateUsesOnlyLiveScaleSubresources`, `TestRestoreScalesBeforeClearingAndRetriesSafely`, `Envtest/watch_reconciliation_and_restart_restoration` |
| W2 | StatefulSet scale and restore | `TestScaleGatewayUpdateUsesOnlyLiveScaleSubresources`, `TestRestoreScalesBeforeClearingAndRetriesSafely`, `Envtest/real_inventory_and_scale_subresources` |
| W3 | Only scale subresources are called | `TestScaleGatewayUpdateUsesOnlyLiveScaleSubresources`, `Envtest/real_inventory_and_scale_subresources` |
| W4 | HPA prevents snapshot and scale mutation | `TestHPARejectionOccursBeforeStateOrScaleAccess`, `Envtest/HPA_rejection_precedes_durable_mutation` |
| W5 | External replica overwrite is corrected | `TestDownscaleFailureRetainsOriginalSnapshotAcrossRetry` |
| W6 | One Workload failure does not block another | `TestReconcileFailuresAreIsolatedPerWorkloadAndKind`, `TestListFailureForOneKindDoesNotBlockOtherKind` |

## State storage

| ID | Scenario | Evidence |
|---|---|---|
| D1 | Missing ConfigMap is initialized | `TestConfigMapStateStoreInitializesMissingConfigMap`, `Envtest/ConfigMap_policy_watch_and_state_persistence` |
| D2 | Resource-version conflict retries | `TestConfigMapStateStoreSaveUsesLatestResourceVersionAfterConflict` |
| D3 | Invalid state is not treated as first discovery | `TestConfigMapStateStoreStrictlyRejectsInvalidExistingState`, `TestConfigMapStateStoreSaveDoesNotOverwriteInvalidState` |
| D4 | Snapshot survives controller restart | `Envtest/watch_reconciliation_and_restart_restoration` |
| D5 | Confirmed deletion observes safe retention | `TestCleanupDeletedRequiresSuccessfulListAndRetention/confirmed-deletion-after-retention` |
| D6 | Transient list/API failure cannot delete state | `TestCleanupDeletedRequiresSuccessfulListAndRetention/failed-list-cannot-delete` |
| D7 | Near-capacity and hard-limit signals are exposed | `TestMeasureCapacityExposesWarningAndHardLimit`, `TestConfigMapStateStoreReportsNearCapacityOnLoad`, `TestConfigMapStateStoreRejectsWritesOverCapacity` |

## Acceptance commands

- `make test`: all unit, fake-client, and envtest packages; envtest reports a clear
  skip only when no assets are configured or installed.
- `make test-envtest`: fetches Kubernetes 1.34.0 assets with the pinned
  `setup-envtest` tool and requires the envtest suite to execute.
- `make test-race`: race checks critical controller/Kubernetes adapter packages.
- `make cross-build`: produces static `linux/amd64` and `linux/arm64` binaries.
- `make acceptance`: format, vet, all tests, required envtest, race, build,
  cross-build, and manifest validation.

## Requirements acceptance mapping

| Requirement | Acceptance evidence |
|---|---|
| 1.1–1.3 | `TestKubernetesGatewayListsBothKindsAcrossNamespaces`, `TestScaleGatewayUpdateUsesOnlyLiveScaleSubresources`, `TestDeploymentAndRBACEnforceReleaseSecurity`, `Envtest/real_inventory_and_scale_subresources` |
| 2.1–2.4 | `TestWorkloadConversionReadsOnlyAllowedFields`, `TestCalculateRevisionAllContainersIsCanonical`, `TestCalculateRevisionNamedContainersIgnoreSidecar` |
| 2.5 | `TestReconcileIdentitySeparatesRevisionAndTrackingChanges`, `TestSnapshotIdentityRules`, `Envtest/watch_reconciliation_and_restart_restoration` |
| 3.1 | `TestDecodeValidPolicy`, `TestDecodeAppliesDefaults`, `TestDecodeRejectsInvalidDocuments`, `TestPolicySamplePassesStrictDecoder` |
| 3.2–3.3 | `TestMatchChoosesHighestPriorityAndKind`, `TestMatchSupportsKubernetesSelectorOperators`, `TestMatchRejectsHighestPriorityTie` |
| 3.4 | `TestManagerKeepsLastGoodSnapshot`, `TestConfigMapWatcherReloadsAtomicallyAndKeepsLastGoodOnInvalidUpdate`, `Envtest/ConfigMap_policy_watch_and_state_persistence` |
| 4.1 | `TestDecideReplicaStateMachine`, `TestDocumentRoundTripUsesVersionedUTCJSON` |
| 4.2–4.3 | `TestMatchWindowShapesAndBoundaries`, `TestMatchOverlapsUseStableNameAndTimezone`, `TestMatchDSTSpringGapDoesNotCompensate`, `TestMatchDSTRepeatedHour` |
| 4.4 | `TestDecideReplicaStateMachine/expired-precedes-window`, `TestExpiredRevisionStaysDownUntilNewRevisionRestoresSnapshot` |
| 5.1–5.4 | `TestDownscaleReadsLiveAndPersistsSnapshotBeforeScale`, `TestSnapshotSaveFailurePreventsDownscale`, `TestRestoreScalesBeforeClearingAndRetriesSafely`, `TestClearFailureLeavesRetryableSnapshot`, `TestDownscaleOrderingProperty` |
| 5.5 | `TestSnapshotIdentityRules` |
| 5.6 | `TestDecideReplicaStateMachine/active-preserves`, `Envtest/lost_snapshot_is_never_guessed` |
| 6.1 | `TestConfigMapStateStoreInitializesMissingConfigMap`, `TestConfigMapStateStoreSaveUsesLatestResourceVersionAfterConflict`, `TestConfigMapStateStoreConflictBackoffIsBoundedAndCancelable` |
| 6.2 | `TestHPADetectorFailsClosedOnDetectionErrors`, `TestHPARejectionOccursBeforeStateOrScaleAccess`, `Envtest/HPA_rejection_precedes_durable_mutation` |
| 6.3 | `TestPeriodicReconcileUsesDefaultAndCoversBothKinds`, `TestWatchEventsTriggerTargetedAndFullReconciliation`, `TestWorkloadWatchErrorRecreatesWatchAndContinues` |
| 6.4 | `TestReconcileFailuresAreIsolatedPerWorkloadAndKind`, `TestListFailureForOneKindDoesNotBlockOtherKind`, `TestCleanupDeletedProtectsLiveSameUIDSnapshot`, state capacity tests |
| 6.5 | observability tests; runtime tests for non-leader readiness, Lease settings, leadership loss, and bounded shutdown |
| 6.6 | `TestDeploymentAndRBACEnforceReleaseSecurity`, `make cross-build`, `make container-acceptance`; Podman image inspection asserts UID/GID `65532:65532`, and local manifest inspection requires `linux/amd64` and `linux/arm64` |
| 7.1 | `go test ./...`, strict config asset tests, `make manifests`, native/static builds, Dockerfile, Kustomize/RBAC/samples, README and troubleshooting guide |
| 7.2 | Every proposal §22 item is indexed in the scenario tables above and this final task owns the executable acceptance commands |

## Design correctness properties

| Property | Evidence and conclusion |
|---|---|
| 1. Canonical Revision Determinism | `TestCalculateRevisionAllContainersIsCanonical` proves input-order independence for both `revisionHash` and `trackingSpecHash`; named-container behavior is covered separately. |
| 2. Snapshot Transaction Ordering | `TestDownscaleOrderingProperty`, `TestDownscaleReadsLiveAndPersistsSnapshotBeforeScale`, and restore/clear retry tests assert snapshot-before-scale and scale-before-clear ordering. |
| 3. No Scale-Up During Down State | `TestDownscaleOrderingProperty` ranges over live/target replica pairs and `TestDecideReplicaStateMachine/down-never-scales-up` covers the explicit low-replica edge. |
| 4. Decision Priority | `TestDecideReplicaStateMachine/expired-precedes-window` and `/active-preserves`, plus `TestExpiredRevisionStaysDownUntilNewRevisionRestoresSnapshot`, assert the fixed priority and active no-op behavior. |

## Container acceptance semantics

The default local engine is Podman. `image-build` and `image-inspect` prove the Dockerfile builds and the configured runtime user is non-root. `image-multiarch` creates a local OCI manifest list and `image-multiarch-inspect` verifies both required Linux architectures without contacting a destination registry. Only `image-push` and `image-multiarch-push` publish. Foreign-architecture execution is intentionally not conflated with build/inspection success because it depends on optional host or Podman-machine emulation.