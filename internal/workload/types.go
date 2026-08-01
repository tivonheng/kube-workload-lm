package workload

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
}

func (w Workload) Key() string {
	return string(w.Kind) + "/" + w.Namespace + "/" + w.Name
}
