请帮我开发一个 Kubernetes Workload Lifecycle Controller，用于基于镜像版本生命周期和时间策略，自动管理 Deployment 与 StatefulSet 的副本数。

一、目标

该控制器需要同时支持：

* Deployment
* StatefulSet

控制器不能修改目标 Workload 的模板配置，只允许：

* 读取 Workload 信息
* 读取容器镜像
* 读取 Workload Labels
* 通过 scale 子资源修改副本数

目标 Workload 由外部发布系统维护，因此不能依赖修改 Deployment 或 StatefulSet 本身来保存状态。

⸻

二、核心生命周期与 Revision 识别

控制器根据 Workload 的容器镜像集合判断是否发布了新的 Workload Revision。生命周期的管理单位是“被跟踪容器的镜像集合”，不是每个容器镜像各自独立计时。

镜像来源：

spec.template.spec.containers[].name
spec.template.spec.containers[].image

默认行为：跟踪 spec.template.spec.containers 中的全部普通容器，不包含 initContainers 和 ephemeral containers。任意普通容器的 image 字符串发生变化，都会生成新的 Revision，并重新开始生命周期计时。

默认参与 Hash 的规范化 Revision 内容例如：

[
  {
    "container": "app",
    "image": "registry.example.com/app:v1.2.3"
  },
  {
    "container": "sidecar",
    "image": "registry.example.com/sidecar:v2.0.0"
  }
]

计算规则：

1. 只保留 container name 和 image 字段。
2. 按 container name 升序排序。
3. 使用固定字段顺序和 UTF-8 编码进行规范化 JSON 序列化。
4. 对序列化结果计算 SHA-256。
5. 结果统一表示为 sha256:<lowercase-hex>。

该 Hash 命名为 revisionHash。不能直接使用整个 PodTemplateSpec 的 Hash，也不能直接依赖 Deployment 的 pod-template-hash 或 StatefulSet 的 controller-revision-hash，因为 resources、env、annotations 等非镜像字段变化不应重置生命周期。

每个 Revision 有一个 firstSeenAt：

expiresAt = firstSeenAt + lifecycle.maxAge

expiresAt 是根据当前策略动态计算的派生值，不作为状态中的权威字段。当 now >= expiresAt 时：

replicas = replicas.expired

默认 replicas.expired = 0。

为了避免 Sidecar 镜像变化重置生命周期，策略可以显式指定只跟踪部分容器：

lifecycle:
  revision:
    source: ContainerImages
    containers:
      - app

containers 未配置时，默认跟踪全部普通容器；显式 containers: [] 非法。指定的容器不存在时，该 Workload 本轮不得缩放，并记录 tracked-container-not-found 错误。

控制器还需要计算 trackingSpecHash，用于标识 Revision 的识别方式。其输入至少包含：

{
  "version": 1,
  "source": "ContainerImages",
  "containers": ["app"]
}

containers 按名称排序；默认全容器模式必须使用明确的规范值表示。trackingSpecHash 与 revisionHash 必须分开保存，不能合并为一个 Hash，以便区分“识别规则变化”和“镜像变化”。

生命周期状态变化规则：

1. workloadUID 变化：视为新的 Workload，重新建立生命周期。
2. trackingSpecHash 未变化、revisionHash 变化：视为新的镜像 Revision，重新记录 firstSeenAt。
3. trackingSpecHash 变化：视为 Revision 识别规则变化，使用当前 Revision 重新建立基线，reason 为 revision-definition-changed，不能伪装成镜像发布。
4. 其他变化不得重置 firstSeenAt。

以下变化不能重置生命周期：

* replicas 变化
* resourceVersion 变化
* generation 变化但被跟踪镜像集合未变化
* Pod 被删除或重建
* Deployment 或 StatefulSet 状态变化
* 控制器自身重启
* 时间窗口或时区变化
* maxAge 或目标副本数变化
* Workload Label 或匹配策略变化，但 Revision 识别规则和被跟踪镜像集合未变化

必须注意：如果配置 image: app:latest，而 image 字符串没有变化，控制器无法判断 Registry 中的实际内容是否变化。推荐使用不可变镜像标识：

app:<git-commit-sha>

或：

app@sha256:<digest>

⸻

三、基于 Target 与 Label Selector 选择策略

不同 Label 的 Workload 可以应用不同生命周期策略。不需要额外的全局 managedSelector，所有目标选择条件统一放在策略的 target 下：

target:
  kinds:
    - Deployment
    - StatefulSet
  selector:
    matchLabels:
      lifecycle.example.com/policy: temporary-3d
    matchExpressions:
      - key: environment
        operator: In
        values:
          - test
          - staging

规则：

* target.kinds 必须非空，只能包含 Deployment 或 StatefulSet，并且不能重复。
* target.selector 直接采用 Kubernetes metav1.LabelSelector 语义。
* selector 必须至少包含一个 matchLabels 或 matchExpressions 条件，空 selector 非法，避免意外匹配全集群。
* 支持 In、NotIn、Exists、DoesNotExist。
* Selector 中的多个条件使用 AND 逻辑。
* 未匹配任何策略的 Workload 必须忽略，不得修改副本数。
* 未来如需增加 Namespace 范围，应扩展在 target 下，不增加第二层全局 Selector。

⸻

四、策略匹配冲突

一个 Workload 可能匹配多条策略。

每条策略增加：

priority: 100

规则：

1. 优先选择 priority 数值最大的策略。
2. 如果多个匹配策略拥有相同的最高优先级，认为存在配置冲突。
3. 出现冲突时不得修改该 Workload 的副本数。
4. 需要记录明确的错误日志。
5. 最好增加 Prometheus 错误指标。

不能依赖策略在 YAML 中的配置顺序决定优先级。

⸻

五、时间缩容策略与时区

时间配置统一放在 schedule 下。每条策略可以使用独立的 IANA Time Zone：

schedule:
  timeZone: Asia/Shanghai
  downWindows:
    - name: weekday-early-morning
      startDays:
        - MON
        - TUE
        - WED
        - THU
        - FRI
      start: "00:00"
      end: "08:00"
    - name: weekend
      startDays:
        - SAT
        - SUN
      allDay: true

支持的时区例如：

* Asia/Shanghai
* America/Los_Angeles
* UTC

schedule 未配置时，表示不启用时间缩容。配置 schedule 时，timeZone 必填并且必须能由 IANA Time Zone Database 加载；downWindows 必须非空。

窗口统一采用左闭右开区间 [start, end)，即开始边界命中、结束边界不命中。

需要支持：

1. 普通同日窗口

startDays:
  - MON
start: "00:00"
end: "08:00"

表示星期一 [00:00, 08:00)。

2. 全天窗口

startDays:
  - SAT
  - SUN
allDay: true

表示周六和周日全天缩容。

3. 跨天窗口

startDays:
  - FRI
start: "20:00"
end: "08:00"

表示星期五 20:00 至星期六 08:00，startDays 表示窗口开始的星期。

4. 多个窗口

当前时间命中任意一个窗口即进入定时缩容状态。如果同时命中多个窗口，使用按配置名称排序后的第一个窗口名称作为稳定的日志 reason，不得依赖 YAML 顺序决定业务结果。

窗口校验规则：

* start 和 end 使用 HH:MM 24 小时格式。
* start == end 非法；全天必须使用 allDay: true。
* allDay 与 start/end 互斥。
* 普通窗口必须同时配置 start 和 end。
* startDays 必须非空且不能重复，只允许 MON、TUE、WED、THU、FRI、SAT、SUN。
* window name 在单条策略内唯一。

夏令时按策略时区的本地墙上时间判断：秋季重复小时的两个实际时间段都命中，春季不存在的本地时间不进行补偿执行。所有持久化时间仍统一使用 UTC RFC3339，时区仅用于窗口匹配。

⸻

六、副本数决策、缩容快照与恢复

策略只配置两个缩容目标，不配置固定的活跃副本数：

replicas:
  scheduledDown: 0
  expired: 0

默认值均为 0，且必须是非负整数。活跃状态下的副本数由外部发布系统维护，Controller 不使用固定 active 值覆盖它。

Controller 在第一次进入任意缩容状态前，必须读取 scale 子资源中的当前副本数，将其持久化为 replicaSnapshot。恢复时使用该快照中的原副本数：

replicaSnapshot:
  replicas: 3
  capturedAt: "2026-08-01T15:59:59Z"
  capturedReason: "scale-down-window:weekend"

副本数判断顺序固定为：

1. 当前 Revision 生命周期是否已过期。
2. 当前时间是否命中定时缩容窗口。
3. 其他情况进入活跃状态，恢复缩容前副本数。

伪代码：

expiresAt = state.firstSeenAt + policy.lifecycle.maxAge

if now >= expiresAt:
    ensureReplicaSnapshotPersisted(currentReplicas)
    desiredReplicas = min(policy.replicas.expired, state.replicaSnapshot.replicas)
    reason = revision-lifecycle-expired
else if currentTime matches any policy.schedule.downWindows:
    ensureReplicaSnapshotPersisted(currentReplicas)
    desiredReplicas = min(policy.replicas.scheduledDown, state.replicaSnapshot.replicas)
    reason = scale-down-window:<window-name>
else if state.replicaSnapshot exists:
    desiredReplicas = state.replicaSnapshot.replicas
    reason = restore-previous-replicas
else:
    desiredReplicas = currentReplicas
    reason = active-window

使用 min 可以保证“缩容状态”永远不会把低副本 Workload 反向扩容。例如缩容目标为 2，而进入窗口前只有 1 个副本时，窗口内保持 1 个副本，恢复值仍为 1。

快照和恢复必须满足以下状态机规则：

* 进入缩容状态时，如果 replicaSnapshot 不存在，先以当前 scale.spec.replicas 创建并持久化快照，成功后才能执行缩容。
* 已存在 replicaSnapshot 时不得再次采集，防止外部系统在缩容期间 Apply replicas 后覆盖原恢复值。
* 从定时缩容状态转为生命周期过期状态时沿用原快照，不得重新采集当前的低副本数。
* 生命周期过期后不能仅因进入活跃时间窗口而恢复。
* 同一 workloadUID 检测到新 Revision 或 Revision 识别规则变化后，生命周期重新开始；如果当前不在缩容窗口，则允许恢复 replicaSnapshot 中的副本数。
* workloadUID 变化表示同名的新 Workload：重新建立生命周期并丢弃旧 replicaSnapshot，不得恢复旧资源的副本数。
* 新 Revision 出现时如果仍命中缩容窗口，保留 replicaSnapshot 并继续缩容；离开窗口后恢复快照副本数。
* 新 Revision 出现时如果当前不在缩容窗口，应恢复 replicaSnapshot 中的副本数。
* 恢复时先成功调用 scale 子资源，再清除 replicaSnapshot。清除状态失败时下次 Reconcile 重复执行相同恢复，必须保证幂等。
* replicaSnapshot 不存在且处于活跃状态时，不修改副本数；外部发布系统可以自由调整活跃副本数，并在下一次缩容前被重新采集。
* 策略切换或副本目标值变化不能覆盖已经存在的 replicaSnapshot。

maxAge、schedule、timeZone 或 replicas 配置变化不会修改 firstSeenAt。maxAge 变化立即作用于已有状态，expiresAt 始终由原 firstSeenAt 加当前有效策略的 maxAge 动态计算。

⸻

七、完整策略 Schema 与示例

策略 ConfigMap 中保存一个版本化的完整 PolicySet 文档。一次更新必须作为原子快照加载，不能逐条混用新旧策略。

apiVersion: lifecycle.example.com/v1alpha1
kind: WorkloadLifecyclePolicySet
spec:
  policies:
    - name: temporary-workloads-3d
      priority: 100

      target:
        kinds:
          - Deployment
          - StatefulSet
        selector:
          matchLabels:
            lifecycle.example.com/policy: temporary-3d
          matchExpressions:
            - key: environment
              operator: In
              values:
                - test
                - staging

      lifecycle:
        maxAge: 72h
        revision:
          source: ContainerImages

      replicas:
        scheduledDown: 0
        expired: 0

      schedule:
        timeZone: Asia/Shanghai
        downWindows:
          - name: weekday-early-morning
            startDays:
              - MON
              - TUE
              - WED
              - THU
              - FRI
            start: "00:00"
            end: "08:00"
          - name: weekend
            startDays:
              - SAT
              - SUN
            allDay: true

    - name: staging-7d
      priority: 200

      target:
        kinds:
          - Deployment
        selector:
          matchLabels:
            lifecycle.example.com/policy: staging-7d

      lifecycle:
        maxAge: 168h
        revision:
          source: ContainerImages
          containers:
            - api

      replicas:
        scheduledDown: 0
        expired: 0

      schedule:
        timeZone: America/Los_Angeles
        downWindows:
          - name: every-night
            startDays:
              - MON
              - TUE
              - WED
              - THU
              - FRI
              - SAT
              - SUN
            start: "22:00"
            end: "08:00"

第一条策略未配置 revision.containers，因此默认跟踪全部普通容器；任一容器 image 字符串变化都会产生新的 revisionHash。第二条策略只跟踪名为 api 的容器，其他 Sidecar 镜像变化不会重置生命周期。

内置默认值：

* lifecycle.maxAge: 72h
* lifecycle.revision.source: ContainerImages
* lifecycle.revision.containers: 全部普通容器
* replicas.scheduledDown: 0
* replicas.expired: 0

不提供 replicas.active。活跃副本数来自缩容前持久化的 replicaSnapshot；未持有快照时，Controller 在活跃状态不修改副本数。

V1 不提供可配置的全局 defaults，避免引入继承、列表合并和热更新影响范围等额外语义。策略文档中出现未知字段或显式 null 必须拒绝加载。

⸻

八、状态持久化

控制器不能依赖内存保存生命周期状态，因为控制器可能重启。状态必须通过独立的 StateStore 抽象持久化到 Kubernetes 中；第一版使用指定 Namespace 下的独立 ConfigMap：

apiVersion: v1
kind: ConfigMap
metadata:
  name: workload-lifecycle-state
  namespace: lifecycle-system
data:
  states.json: |
    {
      "formatVersion": 1,
      "states": {}
    }

每个 Workload 的唯一状态 Key：

<Kind>/<Namespace>/<Name>

例如：

Deployment/test/api
StatefulSet/test/redis

每条状态至少包含：

{
  "formatVersion": 1,
  "states": {
    "Deployment/test/api": {
      "kind": "Deployment",
      "namespace": "test",
      "name": "api",
      "workloadUID": "xxxxx",
      "lastPolicyName": "temporary-workloads-3d",
      "trackingSpecHash": "sha256:tracking-hash",
      "revisionHash": "sha256:revision-hash",
      "revision": {
        "source": "ContainerImages",
        "containers": [
          {
            "container": "app",
            "image": "registry.example.com/app:v1.2.3"
          },
          {
            "container": "sidecar",
            "image": "registry.example.com/sidecar:v2.0.0"
          }
        ]
      },
      "firstSeenAt": "2026-08-01T06:00:00Z",
      "lastSeenAt": "2026-08-01T07:00:00Z",
      "replicaSnapshot": {
        "replicas": 3,
        "capturedAt": "2026-08-01T06:30:00Z",
        "capturedReason": "scale-down-window:weekday-early-morning"
      }
    }
  }
}

状态语义：

* formatVersion 独立于策略 apiVersion，用于状态迁移。
* lastPolicyName 仅用于诊断，不参与 Revision 身份判断，策略切换本身不能重置生命周期。
* trackingSpecHash 标识 Revision 识别规则。
* revisionHash 标识当前被跟踪容器的镜像集合。
* revision.containers 保存参与 Hash 的规范化输入，并按容器名排序，便于审计和故障排查。
* replicaSnapshot 是可选字段；存在时表示 Controller 曾进入缩容状态，并保存恢复所需的原副本数。
* replicaSnapshot.replicas 来自缩容前通过 scale 子资源读取的 scale.spec.replicas，表示期望副本配置；不能使用 status.replicas、readyReplicas，也不能使用缓存中的过期 Workload 对象。
* replicaSnapshot 是可选对象，不能使用 replicas = 0 作为“快照不存在”的哨兵值，因为 0 本身是合法的恢复副本数。
* replicaSnapshot.capturedAt 使用 UTC RFC3339；capturedReason 记录首次创建快照的原因，仅用于审计。
* expiresAt 不作为权威状态持久化，应由 firstSeenAt 和当前策略 maxAge 动态计算。
* 所有时间统一使用 UTC RFC3339 保存；策略 timeZone 只用于时间窗口判断。

一致性要求：

* 首次发现 Workload、新 Revision、Workload UID 变化或 Revision 识别规则变化时，必须先成功持久化新状态，再根据新状态执行扩容或缩容。
* 缩容采用“先持久化快照、后修改 scale”的顺序。快照写入失败时不得缩容。
* 恢复采用“先恢复 scale、后清除快照”的顺序。清除失败时保留快照并在下一轮幂等重试。
* 同一个 Workload 从定时缩容转为生命周期过期时，必须沿用原 replicaSnapshot。
* 同一 Workload 检测到新 Revision 时保留 replicaSnapshot，以便在允许恢复时恢复到缩容前副本数。
* workloadUID 变化表示同名的新 Workload，必须丢弃旧 Workload 的 replicaSnapshot，不能把旧资源的副本数恢复到新资源。
* 缩容期间观察到外部系统修改 replicas 时，不得更新 replicaSnapshot；Controller 应继续根据当前缩容原因纠正副本数。
* 状态写入失败时跳过该 Workload 的 scale 操作，但继续处理其他 Workload。
* ConfigMap 不存在或被删除时自动初始化，并将当前 Revision 视为首次发现。已经缩容的 Workload 会永久丢失原 replicaSnapshot，Controller 无法从 Workload 本身推导缩容前副本数；此时不得猜测性扩容，必须记录高优先级错误并暴露指标，恢复值需要由外部发布系统或人工重新设置。
* 使用 resourceVersion 乐观并发控制，冲突时采用有界指数退避重试。
* 无效或无法迁移的状态不能静默当作首次发现并扩容；应跳过受影响 Workload、记录错误并暴露指标。
* Workload 暂时未匹配策略时不得修改副本数，但必须保留状态，避免 Label 或策略短暂变化导致生命周期和恢复快照丢失。
* 只要同一 workloadUID 的 Workload 仍存在且 replicaSnapshot 未清除，就不得按 TTL 清理该状态，即使它当前未匹配策略、存在策略冲突或存在 HPA 冲突。
* 只有确认 Workload 已删除后，才能基于 lastSeenAt 和可配置安全保留时间清理状态；不能因单次 List/Watch 故障立即删除。

如果 Workload 被删除后又创建同名资源，workloadUID 会变化，必须视为新的 Workload 并重新开始生命周期。

单个 states.json 受 ConfigMap 约 1 MiB 大小限制，并且共享同一个 resourceVersion，因此只能提供 Reconcile 级失败隔离，无法提供严格的存储失败域隔离。实现必须使用 StateStore 接口，并提供容量指标和告警；数据接近上限时可升级为分片 ConfigMap、CRD 或外部存储，而不修改 Controller 核心逻辑。

⸻

九、Deployment 与 StatefulSet 支持

Deployment

读取：

spec.template.spec.containers
metadata.labels
metadata.uid

修改副本数：

deployments/scale

StatefulSet

读取：

spec.template.spec.containers
metadata.labels
metadata.uid

修改副本数：

statefulsets/scale

统一抽象：

@dataclass
class Workload:
    kind: str
    namespace: str
    name: str
    uid: str
    labels: dict[str, str]
    containers: list
    current_replicas: int

State Key：

f"{kind}/{namespace}/{name}"

⸻

十、控制器运行模式

控制器需要同时采用：

1. Watch

Watch 以下资源：

* Deployment
* StatefulSet
* 策略 ConfigMap

用于快速发现：

* 镜像变化
* Label 变化
* 新 Workload
* 策略变化
* Workload 删除

2. 定期 Reconcile

即使没有 Kubernetes 资源事件，时间也会变化，所以必须定期执行全量 reconcile。

默认运行参数：

--reconcile-interval=30s

该参数属于 Controller 运行配置，不属于业务 PolicySet。

定期 reconcile 用于处理：

* 生命周期到期
* 到达凌晨缩容时间
* 到达早上恢复时间
* 周末开始
* 周末结束
* Watch 断线恢复
* 外部发布系统覆盖 replicas

Watch 不能替代定期 reconcile。

⸻

十一、外部发布系统覆盖副本数

外部发布系统可能在缩容期间重新 Apply Workload，并把 replicas 写回非缩容值。

Controller 必须在下一次 Reconcile 时重新根据当前缩容原因纠正副本数。只要 replicaSnapshot 已存在，缩容期间观察到的 replicas 变化不得覆盖快照，也不能重置 Revision 生命周期。

例如 Workload 在进入窗口前为 3 个副本：

1. Controller 先持久化 replicaSnapshot.replicas = 3。
2. Controller 缩容到 0。
3. 外部系统在窗口内 Apply replicas = 1。
4. Controller 重新纠正为 0，但快照仍保持 3。
5. 窗口结束后 Controller 恢复到 3，而不是恢复到 1。

活跃状态且 replicaSnapshot 不存在时，Controller 不修改 replicas，外部发布系统仍拥有活跃副本数的控制权。

⸻

十二、HPA 冲突

如果目标 Workload 配置了 HPA，HPA 和生命周期 Controller 会同时修改 scale 子资源，且无法可靠维护 replicaSnapshot。

V1 固定采用 Reject 语义：

* 检测目标 Workload 是否被 HPA 管理。
* 检测到 HPA 时跳过该 Workload，不创建或修改 replicaSnapshot，不修改副本数。
* 记录 hpa-conflict 警告并暴露指标。
* V1 不提供 allowHPA 布尔配置；在没有定义清晰的副本所有权和恢复语义前，不支持与 HPA 共存。

⸻

十三、StatefulSet 注意事项

StatefulSet 缩容到 0 后：

* Pod 会被删除。
* PVC 通常不会被删除。
* 存储费用可能继续存在。
* 再扩容后会重新创建原有序号 Pod。
* 会重新挂载原 PVC。

控制器不得删除 PVC。

控制器只负责副本数，不负责：

* 数据备份
* PVC 清理
* 数据恢复
* 数据库选主
* 数据同步

日志中需要明确记录 StatefulSet 缩容操作。

⸻

十四、RBAC

控制器跨 Namespace 运行时，使用 ClusterRole。

最小权限：

rules:
  - apiGroups:
      - apps
    resources:
      - deployments
      - statefulsets
    verbs:
      - get
      - list
      - watch
  - apiGroups:
      - apps
    resources:
      - deployments/scale
      - statefulsets/scale
    verbs:
      - get
      - patch
      - update
  - apiGroups:
      - autoscaling
    resources:
      - horizontalpodautoscalers
    verbs:
      - get
      - list
      - watch
  - apiGroups:
      - ""
    resources:
      - configmaps
    verbs:
      - get
      - list
      - watch
      - create
      - patch
      - update

控制器不能拥有：

* 删除 Deployment 权限
* 删除 StatefulSet 权限
* 删除 Pod 权限
* 删除 PVC 权限
* 修改 Workload Pod Template 权限

只能通过 scale 子资源修改 replicas。

⸻

十五、配置校验

启动时和策略 ConfigMap 更新时必须严格校验：

* apiVersion 和 kind 必须是受支持的值。
* 未知字段和显式 null 非法。
* policy name 唯一且非空。
* priority 是整数，不能依赖 YAML 顺序决定优先级。
* target.kinds 非空、不能重复，只能为 Deployment 或 StatefulSet。
* target.selector 非空。
* lifecycle.maxAge 是合法的正 Duration。
* lifecycle.revision.source V1 只能为 ContainerImages。
* lifecycle.revision.containers 未配置表示全部普通容器；显式空数组非法；配置时名称不能重复。
* replicas.scheduledDown 和 replicas.expired 是非负整数。
* 配置 schedule 时，timeZone 必须是合法 IANA Time Zone，downWindows 必须非空。
* start 和 end 格式必须是 HH:MM，采用 24 小时制，且不能相等。
* startDays 非空、不能重复，只能为 MON、TUE、WED、THU、FRI、SAT、SUN。
* allDay 与 start/end 不能同时配置。
* 普通窗口必须同时配置 start 和 end。
* window name 在单条策略内唯一。
* matchExpressions operator 合法。
* In 和 NotIn 必须有非空 values。
* Exists 和 DoesNotExist 不能配置 values。

策略 ConfigMap 每次更新都作为完整快照原子校验和切换：

* 全部策略有效时才替换当前有效快照。
* 任意错误都拒绝整份新配置，继续使用最后一次完整有效快照。
* 不得逐条接受新策略而形成新旧策略混用。
* 输出明确的 invalid-policy 错误日志并暴露策略加载失败指标。
* Controller 首次启动且没有有效策略快照时不得管理 Workload，并应返回 NotReady。

⸻

十六、日志要求

日志使用结构化 JSON 格式，至少包含：

{
  "level": "info",
  "kind": "Deployment",
  "namespace": "test",
  "name": "api",
  "policy": "temporary-workloads-3d",
  "revisionHash": "sha256:xxxx",
  "firstSeenAt": "2026-08-01T06:00:00Z",
  "expiresAt": "2026-08-04T06:00:00Z",
  "currentReplicas": 3,
  "desiredReplicas": 0,
  "restoreReplicas": 3,
  "reason": "scale-down-window:weekend"
}

restoreReplicas 仅在 replicaSnapshot 存在时输出。主要 reason：

* new-revision-detected
* revision-definition-changed
* revision-lifecycle-expired
* scale-down-window
* replica-snapshot-created
* restore-previous-replicas
* replica-snapshot-cleared
* active-window
* tracked-container-not-found
* no-matching-policy
* policy-conflict
* hpa-conflict
* invalid-policy
* scale-succeeded
* scale-failed
* state-write-failed

StatefulSet 缩容和恢复日志必须明确 kind。避免每 30 秒重复输出完全相同的 Info 日志；状态和副本均未变化时使用 Debug 日志。

⸻

十七、Prometheus Metrics

暴露 HTTP Metrics Endpoint：

/metrics

建议指标：

workload_lifecycle_managed_workloads
workload_lifecycle_revision_age_seconds
workload_lifecycle_revision_expired
workload_lifecycle_desired_replicas
workload_lifecycle_restore_replicas
workload_lifecycle_replica_snapshot_active
workload_lifecycle_scale_operations_total
workload_lifecycle_scale_errors_total
workload_lifecycle_state_errors_total
workload_lifecycle_policy_conflicts_total
workload_lifecycle_policy_reload_errors_total
workload_lifecycle_reconcile_duration_seconds
workload_lifecycle_reconcile_total

标签应控制基数，建议：

kind
namespace
policy
reason
result

谨慎使用 Workload name 和 image 作为指标 Label，避免高基数。

⸻

十八、健康检查

暴露：

/healthz
/readyz

healthz：

* 进程正常即返回成功。

readyz：

* 已加载有效策略。
* 能访问 Kubernetes API。
* 状态存储可读写。

⸻

十九、高可用与并发

第一版可以只运行一个副本。

如果支持多个控制器副本，必须使用 Leader Election，避免多个实例同时修改 Workload 和状态。

建议使用 Kubernetes Lease：

coordination.k8s.io/v1

只有 Leader 执行 reconcile。

其他副本只提供健康检查。

⸻

二十、优雅退出

收到 SIGTERM 后：

* 停止接收新的 Watch 事件。
* 停止启动新的 Reconcile。
* 等待当前 Reconcile 完成。
* 释放 Leader Election Lease。
* 正常退出。

⸻

二十一、技术实现建议

优先使用 Go 开发，基于：

* controller-runtime
* client-go
* Kubernetes Lease Leader Election
* Prometheus client library
* Zap structured logging

如果使用 Go，建议项目结构：

cmd/
  controller/
    main.go
internal/
  controller/
  policy/
  selector/
  lifecycle/
  schedule/
  state/
  workload/
  metrics/
  health/
config/
  rbac/
  manager/
  samples/
Dockerfile
Makefile
go.mod
README.md

项目地址: gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager

需要：

* 多阶段 Docker 构建
* 非 root 用户运行
* 只读根文件系统
* 最小基础镜像
* 支持 amd64 和 arm64
* Helm Chart 或 Kustomize 部署文件

⸻

二十二、测试要求

必须提供单元测试和集成测试。

重点测试场景：

Revision 生命周期

1. 首次发现默认跟踪全部普通容器并创建状态。
2. 全部容器镜像集合未变化时保持 firstSeenAt。
3. 默认模式下任一普通容器镜像变化都会产生新 Revision。
4. 显式只跟踪 app 时，未跟踪 Sidecar 镜像变化不重置生命周期。
5. revision.containers 中指定容器不存在时跳过缩放并报错。
6. replicas、resourceVersion 和非镜像 PodTemplate 字段变化不重置生命周期。
7. trackingSpecHash 变化与 revisionHash 变化能被分别识别。
8. Workload UID 变化后重新开始生命周期并丢弃旧 replicaSnapshot。
9. Controller 重启后恢复原 Revision 状态和 replicaSnapshot。
10. 生命周期到期后保持缩容，直到出现新 Revision。

时间窗口

1. 普通同日窗口。
2. 跨天窗口。
3. 周末全天窗口。
4. 多窗口重叠。
5. 窗口开始边界命中。
6. 窗口结束边界不命中。
7. 不同时区。
8. 夏令时开始与结束。
9. 生命周期过期优先于活跃窗口。

副本快照与恢复

1. 从 3 缩容到 0 前先持久化 replicaSnapshot.replicas = 3。
2. replicaSnapshot 写入失败时不得缩容。
3. 窗口结束后恢复到 3，而不是固定值 1。
4. 恢复 scale 成功后才清除 replicaSnapshot。
5. 恢复后清除状态失败时能够幂等重试。
6. 缩容期间外部 Apply replicas = 1 不覆盖原快照，下一轮重新纠正为 0。
7. 定时缩容期间转为生命周期过期时不重复采集低副本数。
8. 生命周期过期后进入活跃窗口不恢复。
9. 过期后出现新 Revision 且不在缩容窗口时恢复原副本数。
10. 过期后出现新 Revision 但仍在缩容窗口时继续缩容，窗口结束后恢复。
11. 活跃状态无 replicaSnapshot 时不修改外部系统设置的副本数。
12. 缩容目标大于当前副本数时不能反向扩容。
13. Controller 重启后仍能使用持久化快照恢复。
14. 状态 ConfigMap 丢失时不得猜测原副本数并扩容。

策略匹配

1. matchLabels。
2. In。
3. NotIn。
4. Exists。
5. DoesNotExist。
6. 未匹配策略时忽略。
7. 高优先级策略生效。
8. 同优先级冲突时不执行缩放。
9. target.kinds 限制生效。
10. 策略切换但 Revision 识别定义和镜像集合未变化时不重置 firstSeenAt。
11. 非法策略更新整份拒绝并保留上一份有效快照。

Workload

1. Deployment 正常缩放和恢复。
2. StatefulSet 正常缩放和恢复。
3. 只调用 scale 子资源。
4. HPA 冲突时不创建快照、不执行缩放。
5. 外部系统覆盖 replicas 后 Controller 根据当前状态重新纠正。
6. 单个 Workload 失败不影响其他 Workload。

状态存储

1. ConfigMap 不存在时自动初始化。
2. ConfigMap 更新冲突时重试。
3. 无效状态不会被静默解释为首次发现并扩容。
4. replicaSnapshot 跨 Controller 重启保持。
5. Workload 删除后按安全保留时间清理状态。
6. 短暂 API 故障不会误删状态。
7. 状态接近 ConfigMap 容量上限时暴露指标和告警。

⸻

二十三、验收标准

最终项目必须包含：

1. 完整可编译源代码。
2. Dockerfile。
3. Kubernetes RBAC。
4. Controller Deployment Manifest。
5. 策略 ConfigMap 示例。
6. 状态 ConfigMap 示例。
7. Helm Chart 或 Kustomize。
8. README。
9. 配置字段说明。
10. 架构说明。
11. 单元测试。
12. 集成测试。
13. Prometheus Metrics。
14. 健康检查。
15. Leader Election。
16. 结构化日志。
17. Deployment 和 StatefulSet 示例。
18. 本地开发与测试命令。
19. 构建和发布命令。
20. 故障排查文档。

请先输出：

1. 整体架构设计。
2. 核心数据结构。
3. Reconcile 流程。
4. 策略 Schema。
5. 状态存储设计。
6. 项目目录结构。

然后再逐文件生成完整代码，不要只提供伪代码。