# Requirements Document

## Introduction

当策略配置 `expiredAction: delete` 时，Revision 超过 maxAge 的 Workload 将被直接删除（删除 Deployment 或 StatefulSet 对象本身），而非缩容到 `replicas.expired` 目标值。此行为为按策略显式 opt-in，不影响未配置该字段的策略的现有缩容行为。

## Glossary

- **Controller**：kube-workload-lifecycle-manager 的调谐控制器。
- **ExpiredAction**：策略新增字段，定义 Revision 过期后的处理行为。有效值为 `scale`（默认，缩容到 expired 目标）和 `delete`（删除 Workload 对象）。
- **Workload**：被 Controller 管理的 Deployment 或 StatefulSet 对象。
- **Revision**：参与跟踪的普通容器 `{name,image}` 规范化集合的 hash。
- **MaxAge**：策略中配置的 Revision 最大存活时间。
- **StateStore**：Controller 持久化 Workload 状态的 ConfigMap 存储。
- **ClusterRole**：Controller 的集群级 RBAC 权限定义。
- **ReplicaSnapshot**：Controller 首次缩容前持久化的 `scale.spec.replicas`。

## Requirements

### Requirement 1: 策略配置 — expiredAction 字段

**User Story:** 作为策略维护者，我希望通过策略字段显式选择过期后删除行为，以便按需对不同 Workload 组采用不同的过期处理方式。

#### Acceptance Criteria

1.1 THE Controller SHALL 在策略 YAML 的 `lifecycle` 部分内支持 `expiredAction` 字段（与 `maxAge` 和 `revision` 同级），有效值为 `scale` 和 `delete`（区分大小写，仅接受全小写形式）。

1.2 IF 策略未配置 `expiredAction` 字段或其值为空字符串，THEN THE Controller SHALL 将该策略的 `expiredAction` 视为 `scale`，保持现有缩容行为不变。

1.3 IF 策略配置了无效的 `expiredAction` 值（既非 `scale` 也非 `delete`），THEN THE Controller SHALL 拒绝整个 PolicySet 更新——不应用任何策略变更，保留最近一次成功加载的 PolicySet，并记录包含无效值和策略名称的错误日志。

1.4 WHEN Controller 成功加载包含 `expiredAction` 字段的 PolicySet 后，THE Controller SHALL 将该字段值持久化到对应 Policy 的 Lifecycle 结构中，供后续调谐决策使用。

### Requirement 2: 过期删除执行

**User Story:** 作为集群运维人员，我希望过期的 Workload 在 opt-in 删除策略下被自动删除，以便释放集群资源且无需人工干预。

#### Acceptance Criteria

2.1 WHEN Workload 的 Revision 已过期（now >= firstSeenAt + maxAge）且匹配的策略配置了 `expiredAction: delete`，THE Controller SHALL 向 Kubernetes API 发起删除该 Workload 的 Deployment 或 StatefulSet 对象的请求。

2.2 THE Controller SHALL 仅对 Deployment 或 StatefulSet 对象本身发起删除 API 调用，不直接对 Pod、PVC 或其他关联对象发起删除调用。

2.3 WHEN Workload 的 Revision 已过期（now >= firstSeenAt + maxAge）且匹配的策略配置了 `expiredAction: scale`（或未配置 expiredAction），THE Controller SHALL 执行现有的缩容到 `replicas.expired` 目标行为，不执行删除。

2.4 IF 删除 API 调用返回错误，THEN THE Controller SHALL 记录包含 Workload 标识（kind/namespace/name）和错误详情的结构化日志，不修改 StateStore 中该 Workload 的状态条目，并在下一次调谐周期（30 秒内）中重新评估并重试删除。

2.5 WHEN 策略配置了 `expiredAction: delete` 且 Workload Revision 已过期，THE Controller SHALL 跳过 ReplicaSnapshot 捕获步骤，直接执行删除（无论 Workload 当前副本数为何值）。

### Requirement 3: 快照/恢复流程绕过

**User Story:** 作为项目维护者，我希望删除路径无需执行快照和恢复逻辑，以免产生无意义的状态数据和恢复预期。

#### Acceptance Criteria

3.1 WHEN 策略配置了 `expiredAction: delete` 且 Workload Revision 已过期（age >= maxAge），THE Controller SHALL 跳过 ReplicaSnapshot 捕获步骤且不执行 scale 子资源写入，直接执行 Workload 删除。

3.2 WHEN 策略配置了 `expiredAction: delete` 且 Workload Revision 已过期，THE Controller SHALL 不尝试恢复任何已存在的 ReplicaSnapshot（既不执行恢复 scale 写入，也不清除快照记录；快照随状态条目在删除成功后由状态清理移除）。

3.3 IF 策略配置了 `expiredAction: delete` 且 Workload Revision 未过期（age < maxAge），THEN THE Controller SHALL 对该 Workload 执行与 `expiredAction: scale` 相同的定时窗口快照捕获、缩容和恢复逻辑。

### Requirement 4: 状态清理

**User Story:** 作为集群运维人员，我希望删除 Workload 后其状态记录被清除，以防 StateStore 无限增长。

#### Acceptance Criteria

4.1 WHEN Controller 成功删除一个 Workload 对象（API 调用返回成功）后，THE Controller SHALL 在同一调谐周期内从 StateStore Document 的 States map 中移除该 Workload 的状态条目（key 为 `kind/namespace/name`），并调用 Save 持久化变更。

4.2 IF 状态清理的 StateStore Save 操作返回错误，THEN THE Controller SHALL 记录包含 Workload 标识（kind/namespace/name）和错误详情的结构化错误日志，并在下次调谐周期中重试清理，不阻塞其他 Workload 的调谐。

4.3 THE Controller SHALL 先执行 Workload 删除 API 调用并确认成功（无错误返回），再执行状态条目移除，确保不会出现"状态已清理但对象未删除"的不一致。

4.4 IF Workload 删除 API 调用失败，THEN THE Controller SHALL 保留 StateStore 中该 Workload 的状态条目不变，不执行任何状态清理操作。

### Requirement 5: RBAC 权限扩展

**User Story:** 作为平台管理员，我希望 Controller 拥有必要的删除权限，以便 expired-delete 功能可以正常运行。

#### Acceptance Criteria

5.1 THE ClusterRole SHALL 在 `apps` API 组的 `deployments` 和 `statefulsets` 资源规则中新增 `delete` 动词权限。

5.2 THE ClusterRole SHALL 保留现有的对 `apps` API 组中 `deployments` 和 `statefulsets` 资源的 `get`、`list`、`watch` 权限不变。

5.3 THE ClusterRole SHALL 保留现有的对 `apps` API 组中 `deployments/scale` 和 `statefulsets/scale` 子资源的 `get`、`update` 权限不变。

### Requirement 6: 决策优先级与日志区分

**User Story:** 作为项目维护者，我希望删除决策在优先级中正确定位，且日志原因可区分删除与缩容，以便可观测性完整。

#### Acceptance Criteria

6.1 THE Controller SHALL 维持决策优先级为（从高到低）：过期（revision-lifecycle-expired） > 窗口内跳过（scale-down-skipped） > 定时缩容（scale-down-window） > 快照恢复（restore-previous-replicas） > 活跃不操作（active-window）。过期决策无论 `expiredAction` 值为 `scale` 或 `delete` 始终为最高优先级。

6.2 WHEN Controller 因匹配策略的 `expiredAction: delete` 执行 Workload 删除，THE Controller SHALL 使用决策原因字符串 `revision-lifecycle-expired:deleted` 以区分于缩容场景的 `revision-lifecycle-expired`。

6.3 WHEN Controller 因匹配策略的 `expiredAction: scale`（或未配置 expiredAction）执行缩容，THE Controller SHALL 继续使用现有决策原因字符串 `revision-lifecycle-expired`。

6.4 WHEN Controller 执行过期删除操作，THE Controller SHALL 记录至少包含以下字段的结构化日志条目：Workload kind、Workload namespace、Workload name、匹配策略名称（lastPolicyName）、决策原因（`revision-lifecycle-expired:deleted`）。

### Requirement 7: 安全不变量更新

**User Story:** 作为项目维护者，我希望文档明确声明安全不变量的变更范围，以便审计和合规评审有据可查。

#### Acceptance Criteria

7.1 THE Controller SHALL 仅在匹配策略的 `expiredAction` 字段显式等于字符串 `delete` 时执行 Workload 删除操作；`expiredAction` 为 `scale`、为空、或未配置的策略绝不触发删除。

7.2 THE Controller SHALL 在 README 安全说明中更新不变量描述，明确声明：(1) opt-in 过期删除（`expiredAction: delete`）是 Controller 唯一的删除路径，(2) 删除仅作用于 Deployment 或 StatefulSet 对象本身。

7.3 IF Controller 无法确认策略配置了 `expiredAction: delete`（策略 ConfigMap 加载失败、YAML 解析错误、或 `expiredAction` 字段值不在有效枚举 `[scale, delete]` 内），THEN THE Controller SHALL 不执行删除操作，对该 Workload 保持不操作状态（等同于 active-window 决策）。
