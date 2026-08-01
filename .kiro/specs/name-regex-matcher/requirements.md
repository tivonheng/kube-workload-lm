# Requirements Document

## Introduction

本特性为策略 Target 新增可选的 `namePatterns` 字段，允许用户通过 Go RE2 正则表达式按 Workload 名称模式匹配目标，无需逐一为 Workload 打标签。该功能作为现有 LabelSelector 的补充匹配条件，与 kind 匹配共同决定策略是否生效。

## Glossary

- **PolicySet**: 策略文档的顶层对象，包含一组策略定义，作为原子快照加载和校验
- **Policy**: 单条生命周期管理策略，包含 target、lifecycle、replicas、schedule 等配置
- **Target**: 策略中定义匹配目标的配置块，包含 kinds、selector 和 namePatterns
- **Name_Pattern**: 一个 Go RE2 语法的正则表达式字符串，用于匹配 Workload 的 Name 字段
- **Matcher**: `internal/policy/matcher.go` 中的策略匹配引擎，负责根据 Target 配置判断 Workload 是否匹配策略
- **Workload**: 被管理的 Kubernetes Deployment 或 StatefulSet，包含 Kind、Namespace、Name、UID、Labels、Containers 字段
- **Compiled_Regex**: 策略加载阶段通过 `regexp.Compile` 预编译的正则表达式对象，用于运行时高效匹配
- **LabelSelector**: Kubernetes metav1.LabelSelector 语义的标签选择器，当前策略 target 使用的匹配方式

## Requirements

### Requirement 1: Target 新增 namePatterns 字段

**User Story:** As a 平台工程师, I want to 在策略 target 中定义名称正则表达式列表, so that 符合命名模式的 Workload 自动纳入生命周期管理而无需逐一打标签。

#### Acceptance Criteria

1. THE Target SHALL support an optional `namePatterns` field that accepts a list of Go RE2 regular expression strings
2. WHEN `namePatterns` is omitted or empty, THE Target SHALL not impose any name-based matching condition
3. WHEN `namePatterns` contains one or more patterns, THE Target SHALL treat a Workload name as matching if the name matches at least one pattern in the list

### Requirement 2: 组合匹配语义

**User Story:** As a 平台工程师, I want namePatterns 与 LabelSelector 作为 OR 关系组合使用, so that 通过名称模式或标签任一方式都能将 Workload 纳入策略匹配范围。

#### Acceptance Criteria

1. THE Matcher SHALL apply the following combined matching logic: kind matches AND (LabelSelector matches OR any namePattern matches)
2. WHEN a Workload satisfies kind matching and LabelSelector matching, THE Matcher SHALL report the Workload as matched regardless of namePatterns evaluation
3. WHEN a Workload satisfies kind matching and at least one namePattern matches the Workload Name, THE Matcher SHALL report the Workload as matched regardless of LabelSelector evaluation
4. WHEN a Workload satisfies kind matching but neither LabelSelector nor any namePattern matches, THE Matcher SHALL report the Workload as not matched

### Requirement 3: 向后兼容

**User Story:** As a 现有用户, I want 不配置 namePatterns 的策略行为与升级前完全一致, so that 升级控制器版本不会影响已有策略的匹配行为。

#### Acceptance Criteria

1. WHEN `namePatterns` is not configured in a policy target, THE Matcher SHALL evaluate matching using only kind and LabelSelector, producing identical results to the pre-feature behavior
2. WHEN `namePatterns` is configured as an empty list, THE Matcher SHALL behave identically to when `namePatterns` is not configured
3. THE PolicySet SHALL accept policies that omit the `namePatterns` field without requiring any migration or schema changes to existing policy documents

### Requirement 4: Selector 可选性调整

**User Story:** As a 平台工程师, I want to 仅使用 namePatterns 匹配而不配置 LabelSelector, so that 纯名称模式匹配的策略不需要设置无意义的标签条件。

#### Acceptance Criteria

1. WHEN `namePatterns` is configured with at least one pattern, THE PolicySet validation SHALL allow `selector` to be omitted or empty
2. WHEN both `selector` and `namePatterns` are omitted or empty, THE PolicySet validation SHALL reject the policy target with a descriptive error indicating that at least one matching condition is required
3. WHEN `selector` is configured without `namePatterns`, THE PolicySet validation SHALL accept the policy target using existing validation logic

### Requirement 5: 正则预编译与校验

**User Story:** As a 平台工程师, I want 非法正则在策略加载时立即拒绝整个 PolicySet, so that 运行时不会因正则语法错误导致匹配异常。

#### Acceptance Criteria

1. WHEN a PolicySet is loaded, THE Validator SHALL compile each namePattern using Go `regexp.Compile` (RE2 syntax) during policy compilation
2. IF any namePattern fails to compile, THEN THE Validator SHALL reject the entire PolicySet update with an error message indicating the policy name, pattern index, and compilation error
3. WHEN all namePatterns compile successfully, THE Matcher SHALL use the pre-compiled regex objects for runtime matching without recompiling on each evaluation
4. THE Validator SHALL reject a namePattern that is an empty string with a descriptive error

### Requirement 6: 全量匹配语义

**User Story:** As a 平台工程师, I want namePatterns 对 Workload Name 执行全量匹配, so that 正则 `pfb\d+` 只匹配完全符合该模式的名称而不会部分匹配。

#### Acceptance Criteria

1. THE Matcher SHALL apply each namePattern as a full-match against the entire Workload Name (equivalent to anchoring with `^` and `$`)
2. WHEN a namePattern matches only a substring of the Workload Name, THE Matcher SHALL report the pattern as not matched
3. WHEN a namePattern matches the entire Workload Name, THE Matcher SHALL report the pattern as matched

### Requirement 7: 优先级和冲突规则不变

**User Story:** As a 平台工程师, I want namePatterns 引入后策略优先级和冲突检测逻辑保持不变, so that 多策略匹配的行为仍然可预测。

#### Acceptance Criteria

1. WHEN multiple policies match a Workload through any combination of LabelSelector and namePatterns, THE Matcher SHALL select the highest priority policy using existing priority rules
2. WHEN multiple policies with the same highest priority match a Workload, THE Matcher SHALL report a conflict using existing conflict detection logic
3. THE Matcher SHALL not introduce any new priority or conflict resolution rules specific to namePatterns-based matching

### Requirement 8: Matcher 函数签名扩展

**User Story:** As a 控制器开发者, I want Match 函数接受 Workload Name 参数, so that 匹配引擎可以对名称执行正则匹配。

#### Acceptance Criteria

1. THE Match function SHALL accept the Workload Name as an input parameter in addition to existing kind and labels parameters
2. THE Matcher SHALL pass the Workload Name to the Target matching logic for namePatterns evaluation
3. WHEN the Workload Name parameter is empty, THE Matcher SHALL treat all namePatterns as not matched for that Workload
