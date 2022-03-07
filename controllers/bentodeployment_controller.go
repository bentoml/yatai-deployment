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

package controllers

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2beta2 "k8s.io/api/autoscaling/v2beta2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"

	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/banzaicloud/k8s-objectmatcher/patch"
	"github.com/huandu/xstrings"
	"github.com/pkg/errors"

	"github.com/bentoml/yatai-schemas/modelschemas"
	"github.com/bentoml/yatai-schemas/schemasv1"

	servingv1alpha1 "github.com/bentoml/yatai-deployment-operator/api/serving/v1alpha1"
	"github.com/bentoml/yatai-deployment-operator/common/consts"
	"github.com/bentoml/yatai-deployment-operator/common/utils"
	yataiclient "github.com/bentoml/yatai-deployment-operator/yatai-client"
)

// BentoDeploymentReconciler reconciles a BentoDeployment object
type BentoDeploymentReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

//+kubebuilder:rbac:groups=serving.yatai.ai,resources=bentodeployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=serving.yatai.ai,resources=bentodeployments/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=serving.yatai.ai,resources=bentodeployments/finalizers,verbs=update

//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingressclasses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the BentoDeployment object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.10.0/pkg/reconcile
func (r *BentoDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	logs := log.FromContext(ctx)

	bentoDeployment := &servingv1alpha1.BentoDeployment{}
	err = r.Get(ctx, req.NamespacedName, bentoDeployment)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			// Object not found, return.  Created objects are automatically garbage collected.
			// For additional cleanup logic use finalizers.
			logs.Info("BentoDeployment resource not found. Ignoring since object must be deleted.")
			err = nil
			return
		}
		// Error reading the object - requeue the request.
		logs.Error(err, "Failed to get BentoDeployment.")
		return
	}

	yataiEndpoint := os.Getenv(consts.EnvYataiEndpoint)
	yataiApiToken := os.Getenv(consts.EnvYataiApiToken)
	yataiClient := yataiclient.NewYataiClient(yataiEndpoint, yataiApiToken)

	var bentoCache *schemasv1.BentoFullSchema
	getBento := func() (*schemasv1.BentoFullSchema, error) {
		if bentoCache != nil {
			return bentoCache, nil
		}
		bentoRepositoryName, _, bentoVersion := xstrings.Partition(bentoDeployment.Spec.BentoTag, ":")
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "GetBento", "Fetching Bento %s:%s", bentoRepositoryName, bentoVersion)
		bento_, err := yataiClient.GetBento(ctx, bentoRepositoryName, bentoVersion)
		if err == nil {
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "GetBento", "Fetched Bento %s:%s", bentoRepositoryName, bentoVersion)
		} else {
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "GetBento", "Failed to fetch Bento %s:%s: %s", bentoRepositoryName, bentoVersion, err)
		}
		return bento_, err
	}

	bento, err := getBento()
	if err != nil {
		return
	}

	// create or update deployment
	err = r.createOrUpdateDeployment(ctx, yataiClient, bentoDeployment, bento)
	if err != nil {
		return
	}

	// create or update hpa
	err = r.createOrUpdateHPA(ctx, bentoDeployment, bento)
	if err != nil {
		return
	}

	// create or update service
	err = r.createOrUpdateService(ctx, bentoDeployment, bento)
	if err != nil {
		return
	}

	// create or update ingresses
	err = r.createOrUpdateIngresses(ctx, bentoDeployment, bento)
	if err != nil {
		return
	}

	logs.Info("Finished reconciling.")
	r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "Update", "Updated")
	return
}

func (r *BentoDeploymentReconciler) createOrUpdateDeployment(ctx context.Context, yataiClient *yataiclient.YataiClient, bentoDeployment *servingv1alpha1.BentoDeployment, bento *schemasv1.BentoFullSchema) (err error) {
	logs := log.FromContext(ctx)

	deployment, err := r.generateDeployment(ctx, yataiClient, bentoDeployment, bento)
	if err != nil {
		return
	}

	deploymentLogKeysAndValues := []interface{}{"namespace", deployment.Namespace, "name", deployment.Name}
	deploymentNamespacedName := fmt.Sprintf("%s/%s", deployment.Namespace, deployment.Name)

	r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "GetDeployment", "Getting Deployment %s", deploymentNamespacedName)

	oldDeployment := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: deployment.Name, Namespace: deployment.Namespace}, oldDeployment)
	oldDeploymentIsNotFound := k8serrors.IsNotFound(err)
	if err != nil && !oldDeploymentIsNotFound {
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "GetDeployment", "Failed to get Deployment %s: %s", deploymentNamespacedName, err)
		logs.Error(err, "Failed to get Deployment.", deploymentLogKeysAndValues...)
		return
	}

	if oldDeploymentIsNotFound {
		logs.Info("Deployment not found. Creating a new one.", deploymentLogKeysAndValues...)

		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "CreateDeployment", "Creating a new Deployment %s", deploymentNamespacedName)
		err = r.Create(ctx, deployment)
		if err != nil {
			logs.Error(err, "Failed to create Deployment.", deploymentLogKeysAndValues...)
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "CreateDeployment", "Failed to create Deployment %s: %s", deploymentNamespacedName, err)
			return
		}
		logs.Info("Deployment created.", deploymentLogKeysAndValues...)
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "CreateDeployment", "Created Deployment %s", deploymentNamespacedName)
	} else {
		logs.Info("Deployment found.", deploymentLogKeysAndValues...)

		status := r.generateStatus(bentoDeployment, oldDeployment)

		if !reflect.DeepEqual(status, bentoDeployment.Status) {
			bentoDeployment.Status = status
			err = r.Status().Update(ctx, bentoDeployment)
			if err != nil {
				logs.Error(err, "Failed to update BentoDeployment status.")
				return
			}
		}

		var patchResult *patch.PatchResult
		patchResult, err = patch.DefaultPatchMaker.Calculate(oldDeployment, deployment)
		if err != nil {
			logs.Error(err, "Failed to calculate patch.", deploymentLogKeysAndValues...)
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "CalculatePatch", "Failed to calculate patch for Deployment %s: %s", deploymentNamespacedName, err)
			return
		}

		if !patchResult.IsEmpty() {
			logs.Info("Deployment spec is different. Updating Deployment.", deploymentLogKeysAndValues...)

			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateDeployment", "Updating Deployment %s", deploymentNamespacedName)
			err = r.Update(ctx, deployment)
			if err != nil {
				logs.Error(err, "Failed to update Deployment.", deploymentLogKeysAndValues...)
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "UpdateDeployment", "Failed to update Deployment %s: %s", deploymentNamespacedName, err)
				return
			}
			logs.Info("Deployment updated.", deploymentLogKeysAndValues...)
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateDeployment", "Updated Deployment %s", deploymentNamespacedName)
		} else {
			logs.Info("Deployment spec is the same. Skipping update.", deploymentLogKeysAndValues...)
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateDeployment", "Skipping update Deployment %s", deploymentNamespacedName)
		}
	}

	return
}

func (r *BentoDeploymentReconciler) createOrUpdateHPA(ctx context.Context, bentoDeployment *servingv1alpha1.BentoDeployment, bento *schemasv1.BentoFullSchema) (err error) {
	logs := log.FromContext(ctx)

	hpa, err := r.generateHPA(bentoDeployment, bento)
	if err != nil {
		return
	}

	hpaLogKeysAndValues := []interface{}{"namespace", hpa.Namespace, "name", hpa.Name}
	hpaNamespacedName := fmt.Sprintf("%s/%s", hpa.Namespace, hpa.Name)

	r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "GetHPA", "Getting HPA %s", hpaNamespacedName)

	oldHPA := &autoscalingv2beta2.HorizontalPodAutoscaler{}
	err = r.Get(ctx, types.NamespacedName{Name: hpa.Name, Namespace: hpa.Namespace}, oldHPA)
	oldHPAIsNotFound := k8serrors.IsNotFound(err)
	if err != nil && !oldHPAIsNotFound {
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "GetHPA", "Failed to get HPA %s: %s", hpaNamespacedName, err)
		logs.Error(err, "Failed to get HPA.", hpaLogKeysAndValues...)
		return
	}

	if oldHPAIsNotFound {
		logs.Info("HPA not found. Creating a new one.", hpaLogKeysAndValues...)

		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "CreateHPA", "Creating a new HPA %s", hpaNamespacedName)
		err = r.Create(ctx, hpa)
		if err != nil {
			logs.Error(err, "Failed to create HPA.", hpaLogKeysAndValues...)
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "CreateHPA", "Failed to create HPA %s: %s", hpaNamespacedName, err)
			return
		}
		logs.Info("HPA created.", hpaLogKeysAndValues...)
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "CreateHPA", "Created HPA %s", hpaNamespacedName)
	} else {
		logs.Info("HPA found.", hpaLogKeysAndValues...)

		var patchResult *patch.PatchResult
		patchResult, err = patch.DefaultPatchMaker.Calculate(oldHPA, hpa)
		if err != nil {
			logs.Error(err, "Failed to calculate patch.", hpaLogKeysAndValues...)
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "CalculatePatch", "Failed to calculate patch for HPA %s: %s", hpaNamespacedName, err)
			return
		}

		if !patchResult.IsEmpty() {
			logs.Info("HPA spec is different. Updating HPA.", hpaLogKeysAndValues...)

			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateHPA", "Updating HPA %s", hpaNamespacedName)
			err = r.Update(ctx, hpa)
			if err != nil {
				logs.Error(err, "Failed to update HPA.", hpaLogKeysAndValues...)
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "UpdateHPA", "Failed to update HPA %s: %s", hpaNamespacedName, err)
				return
			}
			logs.Info("HPA updated.", hpaLogKeysAndValues...)
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateHPA", "Updated HPA %s", hpaNamespacedName)
		} else {
			logs.Info("HPA spec is the same. Skipping update.", hpaLogKeysAndValues...)
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateHPA", "Skipping update HPA %s", hpaNamespacedName)
		}
	}

	return
}

func (r *BentoDeploymentReconciler) createOrUpdateService(ctx context.Context, bentoDeployment *servingv1alpha1.BentoDeployment, bento *schemasv1.BentoFullSchema) (err error) {
	logs := log.FromContext(ctx)

	service, err := r.generateService(bentoDeployment, bento)
	if err != nil {
		return
	}

	serviceLogKeysAndValues := []interface{}{"namespace", service.Namespace, "name", service.Name}
	serviceNamespacedName := fmt.Sprintf("%s/%s", service.Namespace, service.Name)

	r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "GetService", "Getting Service %s", serviceNamespacedName)

	oldService := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: service.Name, Namespace: service.Namespace}, oldService)
	oldServiceIsNotFound := k8serrors.IsNotFound(err)
	if err != nil && !oldServiceIsNotFound {
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "GetService", "Failed to get Service %s: %s", serviceNamespacedName, err)
		logs.Error(err, "Failed to get Service.", serviceLogKeysAndValues...)
		return
	}

	if oldServiceIsNotFound {
		logs.Info("Service not found. Creating a new one.", serviceLogKeysAndValues...)

		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "CreateService", "Creating a new Service %s", serviceNamespacedName)
		err = r.Create(ctx, service)
		if err != nil {
			logs.Error(err, "Failed to create Service.", serviceLogKeysAndValues...)
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "CreateService", "Failed to create Service %s: %s", serviceNamespacedName, err)
			return
		}
		logs.Info("Service created.", serviceLogKeysAndValues...)
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "CreateService", "Created Service %s", serviceNamespacedName)
	} else {
		logs.Info("Service found.", serviceLogKeysAndValues...)

		var patchResult *patch.PatchResult
		patchResult, err = patch.DefaultPatchMaker.Calculate(oldService, service)
		if err != nil {
			logs.Error(err, "Failed to calculate patch.", serviceLogKeysAndValues...)
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "CalculatePatch", "Failed to calculate patch for Service %s: %s", serviceNamespacedName, err)
			return
		}

		if !patchResult.IsEmpty() {
			logs.Info("Service spec is different. Updating Service.", serviceLogKeysAndValues...)

			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateService", "Updating Service %s", serviceNamespacedName)
			err = r.Update(ctx, service)
			if err != nil {
				logs.Error(err, "Failed to update Service.", serviceLogKeysAndValues...)
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "UpdateService", "Failed to update Service %s: %s", serviceNamespacedName, err)
				return
			}
			logs.Info("Service updated.", serviceLogKeysAndValues...)
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateService", "Updated Service %s", serviceNamespacedName)
		} else {
			logs.Info("Service spec is the same. Skipping update.", serviceLogKeysAndValues...)
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateService", "Skipping update Service %s", serviceNamespacedName)
		}
	}

	return
}

func (r *BentoDeploymentReconciler) createOrUpdateIngresses(ctx context.Context, bentoDeployment *servingv1alpha1.BentoDeployment, bento *schemasv1.BentoFullSchema) (err error) {
	logs := log.FromContext(ctx)

	ingresses, err := r.generateIngresses(ctx, bentoDeployment, bento)
	if err != nil {
		return
	}

	for _, ingress := range ingresses {
		ingressLogKeysAndValues := []interface{}{"namespace", ingress.Namespace, "name", ingress.Name}
		ingressNamespacedName := fmt.Sprintf("%s/%s", ingress.Namespace, ingress.Name)

		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "GetIngress", "Getting Ingress %s", ingressNamespacedName)

		oldIngress := &networkingv1.Ingress{}
		err = r.Get(ctx, types.NamespacedName{Name: ingress.Name, Namespace: ingress.Namespace}, oldIngress)
		oldIngressIsNotFound := k8serrors.IsNotFound(err)
		if err != nil && !oldIngressIsNotFound {
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "GetIngress", "Failed to get Ingress %s: %s", ingressNamespacedName, err)
			logs.Error(err, "Failed to get Ingress.", ingressLogKeysAndValues...)
			return
		}

		if oldIngressIsNotFound {
			logs.Info("Ingress not found. Creating a new one.", ingressLogKeysAndValues...)

			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "CreateIngress", "Creating a new Ingress %s", ingressNamespacedName)
			err = r.Create(ctx, ingress)
			if err != nil {
				logs.Error(err, "Failed to create Ingress.", ingressLogKeysAndValues...)
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "CreateIngress", "Failed to create Ingress %s: %s", ingressNamespacedName, err)
				return
			}
			logs.Info("Ingress created.", ingressLogKeysAndValues...)
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "CreateIngress", "Created Ingress %s", ingressNamespacedName)
		} else {
			logs.Info("Ingress found.", ingressLogKeysAndValues...)

			var patchResult *patch.PatchResult
			patchResult, err = patch.DefaultPatchMaker.Calculate(oldIngress, ingress)
			if err != nil {
				logs.Error(err, "Failed to calculate patch.", ingressLogKeysAndValues...)
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "CalculatePatch", "Failed to calculate patch for Ingress %s: %s", ingressNamespacedName, err)
				return
			}

			if !patchResult.IsEmpty() {
				logs.Info("Ingress spec is different. Updating Ingress.", ingressLogKeysAndValues...)

				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateIngress", "Updating Ingress %s", ingressNamespacedName)
				err = r.Update(ctx, ingress)
				if err != nil {
					logs.Error(err, "Failed to update Ingress.", ingressLogKeysAndValues...)
					r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "UpdateIngress", "Failed to update Ingress %s: %s", ingressNamespacedName, err)
					return
				}
				logs.Info("Ingress updated.", ingressLogKeysAndValues...)
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateIngress", "Updated Ingress %s", ingressNamespacedName)
			} else {
				logs.Info("Ingress spec is the same. Skipping update.", ingressLogKeysAndValues...)
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateIngress", "Skipping update Ingress %s", ingressNamespacedName)
			}
		}
	}

	return
}

func (r *BentoDeploymentReconciler) generateStatus(bentoDeployment *servingv1alpha1.BentoDeployment, deployment *appsv1.Deployment) servingv1alpha1.BentoDeploymentStatus {
	labels := r.getKubeLabels(bentoDeployment)
	status := servingv1alpha1.BentoDeploymentStatus{
		PodSelector:         labels,
		Replicas:            deployment.Status.Replicas,
		ReadyReplicas:       deployment.Status.ReadyReplicas,
		UpdatedReplicas:     deployment.Status.UpdatedReplicas,
		AvailableReplicas:   deployment.Status.AvailableReplicas,
		UnavailableReplicas: deployment.Status.UnavailableReplicas,
	}
	return status
}

func (r *BentoDeploymentReconciler) getKubeName(bentoDeployment *servingv1alpha1.BentoDeployment) string {
	return bentoDeployment.Name
}

func (r *BentoDeploymentReconciler) getKubeLabels(bentoDeployment *servingv1alpha1.BentoDeployment) map[string]string {
	labels := map[string]string{
		consts.KubeLabelYataiDeployment: bentoDeployment.Name,
		consts.KubeLabelCreator:         consts.KubeCreator,
	}
	return labels
}

func (r *BentoDeploymentReconciler) getKubeAnnotations(bento *schemasv1.BentoFullSchema) map[string]string {
	annotations := map[string]string{
		consts.KubeAnnotationBentoRepository: bento.Repository.Name,
		consts.KubeAnnotationBentoVersion:    bento.Version,
	}
	return annotations
}

func (r *BentoDeploymentReconciler) generateDeployment(ctx context.Context, yataiClient *yataiclient.YataiClient, bentoDeployment *servingv1alpha1.BentoDeployment, bento *schemasv1.BentoFullSchema) (kubeDeployment *appsv1.Deployment, err error) {
	kubeNs := bentoDeployment.Namespace

	podTemplateSpec, err := r.generatePodTemplateSpec(ctx, yataiClient, bentoDeployment, bento)
	if err != nil {
		return
	}

	labels := r.getKubeLabels(bentoDeployment)

	annotations := r.getKubeAnnotations(bento)

	kubeName := r.getKubeName(bentoDeployment)

	defaultMaxSurge := intstr.FromString("25%")
	defaultMaxUnavailable := intstr.FromString("25%")

	strategy := appsv1.DeploymentStrategy{
		Type: appsv1.RollingUpdateDeploymentStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDeployment{
			MaxSurge:       &defaultMaxSurge,
			MaxUnavailable: &defaultMaxUnavailable,
		},
	}

	replicas := bentoDeployment.Spec.Autoscaling.MinReplicas

	kubeDeployment = &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        kubeName,
			Namespace:   kubeNs,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					consts.KubeLabelYataiSelector: kubeName,
				},
			},
			Template: *podTemplateSpec,
			Strategy: strategy,
		},
	}

	err = ctrl.SetControllerReference(bentoDeployment, kubeDeployment, r.Scheme)

	return
}

func (r *BentoDeploymentReconciler) generateHPA(bentoDeployment *servingv1alpha1.BentoDeployment, bento *schemasv1.BentoFullSchema) (hpa *autoscalingv2beta2.HorizontalPodAutoscaler, err error) {
	labels := r.getKubeLabels(bentoDeployment)

	annotations := r.getKubeAnnotations(bento)

	kubeName := r.getKubeName(bentoDeployment)

	kubeNs := bentoDeployment.Namespace

	maxReplicas := bentoDeployment.Spec.Autoscaling.MaxReplicas
	if maxReplicas == 0 {
		maxReplicas = consts.HPADefaultMaxReplicas
	}

	metrics := bentoDeployment.Spec.Autoscaling.Metrics
	if len(metrics) == 0 {
		averageUtilization := int32(consts.HPACPUDefaultAverageUtilization)
		metrics = []autoscalingv2beta2.MetricSpec{
			{
				Type: autoscalingv2beta2.ResourceMetricSourceType,
				Resource: &autoscalingv2beta2.ResourceMetricSource{
					Name: corev1.ResourceCPU,
					Target: autoscalingv2beta2.MetricTarget{
						Type:               autoscalingv2beta2.UtilizationMetricType,
						AverageUtilization: &averageUtilization,
					},
				},
			},
		}
	}

	kubeHpa := &autoscalingv2beta2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:        kubeName,
			Namespace:   kubeNs,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: autoscalingv2beta2.HorizontalPodAutoscalerSpec{
			MinReplicas: bentoDeployment.Spec.Autoscaling.MinReplicas,
			MaxReplicas: maxReplicas,
			ScaleTargetRef: autoscalingv2beta2.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       kubeName,
			},
			Metrics: metrics,
		},
	}

	err = ctrl.SetControllerReference(bentoDeployment, kubeHpa, r.Scheme)

	return kubeHpa, err
}

func (r *BentoDeploymentReconciler) generatePodTemplateSpec(ctx context.Context, yataiClient *yataiclient.YataiClient, bentoDeployment *servingv1alpha1.BentoDeployment, bento *schemasv1.BentoFullSchema) (podTemplateSpec *corev1.PodTemplateSpec, err error) {
	podLabels := r.getKubeLabels(bentoDeployment)

	annotations := r.getKubeAnnotations(bento)

	kubeName := r.getKubeName(bentoDeployment)

	clusterName := os.Getenv(consts.EnvYataiClusterName)
	if clusterName == "" {
		clusterName = "default"
	}

	r.Recorder.Event(bentoDeployment, corev1.EventTypeNormal, "GetDockerRegistryConfigRef", "Fetching docker registry config ref")
	dockerRegistryRef, err := yataiClient.GetDockerRegistryRef(ctx, clusterName)
	if err != nil {
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "GetDockerRegistryConfigRef", "Failed to fetch docker registry config ref: %v", err)
		return
	}
	r.Recorder.Event(bentoDeployment, corev1.EventTypeNormal, "GetDockerRegistryConfigRef", "Successfully fetched docker registry config ref")

	secret := &corev1.Secret{}

	err = r.Get(ctx, types.NamespacedName{Name: dockerRegistryRef.Name, Namespace: dockerRegistryRef.Namespace}, secret)
	if err != nil {
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "GetDockerRegistryConfig", "Failed to get docker registry config from secret %s/%s: %v", dockerRegistryRef.Namespace, dockerRegistryRef.Name, err)
		return
	}
	r.Recorder.Event(bentoDeployment, corev1.EventTypeNormal, "GetDockerRegistryConfig", "Successfully fetched docker registry config from secret")

	dockerRegistry := modelschemas.DockerRegistrySchema{}
	err = json.Unmarshal(secret.Data[dockerRegistryRef.Key], &dockerRegistry)
	if err != nil {
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "UnmarshalDockerRegistryConfig", "Failed to unmarshal docker registry config from secret %s/%s: %v", dockerRegistryRef.Namespace, dockerRegistryRef.Name, err)
		return
	}

	majorCluster, err := yataiClient.GetMajorCluster(ctx)
	if err != nil {
		return
	}

	inCluster := clusterName == majorCluster.Name

	imageName := bento.ImageName
	if inCluster {
		imageName = bento.InClusterImageName
	}

	containerPort := consts.BentoServicePort
	envs := make([]corev1.EnvVar, 0, len(bentoDeployment.Spec.Env)+1)
	envsSeen := make(map[string]struct{})

	for _, env := range bentoDeployment.Spec.Env {
		if _, ok := envsSeen[env.Name]; ok {
			continue
		}
		if env.Name == consts.BentoServicePortEnvName {
			containerPort, err = strconv.Atoi(env.Value)
			if err != nil {
				return nil, errors.Wrapf(err, "invalid port value %s", env.Value)
			}
		}
		envsSeen[env.Name] = struct{}{}
		envs = append(envs, env)
	}

	if _, ok := envsSeen[consts.BentoServicePortEnvName]; !ok {
		envs = append(envs, corev1.EnvVar{
			Name:  consts.BentoServicePortEnvName,
			Value: fmt.Sprintf("%d", containerPort),
		})
	}

	livenessProbe := &corev1.Probe{
		InitialDelaySeconds: 5,
		TimeoutSeconds:      5,
		FailureThreshold:    6,
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/livez",
				Port: intstr.FromInt(containerPort),
			},
		},
	}

	readinessProbe := &corev1.Probe{
		InitialDelaySeconds: 5,
		TimeoutSeconds:      5,
		FailureThreshold:    6,
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/readyz",
				Port: intstr.FromInt(containerPort),
			},
		},
	}

	containers := make([]corev1.Container, 0, 1)

	vs := make([]corev1.Volume, 0)
	vms := make([]corev1.VolumeMount, 0)

	models_ := bento.Models

	args := make([]string, 0)
	imageTlsVerify := "false"
	if dockerRegistry.Secure {
		imageTlsVerify = "true"
	}

	for _, model := range models_ {
		imageName_ := model.ImageName
		if inCluster {
			imageName_ = model.InClusterImageName
		}
		modelRepository := model.Repository
		pvName := fmt.Sprintf("pv-%s-%s", strings.ToLower(strings.ReplaceAll(modelRepository.Name, "_", "-")), model.Version)
		sourcePath := fmt.Sprintf("/models/%s/%s", modelRepository.Name, model.Version)
		destDirPath := fmt.Sprintf("./models/%s", modelRepository.Name)
		destPath := filepath.Join(destDirPath, model.Version)
		args = append(args, "mkdir", "-p", destDirPath, ";", "ln", "-sf", filepath.Join(sourcePath, "model"), destPath, ";", "echo", "-n", fmt.Sprintf("'%s'", model.Version), ">", filepath.Join(destDirPath, "latest"), ";")
		v := corev1.Volume{
			Name: pvName,
			VolumeSource: corev1.VolumeSource{
				CSI: &corev1.CSIVolumeSource{
					Driver: consts.KubeCSIDriverImage,
					VolumeAttributes: map[string]string{
						"image":     imageName_,
						"tlsVerify": imageTlsVerify,
					},
				},
			},
		}
		vs = append(vs, v)
		vm := corev1.VolumeMount{
			Name:      pvName,
			MountPath: sourcePath,
		}
		vms = append(vms, vm)
	}

	args = append(args, "./env/docker/entrypoint.sh", "bentoml", "serve", ".", "--production")

	container := corev1.Container{
		Name:           kubeName,
		Image:          imageName,
		Command:        []string{"sh", "-c"},
		Args:           []string{strings.Join(args, " ")},
		LivenessProbe:  livenessProbe,
		ReadinessProbe: readinessProbe,
		Resources:      bentoDeployment.Spec.Resources,
		Env:            envs,
		TTY:            true,
		Stdin:          true,
		VolumeMounts:   vms,
	}

	containers = append(containers, container)

	podLabels[consts.KubeLabelYataiSelector] = kubeName

	podTemplateSpec = &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      podLabels,
			Annotations: annotations,
		},
		Spec: corev1.PodSpec{
			Containers: containers,
			Volumes:    vs,
		},
	}

	if dockerRegistry.Username != "" {
		podTemplateSpec.Spec.ImagePullSecrets = []corev1.LocalObjectReference{
			{
				Name: consts.KubeSecretNameRegcred,
			},
		}
	}

	return
}

func (r *BentoDeploymentReconciler) generateService(bentoDeployment *servingv1alpha1.BentoDeployment, bento *schemasv1.BentoFullSchema) (kubeService *corev1.Service, err error) {
	kubeName := r.getKubeName(bentoDeployment)

	targetPort := consts.BentoServicePort
	for _, env := range bentoDeployment.Spec.Env {
		if env.Name == consts.BentoServicePortEnvName {
			port_, err := strconv.Atoi(env.Value)
			if err != nil {
				return nil, errors.Wrapf(err, "convert port %s to int", env.Value)
			}
			targetPort = port_
			break
		}
	}

	spec := corev1.ServiceSpec{
		Selector: map[string]string{
			consts.KubeLabelYataiSelector: kubeName,
		},
		Ports: []corev1.ServicePort{
			{
				Name:       "http-default",
				Port:       consts.BentoServicePort,
				TargetPort: intstr.FromInt(targetPort),
				Protocol:   corev1.ProtocolTCP,
			},
		},
	}

	labels := r.getKubeLabels(bentoDeployment)

	annotations := r.getKubeAnnotations(bento)

	kubeNs := bentoDeployment.Namespace

	kubeService = &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        kubeName,
			Namespace:   kubeNs,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: spec,
	}

	err = ctrl.SetControllerReference(bentoDeployment, kubeService, r.Scheme)

	return
}

func (r *BentoDeploymentReconciler) getIngressIp(ctx context.Context) (string, error) {
	var ip string
	if ip == "" {
		svcName := "yatai-ingress-controller-ingress-nginx-controller"
		svc := &corev1.Service{}
		err := r.Get(ctx, types.NamespacedName{Name: svcName, Namespace: consts.KubeNamespaceYataiComponents}, svc)
		if err != nil {
			return "", errors.Wrap(err, "get ingress service")
		}
		if len(svc.Status.LoadBalancer.Ingress) == 0 {
			return "", errors.Errorf("the external ip of service %s on namespace %s is empty!", svcName, consts.KubeNamespaceYataiComponents)
		}

		ing := svc.Status.LoadBalancer.Ingress[0]

		ip = ing.IP
		if ip == "" {
			ip = ing.Hostname
		}
	}
	if ip == "" {
		return "", errors.Errorf("please specify the ingress ip or hostname")
	}
	if net.ParseIP(ip) == nil {
		addr, err := net.LookupIP(ip)
		if err != nil {
			return "", errors.Wrapf(err, "lookup ip from ingress hostname %s", ip)
		}
		if len(addr) == 0 {
			return "", errors.Errorf("cannot lookup ip from ingress hostname %s", ip)
		}
		ip = addr[0].String()
	}
	return ip, nil
}

func (r *BentoDeploymentReconciler) generateIngressHost(ctx context.Context, bentoDeployment *servingv1alpha1.BentoDeployment) (string, error) {
	return r.generateDefaultHostname(ctx, bentoDeployment)
}

func (r *BentoDeploymentReconciler) generateDefaultHostname(ctx context.Context, bentoDeployment *servingv1alpha1.BentoDeployment) (string, error) {
	ip, err := r.getIngressIp(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-yatai-%s.apps.yatai.dev", bentoDeployment.Name, strings.ReplaceAll(ip, ".", "-")), nil
}

func (r *BentoDeploymentReconciler) generateIngresses(ctx context.Context, bentoDeployment *servingv1alpha1.BentoDeployment, bento *schemasv1.BentoFullSchema) (ingresses []*networkingv1.Ingress, err error) {
	kubeName := r.getKubeName(bentoDeployment)

	internalHost, err := r.generateIngressHost(ctx, bentoDeployment)
	if err != nil {
		return
	}

	annotations := r.getKubeAnnotations(bento)

	tag := fmt.Sprintf("%s:%s", bento.Repository.Name, bento.Version)

	annotations["nginx.ingress.kubernetes.io/configuration-snippet"] = fmt.Sprintf(`
more_set_headers "X-Powered-By: Yatai";
more_set_headers "X-Yatai-Bento: %s";
`, tag)

	annotations["nginx.ingress.kubernetes.io/ssl-redirect"] = "false"

	labels := r.getKubeLabels(bentoDeployment)

	pathType := networkingv1.PathTypeImplementationSpecific

	kubeNs := bentoDeployment.Namespace

	interIng := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        kubeName,
			Namespace:   kubeNs,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: utils.StringPtr(consts.KubeIngressClassName),
			Rules: []networkingv1.IngressRule{
				{
					Host: internalHost,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pathType,
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: kubeName,
											Port: networkingv1.ServiceBackendPort{
												Number: consts.BentoServicePort,
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	err = ctrl.SetControllerReference(bentoDeployment, interIng, r.Scheme)

	ings := []*networkingv1.Ingress{interIng}

	return ings, err
}

// SetupWithManager sets up the controller with the Manager.
func (r *BentoDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	pred := predicate.GenerationChangedPredicate{}
	return ctrl.NewControllerManagedBy(mgr).
		For(&servingv1alpha1.BentoDeployment{}).
		Owns(&appsv1.Deployment{}).
		Owns(&autoscalingv2beta2.HorizontalPodAutoscaler{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.Ingress{}).
		WithEventFilter(pred).
		Complete(r)
}
