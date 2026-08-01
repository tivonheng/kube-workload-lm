package policy

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestConfigMapWatcherReloadsAtomicallyAndKeepsLastGoodOnInvalidUpdate(t *testing.T) {
	client := kubefake.NewSimpleClientset()
	source := watch.NewRaceFreeFake()
	client.PrependWatchReactor("configmaps", func(action k8stesting.Action) (bool, watch.Interface, error) {
		watchAction := action.(k8stesting.WatchAction)
		if action.GetNamespace() != "controller-system" || watchAction.GetWatchRestrictions().Fields.String() != "metadata.name=workload-lifecycle-policies" {
			t.Fatalf("unexpected policy watch: namespace=%q fields=%q", action.GetNamespace(), watchAction.GetWatchRestrictions().Fields.String())
		}
		return true, source, nil
	})
	manager := &Manager{}
	stream, err := NewConfigMapWatcher(
		client.CoreV1().ConfigMaps("controller-system"), "workload-lifecycle-policies", DefaultConfigMapDataKey, manager,
	).Watch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Stop()

	source.Add(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "workload-lifecycle-policies"}, Data: map[string]string{DefaultConfigMapDataKey: validPolicyYAML}})
	if event := receivePolicyEvent(t, stream.ResultChan()); event.Type != EventReloaded || event.Err != nil {
		t.Fatalf("valid update event = %#v", event)
	}
	first, ok := manager.Current()
	if !ok || first.Policies[0].Name != "temporary" {
		t.Fatalf("snapshot after valid update = %#v", first)
	}

	source.Modify(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "workload-lifecycle-policies"}, Data: map[string]string{DefaultConfigMapDataKey: "invalid: true"}})
	invalid := receivePolicyEvent(t, stream.ResultChan())
	if invalid.Type != EventReloadRejected || invalid.Err == nil {
		t.Fatalf("invalid update event = %#v", invalid)
	}
	current, ok := manager.Current()
	if !ok || current.Policies[0].Name != "temporary" {
		t.Fatalf("invalid update replaced last-good snapshot: %#v", current)
	}
}

func TestConfigMapWatcherSignalsDisconnectAndStopsOnCancellation(t *testing.T) {
	client := kubefake.NewSimpleClientset()
	source := watch.NewRaceFreeFake()
	client.PrependWatchReactor("configmaps", func(k8stesting.Action) (bool, watch.Interface, error) { return true, source, nil })
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := NewConfigMapWatcher(client.CoreV1().ConfigMaps("ns"), "policies", "", &Manager{}).Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	source.Stop()
	event := receivePolicyEvent(t, stream.ResultChan())
	if event.Type != EventDisconnected || !errors.Is(event.Err, ErrPolicyWatchDisconnected) {
		t.Fatalf("disconnect event = %#v", event)
	}
	cancel()
	stream.Stop()
	select {
	case _, ok := <-stream.ResultChan():
		if ok {
			t.Fatal("watch stream remained open")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch stream leaked after cancellation")
	}
}

func receivePolicyEvent(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("policy watch closed early")
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for policy event")
		return Event{}
	}
}
