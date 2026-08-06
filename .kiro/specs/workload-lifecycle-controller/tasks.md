# Implementation Plan: Workload Lifecycle Controller

## Overview
按依赖顺序先实现纯领域核心，再接入策略加载、状态存储、Kubernetes API、Controller 编排和发布资产。每个阶段必须通过对应单元或集成验证。

## Tasks

- [x] 1. 建立 Spec
  - [x] 1.1 将 proposal 转换为 requirements、design、tasks
  - [x] 1.2 固化 Revision、时区和 replicaSnapshot 一致性语义

- [x] 2. 初始化领域核心
  - [x] 2.1 初始化 Go module、入口和基础包
  - [x] 2.2 定义 Workload、Policy、State、Decision、Reason 和 Clock
  - [x] 2.3 实现 canonical Revision、trackingSpecHash、revisionHash
  - [x] 2.4 实现同日/跨日/全天 IANA 时间窗口
  - [x] 2.5 实现过期、缩容、恢复、活跃决策状态机
  - _Requirements: 2.1-2.5, 4.1-4.4, 5.1-5.6_

- [x] 3. 实现策略系统
  - [x] 3.1 严格解码、固定默认值和完整校验
  - [x] 3.2 编译 Kubernetes LabelSelector
  - [x] 3.3 实现 kind、selector、priority 和同优先级冲突匹配
  - [x] 3.4 实现最后有效策略的原子快照加载
  - _Requirements: 3.1-3.4_

- [x] 4. 实现状态存储
  - [x] 4.1 定义 StateStore 接口与版本化状态文档
  - [x] 4.2 实现 ConfigMap 创建、读取、更新和冲突退避
  - [x] 4.3 实现无效状态保护、容量指标和删除保留期
  - _Requirements: 5.1-5.6, 6.1, 6.4_

- [x] 5. 接入 Kubernetes Workload
  - [x] 5.1 实现 Deployment/StatefulSet 列表、Watch 和统一模型
  - [x] 5.2 仅通过 scale 子资源读取和更新副本
  - [x] 5.3 实现 HPA target 检测与 Reject 行为
  - _Requirements: 1.1-1.3, 6.2-6.3_

- [x] 6. 实现 Controller 编排
  - [x] 6.1 实现首次发现、UID/Tracking/Revision 转换
  - [x] 6.2 实现“快照后缩容、恢复后清快照”的幂等顺序
  - [x] 6.3 实现 Watch 触发、30s 全量 Reconcile 和失败隔离
  - _Requirements: 2.5, 4.4, 5.1-5.6, 6.3-6.4_

- [x] 7. 实现运维能力
  - [x] 7.1 JSON 日志、Prometheus 指标、healthz/readyz
  - [x] 7.2 Lease Leader Election 与 SIGTERM 优雅退出
  - _Requirements: 6.5_

- [x] 8. 完成发布与验收
  - [x] 8.1 RBAC、Deployment、Kustomize、策略/状态/Workload 示例
  - [x] 8.2 多阶段非 root Dockerfile、Makefile、README 和排障文档
  - [x] 8.3 单元测试、fake client 测试、envtest 集成测试和多架构构建
  - _Requirements: 6.6, 7.1-7.2_

## Task Dependency Graph
```json
{
  "waves": [
    {"wave": 1, "tasks": ["1"]},
    {"wave": 2, "tasks": ["2"]},
    {"wave": 3, "tasks": ["3", "4"]},
    {"wave": 4, "tasks": ["5"]},
    {"wave": 5, "tasks": ["6"]},
    {"wave": 6, "tasks": ["7"]},
    {"wave": 7, "tasks": ["8"]}
  ]
}
```
领域任务可在同一阶段并行开发，但 Kubernetes 编排必须依赖已验证的策略、状态和决策接口。

## Notes
- 每完成一个任务即运行对应定向测试，并在阶段边界运行 `go test ./...`、`go vet ./...` 和构建。
- 不允许为通过测试而绕过 scale 子资源、快照事务顺序或严格配置校验。