package config_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/policy"
	"gitlab.glb.osl-nucleus.com/devops/kube-workload-lifecycle-manager/internal/state"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

func loadConfigMap(t *testing.T, path string) corev1.ConfigMap {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var configMap corev1.ConfigMap
	if err := yaml.Unmarshal(data, &configMap); err != nil {
		t.Fatal(err)
	}
	return configMap
}

func TestPolicySamplePassesStrictDecoder(t *testing.T) {
	configMap := loadConfigMap(t, "base/policy-configmap.yaml")
	payload, ok := configMap.Data["policies.yaml"]
	if !ok {
		t.Fatal("policy ConfigMap is missing policies.yaml")
	}
	if _, err := policy.Decode([]byte(payload)); err != nil {
		t.Fatalf("decode policy sample: %v", err)
	}
}

func TestInitialStateSamplePassesStrictDecoder(t *testing.T) {
	configMap := loadConfigMap(t, "base/state-configmap.yaml")
	payload, ok := configMap.Data[state.ConfigMapDataKey]
	if !ok {
		t.Fatalf("state ConfigMap is missing %s", state.ConfigMapDataKey)
	}
	if !json.Valid([]byte(payload)) {
		t.Fatal("state sample is not JSON")
	}
	if _, err := state.DecodeDocument([]byte(payload)); err != nil {
		t.Fatalf("decode state sample: %v", err)
	}
}

func loadStrict[T any](t *testing.T, path string) T {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value T
	if err := yaml.UnmarshalStrict(data, &value); err != nil {
		t.Fatalf("strictly decode %s: %v", path, err)
	}
	return value
}

func TestWorkloadSamplesPassStrictKubernetesDecoding(t *testing.T) {
	deployment := loadStrict[appsv1.Deployment](t, "samples/deployment.yaml")
	if deployment.Kind != "Deployment" || len(deployment.Spec.Template.Spec.Containers) == 0 {
		t.Fatalf("invalid Deployment sample: %#v", deployment)
	}

	data, err := os.ReadFile("samples/statefulset.yaml")
	if err != nil {
		t.Fatal(err)
	}
	documents := strings.Split(string(data), "\n---\n")
	if len(documents) != 2 {
		t.Fatalf("StatefulSet sample has %d YAML documents, want Service and StatefulSet", len(documents))
	}
	var service corev1.Service
	if err := yaml.UnmarshalStrict([]byte(documents[0]), &service); err != nil {
		t.Fatalf("strictly decode sample Service: %v", err)
	}
	var statefulSet appsv1.StatefulSet
	if err := yaml.UnmarshalStrict([]byte(documents[1]), &statefulSet); err != nil {
		t.Fatalf("strictly decode sample StatefulSet: %v", err)
	}
	if service.Kind != "Service" || statefulSet.Kind != "StatefulSet" || len(statefulSet.Spec.Template.Spec.Containers) == 0 {
		t.Fatalf("invalid StatefulSet sample objects: service=%#v statefulSet=%#v", service, statefulSet)
	}
}

func TestDockerfileUsesRegistryIndependentScratchRuntime(t *testing.T) {
	data, err := os.ReadFile("../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := string(data)
	if strings.Contains(strings.ToLower(dockerfile), "gcr.io") {
		t.Fatal("Dockerfile must not reference gcr.io")
	}

	finalStageAt := strings.LastIndex(dockerfile, "FROM scratch\n")
	if finalStageAt < 0 {
		t.Fatal("Dockerfile final runtime must use FROM scratch")
	}
	finalStage := dockerfile[finalStageAt:]
	for _, required := range []string{
		"COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt",
		"COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo",
		"COPY --from=build /out/controller /controller",
		"USER 65532:65532",
		"EXPOSE 8080",
		"ENTRYPOINT [\"/controller\"]",
	} {
		if !strings.Contains(finalStage, required) {
			t.Errorf("Dockerfile scratch runtime is missing %q", required)
		}
	}
	if !strings.Contains(dockerfile, "CGO_ENABLED=0") {
		t.Error("Dockerfile must compile a static controller binary with CGO_ENABLED=0")
	}
}

func TestDeploymentAndRBACEnforceReleaseSecurity(t *testing.T) {
	deployment := loadStrict[appsv1.Deployment](t, "base/deployment.yaml")
	podSecurity := deployment.Spec.Template.Spec.SecurityContext
	container := deployment.Spec.Template.Spec.Containers[0]
	security := container.SecurityContext
	if podSecurity == nil || podSecurity.RunAsNonRoot == nil || !*podSecurity.RunAsNonRoot ||
		podSecurity.RunAsUser == nil || *podSecurity.RunAsUser != 65532 ||
		security == nil || security.ReadOnlyRootFilesystem == nil || !*security.ReadOnlyRootFilesystem ||
		security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation ||
		security.Capabilities == nil || len(security.Capabilities.Drop) != 1 || security.Capabilities.Drop[0] != corev1.Capability("ALL") {
		t.Fatalf("controller security context is not non-root/read-only/least-privilege: pod=%#v container=%#v", podSecurity, security)
	}

	role := loadStrict[rbacv1.ClusterRole](t, "base/cluster-role.yaml")
	for _, rule := range role.Rules {
		for _, resource := range rule.Resources {
			if resource == "pods" || resource == "persistentvolumeclaims" {
				t.Fatalf("ClusterRole grants access to prohibited resource %s", resource)
			}
			if resource == "deployments" || resource == "statefulsets" {
				for _, verb := range rule.Verbs {
					if verb != "get" && verb != "list" && verb != "watch" {
						t.Fatalf("ClusterRole grants workload-body mutation: %s %s", verb, resource)
					}
				}
			}
			if resource == "deployments/scale" || resource == "statefulsets/scale" {
				for _, verb := range rule.Verbs {
					if verb != "get" && verb != "update" {
						t.Fatalf("ClusterRole grants unexpected scale verb: %s %s", verb, resource)
					}
				}
			}
		}
	}
}
