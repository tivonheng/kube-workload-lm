package runtime

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

const (
	DefaultLeaseName     = "workload-lifecycle-controller"
	DefaultLeaseDuration = 15 * time.Second
	DefaultRenewDeadline = 10 * time.Second
	DefaultRetryPeriod   = 2 * time.Second
)

type KubernetesElector struct {
	config leaderelection.LeaderElectionConfig
}

func NewKubernetesElector(client kubernetes.Interface, namespace, name, identity string) (*KubernetesElector, error) {
	if client == nil || strings.TrimSpace(namespace) == "" || strings.TrimSpace(name) == "" || strings.TrimSpace(identity) == "" {
		return nil, ErrInvalidDependencies
	}
	lock, err := resourcelock.New(
		resourcelock.LeasesResourceLock,
		namespace,
		name,
		client.CoreV1(),
		client.CoordinationV1(),
		resourcelock.ResourceLockConfig{Identity: identity},
	)
	if err != nil {
		return nil, fmt.Errorf("create Lease resource lock: %w", err)
	}
	return &KubernetesElector{config: leaderelection.LeaderElectionConfig{
		Lock: lock, LeaseDuration: DefaultLeaseDuration, RenewDeadline: DefaultRenewDeadline,
		RetryPeriod: DefaultRetryPeriod, ReleaseOnCancel: true, Name: name,
	}}, nil
}

func (elector *KubernetesElector) Run(ctx context.Context, callbacks LeaderCallbacks) error {
	config := elector.config
	config.Callbacks = leaderelection.LeaderCallbacks{
		OnStartedLeading: callbacks.OnStartedLeading,
		OnStoppedLeading: callbacks.OnStoppedLeading,
		OnNewLeader:      callbacks.OnNewLeader,
	}
	leaderElector, err := leaderelection.NewLeaderElector(config)
	if err != nil {
		return fmt.Errorf("configure leader election: %w", err)
	}
	leaderElector.Run(ctx)
	if ctx.Err() != nil {
		return nil
	}
	return ErrLeadershipLost
}

func NewLeaderIdentity() (string, error) {
	hostname := strings.TrimSpace(os.Getenv("POD_NAME"))
	if hostname == "" {
		var err error
		hostname, err = os.Hostname()
		if err != nil {
			return "", fmt.Errorf("determine leader identity hostname: %w", err)
		}
	}
	randomID, err := uuid.NewUUID()
	if err != nil {
		return "", fmt.Errorf("generate leader identity UUID: %w", err)
	}
	return fmt.Sprintf("%s_%s", hostname, randomID), nil
}
