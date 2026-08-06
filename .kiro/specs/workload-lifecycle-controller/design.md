# Design Document

## Overview
本设计将策略解析、Revision 识别、时间窗口和副本状态机保持为纯 Go 领域核心，将 Kubernetes API 访问隔离在适配器中，以保证行为可测试、存储可替换且只使用 scale 子资源。

## Architecture
```text
Policy Loader -> Matcher -> Revision Calculator -> Schedule Evaluator
                                      |                    |
Workload Gateway -> Reconciler -> Decision Engine -> StateStore -> Scale Gateway
                                      |
                              Metrics / JSON Logs
```
核心逻辑保持纯 Go；Kubernetes API 仅存在于 WorkloadGateway、ScaleGateway 和 ConfigMapStateStore 适配器。

## Components and Interfaces
- `PolicyLoader`：严格解码、校验并原子发布不可变策略快照。
- `PolicyMatcher`：执行 kind、LabelSelector、priority 和冲突判断。
- `RevisionCalculator`：生成 trackingSpecHash、revisionHash 与规范化 Revision。
- `ScheduleEvaluator`：按策略时区判断同日、跨日和全天窗口。
- `DecisionEngine`：执行过期、定时缩容、恢复、活跃不操作的固定优先级。
- `StateStore`、`WorkloadGateway`、`ScaleGateway`：隔离持久化和 Kubernetes API。

## Data Models
- `workload.Workload`: kind、namespace、name、UID、labels、containers、scale.spec.replicas。
- `policy.PolicySet/Policy`: target、priority、lifecycle、replicas、schedule。
- `lifecycle.Revision`: 规范化、按名称排序的容器镜像集合及 revisionHash。
- `lifecycle.TrackingSpec`: source、可选容器集合及 trackingSpecHash。
- `state.WorkloadState`: Revision 身份、first/lastSeenAt、可选 ReplicaSnapshot。
- `lifecycle.Decision`: desired replicas、reason、是否需要先建快照或恢复后清快照。

## Reconcile 顺序
1. 获取 Workload 最新对象与 scale 子资源；检查 HPA。
2. 匹配 kind/selector，按 priority 选择唯一策略；无匹配、冲突或 HPA 时不修改。
3. 计算 trackingSpecHash 与 revisionHash；处理首次发现、UID、定义或 Revision 变化并先持久化。
4. 使用注入 Clock 计算 expiresAt，并在策略时区匹配窗口。
5. DecisionEngine 按 `expired > scheduledDown > restore > active` 生成决策。
6. 需要缩容且无快照时：重新读取 scale、持久化快照、再更新 scale。
7. 需要恢复时：先更新 scale，再清除快照；每一步失败均保留可重试状态。
8. 更新 lastSeenAt、指标和结构化日志；一个 Workload 的错误不终止批次。

## Hash 与时间
Revision JSON 仅包含固定顺序的 `container`、`image` 字段；列表按容器名排序，SHA-256 输出小写十六进制。TrackingSpec 使用独立版本化规范值。时间以 UTC 持久化；窗口按 IANA 本地墙上时间判断，采用 `[start,end)`，跨天检查前一开始日。

## 状态与一致性
V1 ConfigMap 保存 `{formatVersion,states}`，更新使用 resourceVersion 与有界退避。无效状态不静默重建。单 ConfigMap 的 1MiB 与共享失败域由 StateStore 隔离，后续可替换为分片、CRD 或外部存储。快照写入先于缩容，快照清除后于恢复，从而保证崩溃重试安全。

## 配置加载
严格解码完整 PolicySet，应用固定内置默认值后完成交叉字段校验和 selector 编译；成功后用不可变快照原子替换。首次没有有效快照时 readyz 失败，热更新失败时继续使用上一快照。

## 运行与安全
controller-runtime Manager 提供 Watch、周期全量 Reconcile、Lease Leader Election、health probes 和信号处理。RBAC 不授予 Workload update/patch/delete、Pod/PVC delete。镜像使用多阶段构建、非 root 用户和只读根文件系统。

## Correctness Properties
### Property 1: Canonical Revision Determinism
**Validates: Requirements 2.3, 2.4**

同一 tracking spec 和镜像集合的 Hash 不受输入容器顺序影响。

### Property 2: Snapshot Transaction Ordering
**Validates: Requirements 5.1, 5.4**

缩容前快照必须先于 scale 写入，恢复后的快照清除必须晚于 scale 成功。

### Property 3: No Scale-Up During Down State
**Validates: Requirements 5.2**

任何缩容决策的 desired replicas 不得大于快照或当前基线。

### Property 4: Decision Priority
**Validates: Requirements 4.4, 5.6**

生命周期过期必须优先于时间窗口，活跃且无快照时不得修改副本数。

## Error Handling
单 Workload 的策略冲突、HPA、状态或 scale 错误只终止该 Workload；非法策略快照整体拒绝；无效状态不得静默重建并扩容。错误通过结构化 reason、计数指标和 readiness 暴露。

## Testing Strategy
纯逻辑使用表驱动单测覆盖 Hash、策略冲突、时区/DST、边界和副本状态机；fake client 覆盖 ConfigMap 冲突与 scale-only；envtest 覆盖 Deployment/StatefulSet、HPA、Watch 与重启恢复。