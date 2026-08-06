# Design Document

## Overview

在策略 Target 中新增可选 `namePatterns` 字段，支持 Go RE2 正则表达式按 Workload 名称全量匹配。该功能与现有 LabelSelector 形成 OR 关系组合，使得策略匹配可以同时覆盖"标签选择"和"命名模式选择"两种场景，无需逐一为 Workload 打标签。

## Architecture

```text
PolicySet YAML
  │
  ▼
Decode → Validate → CompileTarget
  │                     │
  │        ┌────────────┴────────────┐
  │        │ compileSelector()       │ compileNamePatterns()
  │        │ → labels.Selector       │ → []*regexp.Regexp
  │        └────────────┬────────────┘
  │                     ▼
  │              Target (compiled)
  │                     │
  ▼                     ▼
Match(set, kind, name, labels) → MatchResult
         │
         ▼
  target.matches(kind, name, labels)
    = kindMatches AND (selectorMatches OR namePatternMatches)
```

变更集中在 `internal/policy` 包，不影响 state、workload、lifecycle、schedule 等其他包。Controller 层 `reconcileOne` 调用 `Match` 时补传 `workload.Name` 即可。

## Components and Interfaces

### Target 类型扩展

```go
// internal/policy/types.go
type Target struct {
    Kinds            []workload.Kind
    Selector         metav1.LabelSelector
    compiledSelector labels.Selector        // nil when selector is omitted
    NamePatterns     []string               // raw patterns for serialization/logging
    compiledPatterns []*regexp.Regexp        // pre-compiled at load time
}
```

`compiledSelector` 现有字段在无 selector 时改为存储 `nil`（当前 `compileSelector` 要求非空会拒绝）。新增 `namePatterns` 后，`compileSelector` 改为在存在 namePatterns 时允许省略 selector。

### matches 方法签名变更

```go
func (target Target) matches(kind workload.Kind, name string, workloadLabels map[string]string) bool {
    if !target.kindMatches(kind) {
        return false
    }
    if target.selectorMatches(workloadLabels) {
        return true
    }
    return target.namePatternMatches(name)
}

func (target Target) kindMatches(kind workload.Kind) bool { ... }

func (target Target) selectorMatches(workloadLabels map[string]string) bool {
    if target.compiledSelector == nil {
        return false
    }
    return target.compiledSelector.Matches(labels.Set(workloadLabels))
}

func (target Target) namePatternMatches(name string) bool {
    if name == "" || len(target.compiledPatterns) == 0 {
        return false
    }
    for _, pattern := range target.compiledPatterns {
        if pattern.MatchString(name) {
            return true
        }
    }
    return false
}
```

### Match 函数签名

```go
// internal/policy/matcher.go
func Match(set PolicySet, kind workload.Kind, name string, workloadLabels map[string]string) MatchResult
```

新增 `name string` 参数。调用者（`controller/orchestrator.go` 的 `reconcileOne`）传入 `current.Name`。

### Raw 模型扩展

```go
// internal/policy/raw.go
type rawTarget struct {
    Kinds        *[]workload.Kind    `yaml:"kinds"`
    Selector     *rawSelector        `yaml:"selector"`
    NamePatterns *[]string           `yaml:"namePatterns"`
}
```

### 编译函数

```go
// internal/policy/validate.go
func compileNamePatterns(raw *[]string, policyName string) ([]*regexp.Regexp, []string, error) {
    if raw == nil || len(*raw) == 0 {
        return nil, nil, nil
    }
    patterns := make([]string, 0, len(*raw))
    compiled := make([]*regexp.Regexp, 0, len(*raw))
    for i, pattern := range *raw {
        if pattern == "" {
            return nil, nil, fmt.Errorf("namePatterns[%d]: pattern must be non-empty", i)
        }
        // 全量匹配：自动锚定
        anchored := "^(?:" + pattern + ")$"
        re, err := regexp.Compile(anchored)
        if err != nil {
            return nil, nil, fmt.Errorf("namePatterns[%d] %q: %w", i, pattern, err)
        }
        patterns = append(patterns, pattern)
        compiled = append(compiled, re)
    }
    return compiled, patterns, nil
}
```

### compileTarget 修改

```go
func compileTarget(raw *rawTarget) (Target, error) {
    // ... kinds validation (unchanged) ...

    hasSelector := raw.Selector != nil &&
        (len(raw.Selector.MatchLabels) > 0 || len(raw.Selector.MatchExpressions) > 0)
    hasNamePatterns := raw.NamePatterns != nil && len(*raw.NamePatterns) > 0

    if !hasSelector && !hasNamePatterns {
        return Target{}, fmt.Errorf("at least one of selector or namePatterns must be configured")
    }

    var selector metav1.LabelSelector
    var compiledSelector labels.Selector
    if hasSelector {
        var err error
        selector, compiledSelector, err = compileSelector(raw.Selector)
        if err != nil {
            return Target{}, err
        }
    }

    compiledPatterns, namePatterns, err := compileNamePatterns(raw.NamePatterns, "")
    if err != nil {
        return Target{}, err
    }

    return Target{
        Kinds: kinds, Selector: selector, compiledSelector: compiledSelector,
        NamePatterns: namePatterns, compiledPatterns: compiledPatterns,
    }, nil
}
```

## Data Models

无新增持久化模型。`namePatterns` 仅存在于 PolicySet YAML 配置和内存编译状态中。

配置示例：

```yaml
target:
  kinds: [Deployment, StatefulSet]
  namePatterns:
    - "pfb\\d+"
    - "staging-.*"
```

仅用 namePatterns（无 selector）：

```yaml
target:
  kinds: [Deployment]
  namePatterns:
    - "pfb\\d+"
```

混合使用（OR 语义）：

```yaml
target:
  kinds: [Deployment, StatefulSet]
  selector:
    matchLabels:
      lifecycle.example.com/policy: managed
  namePatterns:
    - "pfb\\d+"
```

## Reconcile 影响

`controller/orchestrator.go` 的 `reconcileOne` 方法调用 `policy.Match` 时增加 `current.Name` 参数：

```go
matched := policy.Match(set, current.Kind, current.Name, current.Labels)
```

其余 reconcile 逻辑、决策引擎、状态管理均不变。

## Error Handling

- 空字符串 pattern → 拒绝 PolicySet，报告 policy 名和 index
- 非法 RE2 语法 → 拒绝 PolicySet，报告 policy 名、index 和编译错误
- 空 workload name → 所有 namePatterns 视为不匹配（不触发 panic）
- 整个 PolicySet 原子拒绝语义不变

## Correctness Properties

### Property 1: Backward Compatibility
**Validates: Requirement 3**

不配置 `namePatterns` 的策略，匹配结果与未安装此功能时完全一致。

### Property 2: Full-Match Semantics
**Validates: Requirement 6**

正则 `pfb\d+` 匹配 `pfb123` 但不匹配 `my-pfb123` 或 `pfb123-extra`。

### Property 3: OR Combination
**Validates: Requirement 2**

满足 kind 且 (LabelSelector 或 namePattern 任一) 即匹配；两者皆不满足则不匹配。

### Property 4: Priority Invariance
**Validates: Requirement 7**

namePatterns 匹配不改变优先级选择和冲突检测逻辑。

## Testing Strategy

- 表驱动单测覆盖：全量匹配语义（锚定行为）、OR 组合逻辑、仅 selector / 仅 namePatterns / 混合、空 name、空 patterns、优先级和冲突不变。
- 编译校验测试：空字符串拒绝、非法正则拒绝、有效正则编译成功、省略 selector 时需 namePatterns。
- 集成测试：controller reconcileOne 传入 name 后匹配正确策略。
- 向后兼容回归：现有测试不修改断言仍通过。
