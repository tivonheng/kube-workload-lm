# Requirements Document

## Introduction
本项目实现一个基于容器镜像 Revision 生命周期和 IANA 时区窗口管理 Deployment、StatefulSet scale 子资源的 Kubernetes Controller。系统必须保留缩容前副本快照，并在允许恢复时恢复原副本数。

## Glossary
- **Revision**：参与跟踪的普通容器 `{name,image}` 规范化集合。
- **ReplicaSnapshot**：Controller 首次缩容前持久化的 `scale.spec.replicas`。
- **PolicySet**：原子加载的一组版本化生命周期策略。

## Requirements

### Requirement 1: 管理边界
**User Story:** 作为平台管理员，我希望 Controller 使用最小权限管理两类 Workload，以避免破坏业务模板和持久化数据。

#### Acceptance Criteria
1.1 Controller SHALL 支持 `Deployment` 与 `StatefulSet`。
1.2 Controller SHALL 只读取 Workload 元数据、Labels、普通容器镜像及 scale 子资源。
1.3 Controller SHALL 只通过 `deployments/scale`、`statefulsets/scale` 修改副本数，且 SHALL NOT 修改 PodTemplate、删除 Workload、Pod 或 PVC。

### Requirement 2: Revision 识别
**User Story:** 作为发布系统使用者，我希望生命周期只由选定容器的镜像集合定义，以便副本和非镜像配置变化不会延长生命周期。

#### Acceptance Criteria
2.1 默认 SHALL 跟踪 `spec.template.spec.containers` 中全部普通容器，不包含 init/ephemeral containers。
2.2 策略 MAY 指定非空容器名称集合；指定名称不存在时 SHALL 跳过该 Workload。
2.3 Controller SHALL 按容器名排序并规范化序列化 `{container,image}`，计算 `sha256:<lowercase-hex>` revisionHash。
2.4 Controller SHALL 独立计算 trackingSpecHash；完整 PodTemplate、pod-template-hash 和 controller-revision-hash SHALL NOT 用作 Revision 身份。
2.5 workload UID、trackingSpecHash 或 revisionHash 变化 SHALL 建立新基线；replicas、资源版本、非镜像模板字段、策略时间或 Label 变化 SHALL NOT 重置 firstSeenAt。

### Requirement 3: 策略匹配与配置
**User Story:** 作为策略维护者，我希望策略选择确定且配置原子加载，以避免错误配置造成非预期缩放。

#### Acceptance Criteria
3.1 PolicySet SHALL 使用 `apiVersion`/`kind` 版本化，并严格拒绝未知字段、显式 null 和非法值。
3.2 `target.kinds` 与 Kubernetes LabelSelector SHALL 同时匹配；空 selector SHALL 非法。
3.3 最高 priority 策略 SHALL 生效；多个最高 priority 相同的策略 SHALL 产生冲突且不得缩放。
3.4 PolicySet 更新 SHALL 整体原子生效；非法更新 SHALL 保留最后完整有效快照。

### Requirement 4: 生命周期与时间窗口
**User Story:** 作为业务团队，我希望按 Revision 年龄和本地时区窗口缩容，以便在正确时间控制资源成本。

#### Acceptance Criteria
4.1 expiresAt SHALL 动态等于 `firstSeenAt + current maxAge`，持久化时间 SHALL 使用 UTC RFC3339。
4.2 每条策略 MAY 配置独立 IANA timeZone 及多个同日、跨日或全天窗口。
4.3 窗口 SHALL 使用 `[start,end)`；跨天的 startDays SHALL 表示开始日；任意窗口命中即缩容。
4.4 决策优先级 SHALL 固定为：Revision 过期、定时缩容、快照恢复、活跃不操作。

### Requirement 5: 副本快照与恢复
**User Story:** 作为 Workload 所有者，我希望缩容结束后恢复原副本配置，以便 Controller 不覆盖发布系统维护的活跃容量。

#### Acceptance Criteria
5.1 首次缩容前 Controller SHALL 从 scale.spec.replicas 读取并持久化 replicaSnapshot，成功后才可缩容。
5.2 缩容目标 SHALL 为 `min(configuredTarget,snapshotReplicas)`，不得因缩容反向扩容。
5.3 缩容期间 SHALL NOT 重采集快照；外部 Apply replicas 后 SHALL 纠正副本但保留原快照。
5.4 恢复 SHALL 先成功更新 scale，再清除快照；失败重试 SHALL 幂等。
5.5 新 Revision SHALL 保留同一 Workload 的快照；UID 变化 SHALL 丢弃旧快照。
5.6 活跃且无快照时 SHALL NOT 修改副本数；状态丢失时 SHALL NOT 猜测恢复值。

### Requirement 6: 状态、冲突与运行
**User Story:** 作为集群运维人员，我希望状态可恢复、冲突安全且运行状态可观测，以便 Controller 能可靠长期运行。

#### Acceptance Criteria
6.1 StateStore SHALL 抽象持久化；V1 SHALL 支持带 formatVersion 的 ConfigMap 状态及 resourceVersion 冲突重试。
6.2 HPA 管理的 Workload SHALL 被拒绝管理，且不得创建快照或缩放。
6.3 Watch SHALL 覆盖 Deployment、StatefulSet、HPA 与策略 ConfigMap，并配合默认 30s 全量 Reconcile。
6.4 单 Workload 失败 SHALL NOT 阻塞其他 Workload；仍存在有效快照的状态 SHALL NOT 被 TTL 清理。
6.5 Controller SHALL 提供结构化日志、低基数 Prometheus 指标、healthz/readyz、Lease Leader Election 和优雅退出。
6.6 部署 SHALL 使用最小 RBAC、非 root、只读根文件系统，并支持 amd64/arm64。

### Requirement 7: 验收
**User Story:** 作为项目维护者，我希望交付物和测试完整可追踪，以便安全构建、部署和排障。

#### Acceptance Criteria
7.1 项目 SHALL 包含可编译代码、单元/集成测试、Dockerfile、Kustomize 或 Helm、RBAC、示例和运维文档。
7.2 proposal.md 第 22 节全部场景 SHALL 可追踪到测试或 Spec 任务。