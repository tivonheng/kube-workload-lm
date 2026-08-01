# Implementation Plan: Name Regex Matcher

## Overview
在策略 Target 中新增 `namePatterns` 字段，支持按 Workload 名称正则匹配。按依赖顺序：先扩展类型和解码，再修改校验和匹配引擎，最后更新 Controller 调用点和验收。

## Tasks

- [x] 1. 扩展策略类型和 Raw 模型
  - [x] 1.1 在 `rawTarget` 中新增 `NamePatterns *[]string` yaml 字段
  - [x] 1.2 在 `Target` 结构中新增 `NamePatterns []string` 和 `compiledPatterns []*regexp.Regexp`
  - _Requirements: 1, 3_

- [x] 2. 实现正则预编译与校验
  - [x] 2.1 实现 `compileNamePatterns` 函数：空字符串拒绝、非法 RE2 拒绝、自动锚定 `^(?:pattern)$`
  - [x] 2.2 修改 `compileTarget`：selector 可选性调整（有 namePatterns 时允许省略 selector；两者都无则拒绝）
  - [x] 2.3 添加编译测试：有效正则编译、空字符串拒绝、非法正则拒绝、省略 selector 场景
  - _Requirements: 4, 5, 6_

- [x] 3. 实现匹配逻辑
  - [x] 3.1 修改 `Target.matches` 方法签名新增 `name string`，实现 `kindMatches AND (selectorMatches OR namePatternMatches)`
  - [x] 3.2 修改 `Match` 函数签名新增 `name string` 参数，传递给 target.matches
  - [x] 3.3 添加匹配测试：全量匹配语义、OR 组合、仅 selector、仅 namePatterns、空 name、优先级和冲突不变
  - _Requirements: 2, 6, 7, 8_

- [x] 4. 更新 Controller 调用点
  - [x] 4.1 修改 `orchestrator.go` 中 `reconcileOne` 的 `policy.Match` 调用，补传 `current.Name`
  - [x] 4.2 修改 orchestrator_test.go 中所有 `Match` 调用适配新签名
  - [x] 4.3 修改其他引用 `policy.Match` 的测试文件适配新签名
  - _Requirements: 8_

- [x] 5. 验收与文档
  - [x] 5.1 更新 policy ConfigMap 示例添加 namePatterns 用例
  - [x] 5.2 更新 deploy.yaml 使用 namePatterns 匹配 `pfb\d+`
  - [x] 5.3 运行 `go test ./...`、`go vet ./...`、`make manifests` 全量验证
  - [x] 5.4 更新 README 策略配置章节说明 namePatterns 用法
  - _Requirements: 1-8_

## Task Dependency Graph
```json
{
  "waves": [
    {"wave": 1, "tasks": ["1"]},
    {"wave": 2, "tasks": ["2"]},
    {"wave": 3, "tasks": ["3"]},
    {"wave": 4, "tasks": ["4"]},
    {"wave": 5, "tasks": ["5"]}
  ]
}
```
类型扩展是基础，编译校验依赖类型，匹配逻辑依赖编译，Controller 调用依赖匹配签名变更，验收收尾。

## Notes
- 正则使用 Go `regexp` 包（RE2 语法），不支持 lookahead/lookbehind。
- 自动锚定确保全量匹配，用户配置 `pfb\d+` 等效于 `^(?:pfb\d+)$`。
- 向后兼容：现有无 namePatterns 的策略行为完全不变。
