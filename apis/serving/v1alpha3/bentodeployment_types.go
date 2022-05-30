/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha3

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/bentoml/yatai-schemas/modelschemas"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type BentoDeploymentRunnerSpec struct {
	Name        string                                  `json:"name,omitempty"`
	Annotations map[string]string                       `json:"annotations,omitempty"`
	Resources   *modelschemas.DeploymentTargetResources `json:"resources,omitempty"`
	Autoscaling *modelschemas.DeploymentTargetHPAConf   `json:"autoscaling,omitempty"`
	Envs        *[]modelschemas.LabelItemSchema         `json:"envs,omitempty"`
}

type BentoDeploymentIngressSpec struct {
	Enabled bool `json:"enabled,omitempty"`
}

// BentoDeploymentSpec defines the desired state of BentoDeployment
type BentoDeploymentSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	BentoTag string `json:"bento_tag"`

	Resources   *modelschemas.DeploymentTargetResources `json:"resources,omitempty"`
	Autoscaling *modelschemas.DeploymentTargetHPAConf   `json:"autoscaling,omitempty"`
	Envs        *[]modelschemas.LabelItemSchema         `json:"envs,omitempty"`

	Runners []BentoDeploymentRunnerSpec `json:"runners,omitempty"`

	Ingress BentoDeploymentIngressSpec `json:"ingress,omitempty"`
}

// BentoDeploymentStatus defines the observed state of BentoDeployment
type BentoDeploymentStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	PodSelector map[string]string `json:"podSelector,omitempty"`

	// +optional
	PrinterReady string `json:"printerReady,omitempty"`

	// Total number of non-terminated pods targeted by this deployment (their labels match the selector).
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// Total number of non-terminated pods targeted by this deployment that have the desired template spec.
	// +optional
	UpdatedReplicas int32 `json:"updatedReplicas,omitempty"`

	// readyReplicas is the number of pods targeted by this Deployment with a Ready Condition.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// Total number of available pods (ready for at least minReadySeconds) targeted by this deployment.
	// +optional
	AvailableReplicas int32 `json:"availableReplicas,omitempty"`

	// Total number of unavailable pods targeted by this deployment. This is the total number of
	// pods that are still required for the deployment to have 100% available capacity. They may
	// either be pods that are running but not yet available or pods that still have not been created.
	// +optional
	UnavailableReplicas int32 `json:"unavailableReplicas,omitempty"`
}

//+genclient
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:storageversion
//+kubebuilder:printcolumn:name="Bento",type="string",JSONPath=".spec.bento_tag",description="BentoTag"
//+kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.printerReady",description="Ready"
//+kubebuilder:printcolumn:name="MinReplicas",type="integer",JSONPath=".spec.autoscaling.min_replicas",description="MinReplicas"
//+kubebuilder:printcolumn:name="MaxReplicas",type="integer",JSONPath=".spec.autoscaling.max_replicas",description="MaxReplicas"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// BentoDeployment is the Schema for the bentodeployments API
type BentoDeployment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BentoDeploymentSpec   `json:"spec,omitempty"`
	Status BentoDeploymentStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// BentoDeploymentList contains a list of BentoDeployment
type BentoDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BentoDeployment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BentoDeployment{}, &BentoDeploymentList{})
}
