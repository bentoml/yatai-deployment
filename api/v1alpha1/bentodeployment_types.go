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

package v1alpha1

import (
	autoscalingv2beta2 "k8s.io/api/autoscaling/v2beta2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type BentoDeploymentRunnerSpec struct {
	Name        string                         `json:"name,omitempty"`
	Autoscaling BentoDeploymentAutoscalingSpec `json:"autoscaling,omitempty"`
	Resources   corev1.ResourceRequirements    `json:"resources,omitempty"`
}

type BentoDeploymentAutoscalingSpec struct {
	MinReplicas *int32                          `json:"minReplicas,omitempty"`
	MaxReplicas int32                           `json:"maxReplicas"`
	Metrics     []autoscalingv2beta2.MetricSpec `json:"metrics,omitempty"`
}

// BentoDeploymentSpec defines the desired state of BentoDeployment
type BentoDeploymentSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	BentoTag    string                         `json:"bentoTag,omitempty"`
	Autoscaling BentoDeploymentAutoscalingSpec `json:"autoscaling,omitempty"`
	Resources   corev1.ResourceRequirements    `json:"resources,omitempty"`
	Env         []corev1.EnvVar                `json:"env,omitempty"`

	Runners []BentoDeploymentRunnerSpec `json:"runners,omitempty"`
}

// BentoDeploymentStatus defines the observed state of BentoDeployment
type BentoDeploymentStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	PodSelector map[string]string `json:"podSelector,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

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
