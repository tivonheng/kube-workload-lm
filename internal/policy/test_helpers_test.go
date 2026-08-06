package policy

const validPolicyYAML = `apiVersion: lifecycle.example.com/v1alpha1
kind: WorkloadLifecyclePolicySet
spec:
  policies:
    - name: temporary
      priority: 100
      target:
        kinds: [Deployment, StatefulSet]
        selector:
          matchLabels:
            lifecycle.example.com/policy: temporary
          matchExpressions:
            - key: environment
              operator: In
              values: [test, staging]
      lifecycle:
        maxAge: 72h
        revision:
          source: ContainerImages
          containers: [app]
      replicas:
        scheduledDown: 0
        expired: 0
      schedule:
        timeZone: Asia/Shanghai
        downWindows:
          - name: weekday
            startDays: [MON, TUE, WED, THU, FRI]
            start: "00:00"
            end: "08:00"
`
