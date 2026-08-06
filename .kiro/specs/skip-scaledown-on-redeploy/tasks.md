# Implementation Plan: Skip Scale-Down on Redeploy

## Overview
在 downWindow 期间检测 Revision 变化并跳过后续缩容。按依赖顺序：先扩展状态字段，再实现窗口实例 ID 计算，然后修改决策引擎新增 skip 分支，最后在 Transition Reconciler 中编排窗口周期管理和 Revision 变化检测。

## Tasks

- [x] 1. 扩展 State Layer
  - [x] 1.1 在 `WorkloadState` 中新增 `WindowEntryRevision`、`WindowInstanceID`、`ScaleDownSkipped` 三个字段
    - 修改 `internal/state/types.go`
    - `WindowEntryRevision string` — 进入 downWindow 时记录的 RevisionHash，json tag `windowEntryRevision,omitempty`
    - `WindowInstanceID string` — 窗口周期唯一标识，格式 `{name}:{YYYY-MM-DD}`，json tag `windowInstanceID,omitempty`
    - `ScaleDownSkipped bool` — 当前周期内因 Revision 变化跳过缩容，json tag `scaleDownSkipped,omitempty`
    - _Requirements: 6.1, 1.1, 1.2_

- [x] 2. 实现 WindowInstanceID 计算
  - [x] 2.1 新建 `internal/schedule/instance.go`，实现 `WindowInstanceID` 函数
    - 签名：`func WindowInstanceID(now time.Time, timezone string, windowName string, windows []Window) (string, error)`
    - 加载时区、找到匹配的 Window、根据 allDay/跨天/不跨天逻辑计算窗口开始日期
    - 返回格式 `"{windowName}:{YYYY-MM-DD}"`
    - _Requirements: 4.3_

  - [ ]* 2.2 编写 `WindowInstanceID` 属性测试
    - 新建 `internal/schedule/instance_property_test.go`
    - **Property 9: WindowInstanceID 唯一性** — 不同日期产生不同 ID，相同名称+日期产生相同 ID
    - **Validates: Requirements 4.3**

  - [ ]* 2.3 编写 `WindowInstanceID` 单元测试
    - 新建 `internal/schedule/instance_test.go`
    - 覆盖场景：allDay 窗口、不跨天窗口、跨天窗口（午夜前后）、无效时区错误
    - _Requirements: 4.3_

- [x] 3. Checkpoint - 确保 schedule 包测试通过
  - 运行 `go test ./internal/schedule/...`，确认所有测试通过，有问题请咨询用户。

- [x] 4. 扩展决策引擎
  - [x] 4.1 在 `DecisionInput` 中新增 `ScaleDownSkipped bool` 字段，新增 `ReasonScaleDownSkipped` 常量
    - 修改 `internal/lifecycle/decision.go`
    - 常量值：`"scale-down-skipped:redeploy"`
    - _Requirements: 2.2_

  - [x] 4.2 在 `Decide()` 中新增 ScaleDownSkip 分支
    - 插入位置：expired 检查之后、scheduledDown 之前
    - 条件：`input.ScaleDownSkipped && input.ScheduledDown`
    - 返回：`Decision{input.CurrentReplicas, ReasonScaleDownSkipped, false, false, false}`
    - _Requirements: 2.1, 2.2, 5.1, 5.2_

  - [ ]* 4.3 编写决策引擎属性测试
    - 修改 `internal/lifecycle/decision_test.go`（或新建 `decision_property_test.go`）
    - **Property 4: ScaleDownSkip 阻止缩容并返回正确原因** — ShouldScale=false, Reason="scale-down-skipped:redeploy"
    - **Property 10: 过期覆盖 ScaleDownSkip** — age >= maxAge 时返回 expired reason
    - **Validates: Requirements 2.1, 2.2, 5.1, 5.2**

- [x] 5. Checkpoint - 确保 lifecycle 包测试通过
  - 运行 `go test ./internal/lifecycle/...`，确认所有测试通过，有问题请咨询用户。

- [x] 6. 实现 Transition Reconciler 窗口周期管理
  - [x] 6.1 在 `Reconcile()` 中新增 `windowInstanceID` 计算逻辑
    - 修改 `internal/controller/transition.go`
    - 在 `matchSchedule` 之后，当 `scheduledDown && windowName != ""` 时调用 `schedule.WindowInstanceID`
    - 引入辅助函数 `computeWindowInstanceID`
    - _Requirements: 4.1, 4.2, 4.3_

  - [x] 6.2 实现 `reconcileWindowCycle` 函数
    - 当不在 downWindow 或 windowInstanceID 与 state 中记录的不同时，清除 `WindowEntryRevision`、`WindowInstanceID`、`ScaleDownSkipped`
    - _Requirements: 1.3, 2.3, 4.1, 4.2_

  - [x] 6.3 实现 `handleRedeployDuringWindow` 方法
    - 检测条件：`scheduledDown && !currentState.ScaleDownSkipped && WindowEntryRevision != "" && WindowEntryRevision != revision.RevisionHash`
    - 有 ReplicaSnapshot：先恢复 scale（到 snapshot 值）、清除 snapshot、再标记 ScaleDownSkipped
    - 无 ReplicaSnapshot：直接标记 ScaleDownSkipped
    - 恢复失败时保留 snapshot 并返回 error，不标记 skip
    - 持久化 skip 状态到 StateStore
    - _Requirements: 1.2, 3.1, 3.2, 3.3_

  - [x] 6.4 修改 `Reconcile()` 将 `ScaleDownSkipped` 传入 `lifecycle.Decide` 并在首次缩容时记录 `WindowEntryRevision`
    - 传递 `currentState.ScaleDownSkipped` 给 `DecisionInput`
    - 当 `decision.NeedsSnapshot && scheduledDown && currentState.WindowEntryRevision == ""` 时设置 `WindowEntryRevision` 和 `WindowInstanceID`
    - _Requirements: 1.1, 6.1, 6.2_

  - [ ]* 6.5 编写 Transition Reconciler 属性测试
    - 新建 `internal/controller/transition_property_test.go`
    - **Property 1: WindowEntryRevision 捕获** — 首次缩容后 state 中 WindowEntryRevision == 当前 RevisionHash
    - **Property 2: Revision 变化触发 ScaleDownSkip** — 窗口内 revision 变化后 ScaleDownSkipped=true
    - **Property 3: 窗口外清除 skip 状态** — scheduledDown=false 时清除所有 skip 字段
    - **Property 5: 恢复-然后-跳过 顺序性** — 有 snapshot 时先恢复再标记 skip
    - **Property 6: 无快照时直接标记 skip** — 无 snapshot 时不触发 scale 操作
    - **Property 7: 窗口周期边界重置** — instanceID 变化时清除 skip 状态
    - **Property 8: 多窗口独立性** — 不同窗口触发的 skip 互不影响
    - **Validates: Requirements 1.1, 1.2, 1.3, 2.3, 3.1, 3.2, 4.1, 4.2, 4.3**

  - [ ]* 6.6 编写 Transition Reconciler 单元测试
    - 修改 `internal/controller/transition_test.go`
    - 覆盖场景：快照恢复失败不标记 skip (Req 3.3)、状态丢失后正常缩容 (Req 6.3)、Controller 重启恢复 skip (Req 6.2)
    - _Requirements: 3.3, 6.2, 6.3_

- [x] 7. 全量验证
  - [x] 7.1 运行 `go test ./...` 和 `go vet ./...` 确认无回归
    - 所有现有测试不受影响
    - 新增测试全部通过
    - _Requirements: 1.1-6.3_

## Notes
- 新增字段使用 `omitempty`，旧格式 JSON 反序列化时自动为零值，无需格式迁移。
- 决策优先级：Revision 过期 > ScaleDownSkip 跳过 > 定时缩容 > 快照恢复 > 活跃不操作。
- Tasks marked with `*` are optional and can be skipped for faster MVP.
- 每完成一个阶段即运行对应包测试，Checkpoint 确保增量验证。
- 属性测试使用 `testing/quick`，与项目现有测试风格一致。

## Task Dependency Graph
```json
{
  "waves": [
    { "id": 0, "tasks": ["1.1"] },
    { "id": 1, "tasks": ["2.1", "4.1"] },
    { "id": 2, "tasks": ["2.2", "2.3", "4.2"] },
    { "id": 3, "tasks": ["4.3", "6.1"] },
    { "id": 4, "tasks": ["6.2", "6.3"] },
    { "id": 5, "tasks": ["6.4"] },
    { "id": 6, "tasks": ["6.5", "6.6"] },
    { "id": 7, "tasks": ["7.1"] }
  ]
}
```
State 扩展是基础；WindowInstanceID 和 DecisionInput 扩展可并行；决策分支依赖新字段；Reconciler 编排依赖 schedule 和 lifecycle 层变更；属性测试和单元测试在实现完成后进行；全量验证收尾。
