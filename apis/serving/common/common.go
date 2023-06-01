// +k8s:deepcopy-gen=package
package common

type ResourceItem struct {
	CPU    string            `json:"cpu,omitempty"`
	Memory string            `json:"memory,omitempty"`
	GPU    string            `json:"gpu,omitempty"`
	Custom map[string]string `json:"custom,omitempty"`
}

type Resources struct {
	Requests *ResourceItem `json:"requests,omitempty"`
	Limits   *ResourceItem `json:"limits,omitempty"`
}

type DeploymentTargetHPAConf struct {
	CPU         *int32  `json:"cpu,omitempty"`
	GPU         *int32  `json:"gpu,omitempty"`
	Memory      *string `json:"memory,omitempty"`
	QPS         *int64  `json:"qps,omitempty"`
	MinReplicas *int32  `json:"min_replicas,omitempty"`
	MaxReplicas *int32  `json:"max_replicas,omitempty"`
}

type LabelItemSchema struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
