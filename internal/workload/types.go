package workload

import "time"

type Kind string

const (
	KindDeployment  Kind = "Deployment"
	KindStatefulSet Kind = "StatefulSet"
)

type Container struct {
	Name  string `json:"container"`
	Image string `json:"image"`
}

type Workload struct {
	Kind            Kind
	Namespace       string
	Name            string
	UID             string
	Labels          map[string]string
	Containers      []Container
	CurrentReplicas int32
	// CreatedAt is the workload's Kubernetes metadata.creationTimestamp. It is
	// the authoritative moment the workload object came into existence and,
	// unlike the controller's FirstSeenAt, is unaffected by controller
	// restarts. A zero value means the creation time is unknown.
	CreatedAt time.Time
}

func (w Workload) Key() string {
	return string(w.Kind) + "/" + w.Namespace + "/" + w.Name
}
