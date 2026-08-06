# Requirements Document

## Introduction
在定时缩容窗口（downWindow）期间，如果 Workload 发生 Revision 变化（即重新发布），Controller 应跳过后续缩容，允许新版本正常运行直到当前窗口结束。本功能确保发布操作优先于定时缩容策略，避免新部署被反复缩容。

## Glossary
- **Controller**：kube-workload-lifecycle-manager 的调谐控制器。
- **DownWindow**：策略中配置的定时缩容窗口，由 schedule.Match() 判定当前时刻是否处于其中。
- **Revision**：参与跟踪的普通容器 `{name,image}` 规范化集合的 hash。
- **ScaleDownSkip**：Revision 在 downWindow 期间发生变化后，Controller 对该 Workload 跳过缩容直至窗口结束的状态。
- **WindowEntryRevision**：进入 downWindow 时（或首次缩容该 Workload 时）记录的 RevisionHash。
- **ReplicaSnapshot**：Controller 首次缩容前持久化的 `scale.spec.replicas`。

## Requirements

### Requirement 1: 窗口内 Revision 变化检测

**User Story:** 作为发布系统使用者，我希望 Controller 能检测到 downWindow 期间的 Revision 变化，以便区分"正常缩容"和"窗口内重新发布"两种场景。

#### Acceptance Criteria

1.1 WHEN Controller 对一个 Workload 执行定时缩容（首次进入 downWindow 且尚未记录 WindowEntryRevision），THE Controller SHALL 将当前 RevisionHash 持久化为该 Workload 的 WindowEntryRevision。

1.2 WHEN Controller 在 downWindow 期间调谐一个 Workload，且该 Workload 的当前 RevisionHash 与 WindowEntryRevision 不同，THE Controller SHALL 将该 Workload 标记为 ScaleDownSkip 状态。

1.3 WHILE Workload 不处于任何 downWindow 中，THE Controller SHALL 清除该 Workload 的 WindowEntryRevision 和 ScaleDownSkip 标记。

### Requirement 2: 跳过缩容行为

**User Story:** 作为 Workload 所有者，我希望窗口内重新发布后新版本能正常运行，不被 Controller 反复缩容。

#### Acceptance Criteria

2.1 WHILE Workload 处于 ScaleDownSkip 状态且当前时刻仍在同一 downWindow 内，THE Controller SHALL 跳过对该 Workload 的定时缩容操作，且 SHALL NOT 修改其副本数。

2.2 WHILE Workload 处于 ScaleDownSkip 状态，THE Controller SHALL 返回决策原因 "scale-down-skipped:redeploy"，以便日志和指标可追踪。

2.3 WHEN Workload 处于 ScaleDownSkip 状态且 downWindow 结束（schedule.Match 返回 false），THE Controller SHALL 清除 ScaleDownSkip 标记和 WindowEntryRevision，恢复正常调谐逻辑。

### Requirement 3: 快照与恢复交互

**User Story:** 作为平台管理员，我希望跳过缩容不破坏已有的副本快照逻辑，以便系统状态一致性得到保证。

#### Acceptance Criteria

3.1 WHEN Workload 在 downWindow 内发生 Revision 变化且已存在 ReplicaSnapshot，THE Controller SHALL 执行快照恢复（scale 回 snapshot 值并清除快照），然后标记 ScaleDownSkip。

3.2 WHEN Workload 在 downWindow 内发生 Revision 变化且尚未被缩容（无 ReplicaSnapshot），THE Controller SHALL 直接标记 ScaleDownSkip，不进行任何缩容或快照操作。

3.3 IF 快照恢复失败（scale 更新失败），THEN THE Controller SHALL 保留 ReplicaSnapshot 并在下次调谐时重试恢复，不标记 ScaleDownSkip 直到恢复成功。

### Requirement 4: 窗口周期边界

**User Story:** 作为策略维护者，我希望跳过缩容仅影响当前窗口周期，不影响后续窗口的正常缩容行为。

#### Acceptance Criteria

4.1 WHEN 同一 downWindow 的下一个周期开始（例如次日同名窗口再次命中），THE Controller SHALL 视为新的窗口入口，重新记录 WindowEntryRevision 并允许执行缩容。

4.2 WHEN 策略配置了多个 downWindow 且 Workload 仅在其中一个窗口触发了 ScaleDownSkip，WHILE 另一个 downWindow 命中，THE Controller SHALL 在新窗口中独立评估，允许正常缩容。

4.3 THE Controller SHALL 使用 downWindow 名称加日期（或等效唯一标识）区分同名窗口的不同周期实例，避免跨周期误判。

### Requirement 5: 决策优先级

**User Story:** 作为项目维护者，我希望 ScaleDownSkip 不影响 Revision 过期缩容，以便安全策略始终生效。

#### Acceptance Criteria

5.1 WHILE Workload 处于 ScaleDownSkip 状态且 Revision 已过期（age >= maxAge），THE Controller SHALL 仍然执行过期缩容，ScaleDownSkip 不阻止过期决策。

5.2 THE Controller SHALL 维持决策优先级为：Revision 过期 > ScaleDownSkip 跳过 > 定时缩容 > 快照恢复 > 活跃不操作。

### Requirement 6: 状态持久化

**User Story:** 作为集群运维人员，我希望 ScaleDownSkip 相关状态可靠持久化，以便 Controller 重启后行为一致。

#### Acceptance Criteria

6.1 THE Controller SHALL 将 WindowEntryRevision 和 ScaleDownSkip 标记持久化到 WorkloadState 中，与现有状态使用同一 StateStore。

6.2 IF Controller 重启且状态加载成功，THEN THE Controller SHALL 恢复 ScaleDownSkip 判断，不因重启重复缩容窗口内已重新发布的 Workload。

6.3 IF 状态丢失（StateStore 为空或格式不兼容），THEN THE Controller SHALL 不猜测 ScaleDownSkip，按正常逻辑从当前状态重新评估。
