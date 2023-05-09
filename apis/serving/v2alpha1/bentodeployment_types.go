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

package v2alpha1

import (
	autoscalingv2beta2 "k8s.io/api/autoscaling/v2beta2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/bentoml/yatai-deployment/apis/serving/common"
)

const (
	BentoDeploymentConditionTypeAvailable         = "Available"
	BentoDeploymentConditionTypeBentoFound        = "BentoFound"
	BentoDeploymentConditionTypeBentoRequestFound = "BentoRequestFound"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type ExtraPodMetadata struct {
	Annotations map[string]string `json:"annotations,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

type ExtraPodSpec struct {
	SchedulerName             string                            `json:"schedulerName,omitempty"`
	NodeSelector              map[string]string                 `json:"nodeSelector,omitempty"`
	Affinity                  *corev1.Affinity                  `json:"affinity,omitempty"`
	Tolerations               []corev1.Toleration               `json:"tolerations,omitempty"`
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
	Containers                []corev1.Container                `json:"containers,omitempty"`
	ServiceAccountName        string                            `json:"serviceAccountName,omitempty"`
}

type Autoscaling struct {
	MinReplicas int32                                               `json:"minReplicas"`
	MaxReplicas int32                                               `json:"maxReplicas"`
	Metrics     []autoscalingv2beta2.MetricSpec                     `json:"metrics,omitempty"`
	Behavior    *autoscalingv2beta2.HorizontalPodAutoscalerBehavior `json:"behavior,omitempty"`
}

type BentoDeploymentRunnerSpec struct {
	Name        string            `json:"name,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Resources   *common.Resources `json:"resources,omitempty"`
	Autoscaling *Autoscaling      `json:"autoscaling,omitempty"`
	Envs        []corev1.EnvVar   `json:"envs,omitempty"`

	// +optional
	ExtraPodMetadata *ExtraPodMetadata `json:"extraPodMetadata,omitempty"`
	// +optional
	ExtraPodSpec *ExtraPodSpec `json:"extraPodSpec,omitempty"`
}

type BentoDeploymentIngressTLSSpec struct {
	SecretName string `json:"secretName,omitempty"`
}

type BentoDeploymentIngressSpec struct {
	Enabled     bool                           `json:"enabled,omitempty"`
	Annotations map[string]string              `json:"annotations,omitempty"`
	Labels      map[string]string              `json:"labels,omitempty"`
	TLS         *BentoDeploymentIngressTLSSpec `json:"tls,omitempty"`
}

type MonitorExporterMountSpec struct {
	Path                string `json:"path,omitempty"`
	ReadOnly            bool   `json:"readOnly,omitempty"`
	corev1.VolumeSource `json:",inline"`
}

type MonitorExporterSpec struct {
	Enabled          bool                       `json:"enabled,omitempty"`
	Output           string                     `json:"output,omitempty"`
	Options          map[string]string          `json:"options,omitempty"`
	StructureOptions []corev1.EnvVar            `json:"structureOptions,omitempty"`
	Mounts           []MonitorExporterMountSpec `json:"mounts,omitempty"`
}

// BentoDeploymentSpec defines the desired state of BentoDeployment
type BentoDeploymentSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	Annotations map[string]string `json:"annotations,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`

	Bento string `json:"bento"`

	Resources   *common.Resources `json:"resources,omitempty"`
	Autoscaling *Autoscaling      `json:"autoscaling,omitempty"`
	Envs        []corev1.EnvVar   `json:"envs,omitempty"`

	Runners []BentoDeploymentRunnerSpec `json:"runners,omitempty"`

	Ingress BentoDeploymentIngressSpec `json:"ingress,omitempty"`

	MonitorExporter *MonitorExporterSpec `json:"monitorExporter,omitempty"`

	// +optional
	ExtraPodMetadata *ExtraPodMetadata `json:"extraPodMetadata,omitempty"`
	// +optional
	ExtraPodSpec *ExtraPodSpec `json:"extraPodSpec,omitempty"`
}

// BentoDeploymentStatus defines the observed state of BentoDeployment
type BentoDeploymentStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	Conditions []metav1.Condition `json:"conditions"`

	PodSelector map[string]string `json:"podSelector,omitempty"`
}

//+genclient
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:storageversion
//+kubebuilder:printcolumn:name="Bento",type="string",JSONPath=".spec.bento",description="Bento"
//+kubebuilder:printcolumn:name="Available",type="string",JSONPath=".status.conditions[?(@.type=='Available')].status",description="Available"
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
