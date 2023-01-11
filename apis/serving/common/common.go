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
