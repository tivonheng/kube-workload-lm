# Implementation Plan: Expired Workload Deletion

## Overview
为 kube-workload-lifecycle-manager 新增过期 Workload 删除功能。按依赖顺序：先扩展策略模型和校验，再扩展决策引擎，然后新增 WorkloadDeleter 接口和实现，最后在 Reconciler 中编排删除逻辑和 RBAC/文档更新。

## Tasks

- [x] 1. 扩展 Policy 模型
  - [x] 1.1 在 `rawLifecycle` 中新增 `ExpiredAction *string` 字段
    - 修改 `internal/policy/raw.go`
    - yaml tag: `expiredAction,omitempty`
    - _Requirements: 1.1, 1.4_

  - [x] 1.2 在 `Lifecycle` 类型中新增 `ExpiredAction string` 字段和常量
    - 修改 `internal/policy/types.go`
    - 新增常量 `ExpiredActionScale = "scale"` 和 `ExpiredActionDelete = "delete"`
    - _Requirements: 1.1_

  - [x] 1.3 在 `compileLifecycle` 中新增 `expiredAction` 校验逻辑
    - 修改 `internal/policy/validate.go`（或 compile 所在文件）
    - nil/空 → 默认 "scale"；无效值 → 返回 error 拒绝 PolicySet
    - _Requirements: 1.2, 1.3_

  - [x] 1.4 编写 Policy 编译单元测试
    - 修改 `internal/policy/decode_test.go`
    - 覆盖：有效值 scale/delete 通过、nil/空默认 scale、无效值 "remove" 拒绝、unknown field 拒绝
    - _Requirements: 1.1, 1.2, 1.3_

- [x] 2. 扩展决策引擎
  - [x] 2.1 在 `DecisionInput` 中新增 `ExpiredAction string` 字段
    - 修改 `internal/lifecycle/decision.go`
    - _Requirements: 6.1_

  - [x] 2.2 在 `Decision` 中新增 `DeleteWorkload bool` 字段
    - 修改 `internal/lifecycle/decision.go`
    - _Requirements: 2.1, 3.1_

  - [x] 2.3 新增 `ReasonRevisionExpiredDeleted` 常量
    - 修改 `internal/lifecycle/decision.go`
    - 值: `"revision-lifecycle-expired:deleted"`
    - _Requirements: 6.2_

  - [x] 2.4 修改 `Decide()` 在过期分支中根据 `ExpiredAction` 返回删除决策
    - 当 expired && ExpiredAction == "delete" → `Decision{CurrentReplicas, ReasonRevisionExpiredDeleted, false, false, false, true}`
    - 当 expired && ExpiredAction != "delete" → 现有 downDecision（不变）
    - _Requirements: 2.1, 2.5, 3.1, 3.2, 6.1, 6.2, 6.3_

  - [x] 2.5 编写决策引擎单元测试
    - 修改 `internal/lifecycle/decision_test.go`
    - 覆盖：expired+delete→DeleteWorkload=true, expired+scale→现有行为, not-expired+delete→正常, expired+delete优先于skip
    - _Requirements: 2.1, 2.3, 3.1, 6.1_

- [x] 3. Checkpoint - 确保 lifecycle 和 policy 包测试通过
  - 运行 `go test ./internal/lifecycle/... ./internal/policy/...`

- [x] 4. 新增 WorkloadDeleter 接口和实现
  - [x] 4.1 在 `internal/workload` 包中定义 `WorkloadDeleter` 接口
    - 新增到 `internal/workload/types.go` 或新建 `internal/workload/deleter.go`
    - 签名: `Delete(ctx context.Context, w Workload) error`
    - _Requirements: 2.1, 2.2_

  - [x] 4.2 实现 `WorkloadDeleter`
    - 新建 `internal/workload/deleter.go`（如果 4.1 没有新建的话）
    - 使用 appsv1 client，根据 Kind 调用 DeleteDeployment/DeleteStatefulSet
    - 使用 Foreground 删除策略
    - _Requirements: 2.1, 2.2_

- [x] 5. 实现 Reconciler 删除逻辑
  - [x] 5.1 在 `Reconciler` 结构体中新增 `deleter workload.WorkloadDeleter` 字段
    - 修改 `internal/controller/transition.go`
    - 更新 `NewReconciler` 构造函数签名和 nil 检查
    - _Requirements: 2.1_

  - [x] 5.2 在 `Reconcile()` 中传入 `ExpiredAction` 给 `Decide()`
    - 修改 `internal/controller/transition.go`
    - `ExpiredAction: selected.Lifecycle.ExpiredAction`
    - _Requirements: 6.1_

  - [x] 5.3 在 `Reconcile()` 中实现 `DeleteWorkload` 分支
    - 在 `Decide()` 之后、`applyDecision` 之前
    - 调用 `r.deleter.Delete`，成功后 `delete(document.States, key)` + `r.store.Save`
    - 删除失败 → 返回 error，状态不变
    - _Requirements: 2.1, 2.4, 4.1, 4.3, 4.4_

  - [x] 5.4 编写 Reconciler 删除逻辑单元测试
    - 修改 `internal/controller/transition_test.go`
    - 覆盖：过期+delete→调用deleter+清理状态, delete失败→状态不变, delete成功+save失败, 未过期+delete→不调用deleter, 过期+scale→不调用deleter
    - _Requirements: 2.1, 2.3, 2.4, 4.1, 4.2, 4.3, 4.4_

- [x] 6. 更新 RBAC 和 Manifests
  - [x] 6.1 在 `config/base/cluster-role.yaml` 中新增 `delete` 动词
    - 在 `["get", "list", "watch"]` 后追加 `"delete"`
    - _Requirements: 5.1, 5.2, 5.3_

  - [x] 6.2 运行 `make manifests` 重新生成 `deploy.yaml`
    - 确认 deploy.yaml 中 ClusterRole 包含 `delete`
    - _Requirements: 5.1_

- [x] 7. 更新文档
  - [x] 7.1 更新 README.md 安全不变量说明
    - 添加 opt-in 过期删除声明，说明仅在 `expiredAction: delete` 时删除
    - _Requirements: 7.2_

  - [x] 7.2 更新 `config/base/policy-configmap.yaml` 示例配置
    - 添加 `expiredAction: delete` 注释示例
    - _Requirements: 1.1_

- [x] 8. 全量验证
  - [x] 8.1 运行 `go test ./...` 和 `go vet ./...` 确认无回归
    - 所有现有测试不受影响
    - 新增测试全部通过
    - _Requirements: 1.1-7.3_

## Notes
- `ExpiredAction` 默认为 "scale"，不配置时行为与之前完全一致，无需状态迁移。
- `DeleteWorkload=true` 时 `NeedsSnapshot`、`ShouldScale`、`ClearSnapshotAfterScale` 均为 false，Reconciler 在进入 `applyDecision` 前即返回。
- NewReconciler 签名变更会影响现有测试中的构造调用，需同步更新 test helper。
- 删除使用 Foreground 策略让 Kubernetes GC 负责 Pod 清理，Controller 不直接删 Pod。

## Task Dependency Graph
```json
{
  "waves": [
    { "id": 0, "tasks": ["1.1", "1.2"] },
    { "id": 1, "tasks": ["1.3", "2.1", "2.2", "2.3"] },
    { "id": 2, "tasks": ["1.4", "2.4"] },
    { "id": 3, "tasks": ["2.5", "3"] },
    { "id": 4, "tasks": ["4.1"] },
    { "id": 5, "tasks": ["4.2", "5.1"] },
    { "id": 6, "tasks": ["5.2", "5.3"] },
    { "id": 7, "tasks": ["5.4"] },
    { "id": 8, "tasks": ["6.1", "7.1", "7.2"] },
    { "id": 9, "tasks": ["6.2"] },
    { "id": 10, "tasks": ["8.1"] }
  ]
}
```
策略模型和常量定义是基础；决策分支依赖新字段；Deleter 接口依赖决策层完成；Reconciler 编排依赖 Deleter；RBAC/文档可并行于最终测试前完成；全量验证收尾。
