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
	"context"
	// nolint: gosec
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	pep440version "github.com/aquasecurity/go-pep440-version"
	"github.com/banzaicloud/k8s-objectmatcher/patch"
	"github.com/huandu/xstrings"
	"github.com/jinzhu/copier"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	autoscalingv2beta2 "k8s.io/api/autoscaling/v2beta2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	k8sversion "k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/bentoml/yatai-schemas/modelschemas"
	"github.com/bentoml/yatai-schemas/schemasv1"

	commonconfig "github.com/bentoml/yatai-common/config"
	commonconsts "github.com/bentoml/yatai-common/consts"
	"github.com/bentoml/yatai-common/system"
	commonutils "github.com/bentoml/yatai-common/utils"

	resourcesv1alpha1 "github.com/bentoml/yatai-image-builder/apis/resources/v1alpha1"

	servingcommon "github.com/bentoml/yatai-deployment/apis/serving/common"
	servingconversion "github.com/bentoml/yatai-deployment/apis/serving/conversion"
	servingv2alpha1 "github.com/bentoml/yatai-deployment/apis/serving/v2alpha1"
	"github.com/bentoml/yatai-deployment/utils"
	"github.com/bentoml/yatai-deployment/version"
	yataiclient "github.com/bentoml/yatai-deployment/yatai-client"
)

const (
	DefaultClusterName                                        = "default"
	DefaultServiceAccountName                                 = "default"
	KubeValueNameSharedMemory                                 = "shared-memory"
	KubeAnnotationDeploymentStrategy                          = "yatai.ai/deployment-strategy"
	KubeAnnotationYataiEnableStealingTrafficDebugMode         = "yatai.ai/enable-stealing-traffic-debug-mode"
	KubeAnnotationYataiEnableDebugMode                        = "yatai.ai/enable-debug-mode"
	KubeAnnotationYataiEnableDebugPodReceiveProductionTraffic = "yatai.ai/enable-debug-pod-receive-production-traffic"
	KubeAnnotationYataiProxySidecarResourcesLimitsCPU         = "yatai.ai/proxy-sidecar-resources-limits-cpu"
	KubeAnnotationYataiProxySidecarResourcesLimitsMemory      = "yatai.ai/proxy-sidecar-resources-limits-memory"
	KubeAnnotationYataiProxySidecarResourcesRequestsCPU       = "yatai.ai/proxy-sidecar-resources-requests-cpu"
	KubeAnnotationYataiProxySidecarResourcesRequestsMemory    = "yatai.ai/proxy-sidecar-resources-requests-memory"
	DeploymentTargetTypeProduction                            = "production"
	DeploymentTargetTypeDebug                                 = "debug"
	ContainerPortNameHTTPProxy                                = "http-proxy"
	ServicePortNameHTTPNonProxy                               = "http-non-proxy"
	HeaderNameDebug                                           = "X-Yatai-Debug"

	legacyHPAMajorVersion = "1"
	legacyHPAMinorVersion = "23"
)

var ServicePortHTTPNonProxy = commonconsts.BentoServicePort + 1

// BentoDeploymentReconciler reconciles a BentoDeployment object
type BentoDeploymentReconciler struct {
	client.Client
	ServerVersion *k8sversion.Info
	Scheme        *runtime.Scheme
	Recorder      record.EventRecorder
}

//+kubebuilder:rbac:groups=serving.yatai.ai,resources=bentodeployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=serving.yatai.ai,resources=bentodeployments/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=serving.yatai.ai,resources=bentodeployments/finalizers,verbs=update

//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingressclasses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

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

	bentoDeployment := &servingv2alpha1.BentoDeployment{}
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

	logs = logs.WithValues("bentoDeployment", bentoDeployment.Name, "namespace", bentoDeployment.Namespace)

	if bentoDeployment.Status.Conditions == nil || len(bentoDeployment.Status.Conditions) == 0 {
		logs.Info("Starting to reconcile BentoDeployment")
		logs.Info("Initializing BentoDeployment status")
		r.Recorder.Event(bentoDeployment, corev1.EventTypeNormal, "Reconciling", "Starting to reconcile BentoDeployment")
		bentoDeployment, err = r.setStatusConditions(ctx, req,
			metav1.Condition{
				Type:    servingv2alpha1.BentoDeploymentConditionTypeAvailable,
				Status:  metav1.ConditionUnknown,
				Reason:  "Reconciling",
				Message: "Starting to reconcile BentoDeployment",
			},
			metav1.Condition{
				Type:    servingv2alpha1.BentoDeploymentConditionTypeBentoFound,
				Status:  metav1.ConditionUnknown,
				Reason:  "Reconciling",
				Message: "Starting to reconcile BentoDeployment",
			},
		)
		if err != nil {
			return
		}
	}

	defer func() {
		if err == nil {
			return
		}
		logs.Error(err, "Failed to reconcile BentoDeployment.")
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "ReconcileError", "Failed to reconcile BentoDeployment: %v", err)
		_, err = r.setStatusConditions(ctx, req,
			metav1.Condition{
				Type:    servingv2alpha1.BentoDeploymentConditionTypeAvailable,
				Status:  metav1.ConditionFalse,
				Reason:  "Reconciling",
				Message: fmt.Sprintf("Failed to reconcile BentoDeployment: %v", err),
			},
		)
		if err != nil {
			return
		}
	}()

	yataiClient, clusterName, err := getYataiClient(ctx)
	if err != nil {
		err = errors.Wrap(err, "get yatai client")
		return
	}

	bentoFoundCondition := meta.FindStatusCondition(bentoDeployment.Status.Conditions, servingv2alpha1.BentoDeploymentConditionTypeBentoFound)
	if bentoFoundCondition != nil && bentoFoundCondition.Status == metav1.ConditionUnknown {
		logs.Info(fmt.Sprintf("Getting Bento %s", bentoDeployment.Spec.Bento))
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "GetBento", "Getting Bento %s", bentoDeployment.Spec.Bento)
	}
	bentoRequest := &resourcesv1alpha1.BentoRequest{}
	bentoCR := &resourcesv1alpha1.Bento{}
	err = r.Get(ctx, types.NamespacedName{
		Namespace: bentoDeployment.Namespace,
		Name:      bentoDeployment.Spec.Bento,
	}, bentoCR)
	bentoIsNotFound := k8serrors.IsNotFound(err)
	if err != nil && !bentoIsNotFound {
		err = errors.Wrapf(err, "get Bento %s/%s", bentoDeployment.Namespace, bentoDeployment.Spec.Bento)
		return
	}
	if bentoIsNotFound {
		if bentoFoundCondition != nil && bentoFoundCondition.Status == metav1.ConditionUnknown {
			logs.Info(fmt.Sprintf("Bento %s not found", bentoDeployment.Spec.Bento))
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "GetBento", "Bento %s not found", bentoDeployment.Spec.Bento)
		}
		bentoDeployment, err = r.setStatusConditions(ctx, req,
			metav1.Condition{
				Type:    servingv2alpha1.BentoDeploymentConditionTypeBentoFound,
				Status:  metav1.ConditionFalse,
				Reason:  "Reconciling",
				Message: "Bento not found",
			},
		)
		if err != nil {
			return
		}
		bentoRequestFoundCondition := meta.FindStatusCondition(bentoDeployment.Status.Conditions, servingv2alpha1.BentoDeploymentConditionTypeBentoRequestFound)
		if bentoRequestFoundCondition == nil || bentoRequestFoundCondition.Status != metav1.ConditionUnknown {
			bentoDeployment, err = r.setStatusConditions(ctx, req,
				metav1.Condition{
					Type:    servingv2alpha1.BentoDeploymentConditionTypeBentoRequestFound,
					Status:  metav1.ConditionUnknown,
					Reason:  "Reconciling",
					Message: "Bento not found",
				},
			)
			if err != nil {
				return
			}
		}
		if bentoRequestFoundCondition != nil && bentoRequestFoundCondition.Status == metav1.ConditionUnknown {
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "GetBentoRequest", "Getting BentoRequest %s", bentoDeployment.Spec.Bento)
		}
		err = r.Get(ctx, types.NamespacedName{
			Namespace: bentoDeployment.Namespace,
			Name:      bentoDeployment.Spec.Bento,
		}, bentoRequest)
		if err != nil {
			err = errors.Wrapf(err, "get BentoRequest %s/%s", bentoDeployment.Namespace, bentoDeployment.Spec.Bento)
			bentoDeployment, err = r.setStatusConditions(ctx, req,
				metav1.Condition{
					Type:    servingv2alpha1.BentoDeploymentConditionTypeBentoFound,
					Status:  metav1.ConditionFalse,
					Reason:  "Reconciling",
					Message: err.Error(),
				},
				metav1.Condition{
					Type:    servingv2alpha1.BentoDeploymentConditionTypeBentoRequestFound,
					Status:  metav1.ConditionFalse,
					Reason:  "Reconciling",
					Message: err.Error(),
				},
			)
			if err != nil {
				return
			}
		}
		if bentoRequestFoundCondition != nil && bentoRequestFoundCondition.Status == metav1.ConditionUnknown {
			logs.Info(fmt.Sprintf("BentoRequest %s found", bentoDeployment.Spec.Bento))
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "GetBentoRequest", "BentoRequest %s is found and waiting for its bento to be provided", bentoDeployment.Spec.Bento)
		}
		bentoDeployment, err = r.setStatusConditions(ctx, req,
			metav1.Condition{
				Type:    servingv2alpha1.BentoDeploymentConditionTypeBentoRequestFound,
				Status:  metav1.ConditionTrue,
				Reason:  "Reconciling",
				Message: "Bento not found",
			},
		)
		if err != nil {
			return
		}
		bentoAvailableCondition := meta.FindStatusCondition(bentoRequest.Status.Conditions, resourcesv1alpha1.BentoRequestConditionTypeBentoAvailable)
		if bentoAvailableCondition != nil && bentoAvailableCondition.Status == metav1.ConditionFalse {
			err = errors.Errorf("BentoRequest %s/%s is not available: %s", bentoRequest.Namespace, bentoRequest.Name, bentoAvailableCondition.Message)
			return
		}
		result = ctrl.Result{
			RequeueAfter: 10 * time.Second,
		}
		return
	} else {
		if bentoFoundCondition != nil && bentoFoundCondition.Status != metav1.ConditionTrue {
			logs.Info(fmt.Sprintf("Bento %s found", bentoDeployment.Spec.Bento))
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "GetBento", "Bento %s is found", bentoDeployment.Spec.Bento)
		}
		bentoDeployment, err = r.setStatusConditions(ctx, req,
			metav1.Condition{
				Type:    servingv2alpha1.BentoDeploymentConditionTypeBentoFound,
				Status:  metav1.ConditionTrue,
				Reason:  "Reconciling",
				Message: "Bento found",
			},
		)
		if err != nil {
			return
		}
	}

	modified := false

	if bentoCR.Spec.Runners != nil {
		for _, runner := range bentoCR.Spec.Runners {
			var modified_ bool
			// create or update runner deployment
			modified_, err = r.createOrUpdateOrDeleteDeployments(ctx, createOrUpdateOrDeleteDeploymentsOption{
				yataiClient:     yataiClient,
				bentoDeployment: bentoDeployment,
				bento:           bentoCR,
				runnerName:      &runner.Name,
				clusterName:     clusterName,
			})
			if err != nil {
				return
			}

			if modified_ {
				modified = true
			}

			// create or update hpa
			modified_, err = r.createOrUpdateHPA(ctx, bentoDeployment, bentoCR, &runner.Name)
			if err != nil {
				return
			}

			if modified_ {
				modified = true
			}

			// create or update service
			modified_, err = r.createOrUpdateOrDeleteServices(ctx, createOrUpdateOrDeleteServicesOption{
				bentoDeployment: bentoDeployment,
				bento:           bentoCR,
				runnerName:      &runner.Name,
			})
			if err != nil {
				return
			}

			if modified_ {
				modified = true
			}
		}
	}

	// create or update api-server deployment
	modified_, err := r.createOrUpdateOrDeleteDeployments(ctx, createOrUpdateOrDeleteDeploymentsOption{
		yataiClient:     yataiClient,
		bentoDeployment: bentoDeployment,
		bento:           bentoCR,
		runnerName:      nil,
		clusterName:     clusterName,
	})
	if err != nil {
		return
	}

	if modified_ {
		modified = true
	}

	// create or update api-server hpa
	modified_, err = r.createOrUpdateHPA(ctx, bentoDeployment, bentoCR, nil)
	if err != nil {
		return
	}

	if modified_ {
		modified = true
	}

	// create or update api-server service
	modified_, err = r.createOrUpdateOrDeleteServices(ctx, createOrUpdateOrDeleteServicesOption{
		bentoDeployment: bentoDeployment,
		bento:           bentoCR,
		runnerName:      nil,
	})
	if err != nil {
		return
	}

	if modified_ {
		modified = true
	}

	// create or update api-server ingresses
	modified_, err = r.createOrUpdateIngresses(ctx, createOrUpdateIngressOption{
		yataiClient:     yataiClient,
		bentoDeployment: bentoDeployment,
		bento:           bentoCR,
	})
	if err != nil {
		return
	}

	if modified_ {
		modified = true
	}

	if yataiClient != nil && clusterName != nil {
		yataiClient_ := *yataiClient
		clusterName_ := *clusterName
		bentoRepositoryName, bentoVersion := getBentoRepositoryNameAndBentoVersion(bentoCR)
		_, err = yataiClient_.GetBento(ctx, bentoRepositoryName, bentoVersion)
		bentoIsNotFound := err != nil && strings.Contains(err.Error(), "not found")
		if err != nil && !bentoIsNotFound {
			return
		}
		if bentoIsNotFound {
			bentoDeployment, err = r.setStatusConditions(ctx, req,
				metav1.Condition{
					Type:    servingv2alpha1.BentoDeploymentConditionTypeAvailable,
					Status:  metav1.ConditionTrue,
					Reason:  "Reconciling",
					Message: "Remote bento from Yatai is not found",
				},
			)
			return
		}
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "GetYataiDeployment", "Fetching yatai deployment %s", bentoDeployment.Name)
		var oldYataiDeployment *schemasv1.DeploymentSchema
		oldYataiDeployment, err = yataiClient_.GetDeployment(ctx, clusterName_, bentoDeployment.Namespace, bentoDeployment.Name)
		isNotFound := err != nil && strings.Contains(strings.ToLower(err.Error()), "not found")
		if err != nil && !isNotFound {
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "GetYataiDeployment", "Failed to fetch yatai deployment %s: %s", bentoDeployment.Name, err)
			return
		}
		err = nil

		envs := make([]*modelschemas.LabelItemSchema, 0)

		specEnvs := bentoDeployment.Spec.Envs

		for _, env := range specEnvs {
			envs = append(envs, &modelschemas.LabelItemSchema{
				Key:   env.Name,
				Value: env.Value,
			})
		}

		runners := make(map[string]modelschemas.DeploymentTargetRunnerConfig, 0)
		for _, runner := range bentoDeployment.Spec.Runners {
			envs_ := make([]*modelschemas.LabelItemSchema, 0)
			if runner.Envs != nil {
				for _, env := range runner.Envs {
					env := env
					envs_ = append(envs_, &modelschemas.LabelItemSchema{
						Key:   env.Name,
						Value: env.Value,
					})
				}
			}
			var hpaConf *modelschemas.DeploymentTargetHPAConf
			hpaConf, err = TransformToOldHPA(runner.Autoscaling)
			if err != nil {
				return
			}
			runners[runner.Name] = modelschemas.DeploymentTargetRunnerConfig{
				Resources:                              servingconversion.ConvertToDeploymentTargetResources(runner.Resources),
				HPAConf:                                hpaConf,
				Envs:                                   &envs_,
				EnableStealingTrafficDebugMode:         &[]bool{checkIfIsStealingTrafficDebugModeEnabled(runner.Annotations)}[0],
				EnableDebugMode:                        &[]bool{checkIfIsDebugModeEnabled(runner.Annotations)}[0],
				EnableDebugPodReceiveProductionTraffic: &[]bool{checkIfIsDebugPodReceiveProductionTrafficEnabled(runner.Annotations)}[0],
				BentoDeploymentOverrides: &modelschemas.RunnerBentoDeploymentOverrides{
					ExtraPodMetadata: runner.ExtraPodMetadata,
					ExtraPodSpec:     runner.ExtraPodSpec,
				},
			}
		}

		var hpaConf *modelschemas.DeploymentTargetHPAConf
		hpaConf, err = TransformToOldHPA(bentoDeployment.Spec.Autoscaling)
		if err != nil {
			return
		}
		deploymentTargets := make([]*schemasv1.CreateDeploymentTargetSchema, 0, 1)
		deploymentTarget := &schemasv1.CreateDeploymentTargetSchema{
			DeploymentTargetTypeSchema: schemasv1.DeploymentTargetTypeSchema{
				Type: modelschemas.DeploymentTargetTypeStable,
			},
			BentoRepository: bentoRepositoryName,
			Bento:           bentoVersion,
			Config: &modelschemas.DeploymentTargetConfig{
				KubeResourceUid:                        string(bentoDeployment.UID),
				KubeResourceVersion:                    bentoDeployment.ResourceVersion,
				Resources:                              servingconversion.ConvertToDeploymentTargetResources(bentoDeployment.Spec.Resources),
				HPAConf:                                hpaConf,
				Envs:                                   &envs,
				Runners:                                runners,
				EnableIngress:                          &bentoDeployment.Spec.Ingress.Enabled,
				EnableStealingTrafficDebugMode:         &[]bool{checkIfIsStealingTrafficDebugModeEnabled(bentoDeployment.Spec.Annotations)}[0],
				EnableDebugMode:                        &[]bool{checkIfIsDebugModeEnabled(bentoDeployment.Spec.Annotations)}[0],
				EnableDebugPodReceiveProductionTraffic: &[]bool{checkIfIsDebugPodReceiveProductionTrafficEnabled(bentoDeployment.Spec.Annotations)}[0],
				BentoDeploymentOverrides: &modelschemas.ApiServerBentoDeploymentOverrides{
					MonitorExporter:  bentoDeployment.Spec.MonitorExporter,
					ExtraPodMetadata: bentoDeployment.Spec.ExtraPodMetadata,
					ExtraPodSpec:     bentoDeployment.Spec.ExtraPodSpec,
				},
				BentoRequestOverrides: &modelschemas.BentoRequestOverrides{
					ImageBuildTimeout:              bentoRequest.Spec.ImageBuildTimeout,
					ImageBuilderExtraPodSpec:       bentoRequest.Spec.ImageBuilderExtraPodSpec,
					ImageBuilderExtraPodMetadata:   bentoRequest.Spec.ImageBuilderExtraPodMetadata,
					ImageBuilderExtraContainerEnv:  bentoRequest.Spec.ImageBuilderExtraContainerEnv,
					ImageBuilderContainerResources: bentoRequest.Spec.ImageBuilderContainerResources,
					DockerConfigJSONSecretName:     bentoRequest.Spec.DockerConfigJSONSecretName,
					DownloaderContainerEnvFrom:     bentoRequest.Spec.DownloaderContainerEnvFrom,
				},
			},
		}
		deploymentTargets = append(deploymentTargets, deploymentTarget)
		updateSchema := &schemasv1.UpdateDeploymentSchema{
			Targets:     deploymentTargets,
			DoNotDeploy: true,
		}
		if isNotFound {
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "CreateYataiDeployment", "Creating yatai deployment %s", bentoDeployment.Name)
			_, err = yataiClient_.CreateDeployment(ctx, clusterName_, &schemasv1.CreateDeploymentSchema{
				Name:                   bentoDeployment.Name,
				KubeNamespace:          bentoDeployment.Namespace,
				UpdateDeploymentSchema: *updateSchema,
			})
			if err != nil {
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "CreateYataiDeployment", "Failed to create yatai deployment %s: %s", bentoDeployment.Name, err)
				return
			}
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "CreateYataiDeployment", "Created yatai deployment %s", bentoDeployment.Name)
		} else {
			noChange := false
			if oldYataiDeployment != nil && oldYataiDeployment.LatestRevision != nil && len(oldYataiDeployment.LatestRevision.Targets) > 0 {
				oldYataiDeployment.LatestRevision.Targets[0].Config.KubeResourceUid = updateSchema.Targets[0].Config.KubeResourceUid
				oldYataiDeployment.LatestRevision.Targets[0].Config.KubeResourceVersion = updateSchema.Targets[0].Config.KubeResourceVersion
				noChange = reflect.DeepEqual(oldYataiDeployment.LatestRevision.Targets[0].Config, updateSchema.Targets[0].Config)
			}
			if noChange {
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateYataiDeployment", "No change in yatai deployment %s, skipping", bentoDeployment.Name)
			} else {
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateYataiDeployment", "Updating yatai deployment %s", bentoDeployment.Name)
				_, err = yataiClient_.UpdateDeployment(ctx, clusterName_, bentoDeployment.Namespace, bentoDeployment.Name, updateSchema)
				if err != nil {
					r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "UpdateYataiDeployment", "Failed to update yatai deployment %s: %s", bentoDeployment.Name, err)
					return
				}
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateYataiDeployment", "Updated yatai deployment %s", bentoDeployment.Name)
			}
		}
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "SyncYataiDeploymentStatus", "Syncing yatai deployment %s status", bentoDeployment.Name)
		_, err = yataiClient_.SyncDeploymentStatus(ctx, clusterName_, bentoDeployment.Namespace, bentoDeployment.Name)
		if err != nil {
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "SyncYataiDeploymentStatus", "Failed to sync yatai deployment %s status: %s", bentoDeployment.Name, err)
			return
		}
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "SyncYataiDeploymentStatus", "Synced yatai deployment %s status", bentoDeployment.Name)
	}

	if !modified {
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateYataiDeployment", "No changes to yatai deployment %s", bentoDeployment.Name)
	}

	logs.Info("Finished reconciling.")
	r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "Update", "All resources updated!")
	bentoDeployment, err = r.setStatusConditions(ctx, req,
		metav1.Condition{
			Type:    servingv2alpha1.BentoDeploymentConditionTypeAvailable,
			Status:  metav1.ConditionTrue,
			Reason:  "Reconciling",
			Message: "Reconciling",
		},
	)
	return
}

func (r *BentoDeploymentReconciler) setStatusConditions(ctx context.Context, req ctrl.Request, conditions ...metav1.Condition) (bentoDeployment *servingv2alpha1.BentoDeployment, err error) {
	bentoDeployment = &servingv2alpha1.BentoDeployment{}
	for i := 0; i < 3; i++ {
		if err = r.Get(ctx, req.NamespacedName, bentoDeployment); err != nil {
			err = errors.Wrap(err, "Failed to re-fetch BentoDeployment")
			return
		}
		for _, condition := range conditions {
			meta.SetStatusCondition(&bentoDeployment.Status.Conditions, condition)
		}
		if err = r.Status().Update(ctx, bentoDeployment); err != nil {
			time.Sleep(100 * time.Millisecond)
		} else {
			break
		}
	}
	if err != nil {
		err = errors.Wrap(err, "Failed to update BentoDeployment status")
		return
	}
	if err = r.Get(ctx, req.NamespacedName, bentoDeployment); err != nil {
		err = errors.Wrap(err, "Failed to re-fetch BentoDeployment")
		return
	}
	return
}

func getYataiClient(ctx context.Context) (yataiClient **yataiclient.YataiClient, clusterName *string, err error) {
	restConfig := config.GetConfigOrDie()
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		err = errors.Wrap(err, "create kubernetes clientset")
		return
	}

	yataiConf, err := commonconfig.GetYataiConfig(ctx, clientset, commonconsts.YataiDeploymentComponentName, false)
	isNotFound := k8serrors.IsNotFound(err)
	if err != nil && !isNotFound {
		err = errors.Wrap(err, "get yatai config")
		return
	}

	if isNotFound {
		return
	}

	yataiEndpoint := yataiConf.Endpoint
	yataiAPIToken := yataiConf.ApiToken
	if yataiEndpoint == "" {
		return
	}

	clusterName_ := yataiConf.ClusterName
	if clusterName_ == "" {
		clusterName_ = DefaultClusterName
	}
	yataiClient_ := yataiclient.NewYataiClient(yataiEndpoint, fmt.Sprintf("%s:%s:%s", commonconsts.YataiDeploymentComponentName, clusterName_, yataiAPIToken))
	yataiClient = &yataiClient_
	clusterName = &clusterName_
	return
}

type createOrUpdateOrDeleteDeploymentsOption struct {
	yataiClient     **yataiclient.YataiClient
	bentoDeployment *servingv2alpha1.BentoDeployment
	bento           *resourcesv1alpha1.Bento
	clusterName     *string
	runnerName      *string
}

func (r *BentoDeploymentReconciler) createOrUpdateOrDeleteDeployments(ctx context.Context, opt createOrUpdateOrDeleteDeploymentsOption) (modified bool, err error) {
	resourceAnnotations := getResourceAnnotations(opt.bentoDeployment, opt.runnerName)
	isStealingTrafficDebugModeEnabled := checkIfIsStealingTrafficDebugModeEnabled(resourceAnnotations)
	containsStealingTrafficDebugModeEnabled := checkIfContainsStealingTrafficDebugModeEnabled(opt.bentoDeployment)
	modified, err = r.createOrUpdateDeployment(ctx, createOrUpdateDeploymentOption{
		createOrUpdateOrDeleteDeploymentsOption: opt,
		isStealingTrafficDebugModeEnabled:       false,
		containsStealingTrafficDebugModeEnabled: containsStealingTrafficDebugModeEnabled,
	})
	if err != nil {
		err = errors.Wrap(err, "create or update deployment")
		return
	}
	if (opt.runnerName == nil && containsStealingTrafficDebugModeEnabled) || (opt.runnerName != nil && isStealingTrafficDebugModeEnabled) {
		modified, err = r.createOrUpdateDeployment(ctx, createOrUpdateDeploymentOption{
			createOrUpdateOrDeleteDeploymentsOption: opt,
			isStealingTrafficDebugModeEnabled:       true,
			containsStealingTrafficDebugModeEnabled: containsStealingTrafficDebugModeEnabled,
		})
		if err != nil {
			err = errors.Wrap(err, "create or update deployment")
			return
		}
	} else {
		debugDeploymentName := r.getKubeName(opt.bentoDeployment, opt.bento, opt.runnerName, true)
		debugDeployment := &appsv1.Deployment{}
		err = r.Get(ctx, types.NamespacedName{Name: debugDeploymentName, Namespace: opt.bentoDeployment.Namespace}, debugDeployment)
		isNotFound := k8serrors.IsNotFound(err)
		if err != nil && !isNotFound {
			err = errors.Wrap(err, "get deployment")
			return
		}
		err = nil
		if !isNotFound {
			err = r.Delete(ctx, debugDeployment)
			if err != nil {
				err = errors.Wrap(err, "delete deployment")
				return
			}
			modified = true
		}
	}
	return
}

type createOrUpdateDeploymentOption struct {
	createOrUpdateOrDeleteDeploymentsOption
	isStealingTrafficDebugModeEnabled       bool
	containsStealingTrafficDebugModeEnabled bool
}

func (r *BentoDeploymentReconciler) createOrUpdateDeployment(ctx context.Context, opt createOrUpdateDeploymentOption) (modified bool, err error) {
	logs := log.FromContext(ctx)

	deployment, err := r.generateDeployment(ctx, generateDeploymentOption{
		bentoDeployment:                         opt.bentoDeployment,
		bento:                                   opt.bento,
		yataiClient:                             opt.yataiClient,
		clusterName:                             opt.clusterName,
		runnerName:                              opt.runnerName,
		isStealingTrafficDebugModeEnabled:       opt.isStealingTrafficDebugModeEnabled,
		containsStealingTrafficDebugModeEnabled: opt.containsStealingTrafficDebugModeEnabled,
	})
	if err != nil {
		return
	}

	logs = logs.WithValues("namespace", deployment.Namespace, "deploymentName", deployment.Name)

	deploymentNamespacedName := fmt.Sprintf("%s/%s", deployment.Namespace, deployment.Name)

	r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "GetDeployment", "Getting Deployment %s", deploymentNamespacedName)

	oldDeployment := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: deployment.Name, Namespace: deployment.Namespace}, oldDeployment)
	oldDeploymentIsNotFound := k8serrors.IsNotFound(err)
	if err != nil && !oldDeploymentIsNotFound {
		r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "GetDeployment", "Failed to get Deployment %s: %s", deploymentNamespacedName, err)
		logs.Error(err, "Failed to get Deployment.")
		return
	}

	if oldDeploymentIsNotFound {
		logs.Info("Deployment not found. Creating a new one.")

		err = errors.Wrapf(patch.DefaultAnnotator.SetLastAppliedAnnotation(deployment), "set last applied annotation for deployment %s", deployment.Name)
		if err != nil {
			logs.Error(err, "Failed to set last applied annotation.")
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "SetLastAppliedAnnotation", "Failed to set last applied annotation for Deployment %s: %s", deploymentNamespacedName, err)
			return
		}

		r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "CreateDeployment", "Creating a new Deployment %s", deploymentNamespacedName)
		err = r.Create(ctx, deployment)
		if err != nil {
			logs.Error(err, "Failed to create Deployment.")
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "CreateDeployment", "Failed to create Deployment %s: %s", deploymentNamespacedName, err)
			return
		}
		logs.Info("Deployment created.")
		r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "CreateDeployment", "Created Deployment %s", deploymentNamespacedName)
		modified = true
	} else {
		logs.Info("Deployment found.")

		var patchResult *patch.PatchResult
		patchResult, err = patch.DefaultPatchMaker.Calculate(oldDeployment, deployment)
		if err != nil {
			logs.Error(err, "Failed to calculate patch.")
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "CalculatePatch", "Failed to calculate patch for Deployment %s: %s", deploymentNamespacedName, err)
			return
		}

		if !patchResult.IsEmpty() {
			logs.Info("Deployment spec is different. Updating Deployment.")

			err = errors.Wrapf(patch.DefaultAnnotator.SetLastAppliedAnnotation(deployment), "set last applied annotation for deployment %s", deployment.Name)
			if err != nil {
				logs.Error(err, "Failed to set last applied annotation.")
				r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "SetLastAppliedAnnotation", "Failed to set last applied annotation for Deployment %s: %s", deploymentNamespacedName, err)
				return
			}

			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "UpdateDeployment", "Updating Deployment %s", deploymentNamespacedName)
			err = r.Update(ctx, deployment)
			if err != nil {
				logs.Error(err, "Failed to update Deployment.")
				r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "UpdateDeployment", "Failed to update Deployment %s: %s", deploymentNamespacedName, err)
				return
			}
			logs.Info("Deployment updated.")
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "UpdateDeployment", "Updated Deployment %s", deploymentNamespacedName)
			modified = true
		} else {
			logs.Info("Deployment spec is the same. Skipping update.")
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "UpdateDeployment", "Skipping update Deployment %s", deploymentNamespacedName)
		}
	}

	return
}

func (r *BentoDeploymentReconciler) createOrUpdateHPA(ctx context.Context, bentoDeployment *servingv2alpha1.BentoDeployment, bento *resourcesv1alpha1.Bento, runnerName *string) (modified bool, err error) {
	logs := log.FromContext(ctx)

	hpa, err := r.generateHPA(bentoDeployment, bento, runnerName)
	if err != nil {
		return
	}
	logs = logs.WithValues("namespace", hpa.Namespace, "hpaName", hpa.Name)
	hpaNamespacedName := fmt.Sprintf("%s/%s", hpa.Namespace, hpa.Name)

	legacyHPA, err := r.handleLegacyHPA(hpa)
	if err != nil {
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "HandleHPA", "Failed to convert HPA version %s: %s", hpaNamespacedName, err)
		logs.Error(err, "Failed to convert HPA.")
		return
	}

	r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "GetHPA", "Getting HPA %s", hpaNamespacedName)

	oldHPA, err := r.getHPA(ctx, hpa)
	oldHPAIsNotFound := k8serrors.IsNotFound(err)
	if err != nil && !oldHPAIsNotFound {
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "GetHPA", "Failed to get HPA %s: %s", hpaNamespacedName, err)
		logs.Error(err, "Failed to get HPA.")
		return
	}

	if oldHPAIsNotFound {
		logs.Info("HPA not found. Creating a new one.")

		err = errors.Wrapf(patch.DefaultAnnotator.SetLastAppliedAnnotation(legacyHPA), "set last applied annotation for hpa %s", hpa.Name)
		if err != nil {
			logs.Error(err, "Failed to set last applied annotation.")
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "SetLastAppliedAnnotation", "Failed to set last applied annotation for HPA %s: %s", hpaNamespacedName, err)
			return
		}

		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "CreateHPA", "Creating a new HPA %s", hpaNamespacedName)
		err = r.Create(ctx, legacyHPA)
		if err != nil {
			logs.Error(err, "Failed to create HPA.")
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "CreateHPA", "Failed to create HPA %s: %s", hpaNamespacedName, err)
			return
		}
		logs.Info("HPA created.")
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "CreateHPA", "Created HPA %s", hpaNamespacedName)
		modified = true
	} else {
		logs.Info("HPA found.")

		var patchResult *patch.PatchResult
		patchResult, err = patch.DefaultPatchMaker.Calculate(oldHPA, legacyHPA)
		if err != nil {
			logs.Error(err, "Failed to calculate patch.")
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "CalculatePatch", "Failed to calculate patch for HPA %s: %s", hpaNamespacedName, err)
			return
		}

		if !patchResult.IsEmpty() {
			logs.Info(fmt.Sprintf("HPA spec is different. Updating HPA. The patch result is: %s", patchResult.String()))

			err = errors.Wrapf(patch.DefaultAnnotator.SetLastAppliedAnnotation(legacyHPA), "set last applied annotation for hpa %s", hpa.Name)
			if err != nil {
				logs.Error(err, "Failed to set last applied annotation.")
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "SetLastAppliedAnnotation", "Failed to set last applied annotation for HPA %s: %s", hpaNamespacedName, err)
				return
			}

			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateHPA", "Updating HPA %s", hpaNamespacedName)
			err = r.Update(ctx, legacyHPA)
			if err != nil {
				logs.Error(err, "Failed to update HPA.")
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "UpdateHPA", "Failed to update HPA %s: %s", hpaNamespacedName, err)
				return
			}
			logs.Info("HPA updated.")
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateHPA", "Updated HPA %s", hpaNamespacedName)
			modified = true
		} else {
			logs.Info("HPA spec is the same. Skipping update.")
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateHPA", "Skipping update HPA %s", hpaNamespacedName)
		}
	}

	return
}

func getResourceAnnotations(bentoDeployment *servingv2alpha1.BentoDeployment, runnerName *string) map[string]string {
	var resourceAnnotations map[string]string
	if runnerName != nil {
		for _, runner := range bentoDeployment.Spec.Runners {
			if runner.Name == *runnerName {
				resourceAnnotations = runner.Annotations
				break
			}
		}
	} else {
		resourceAnnotations = bentoDeployment.Spec.Annotations
	}

	if resourceAnnotations == nil {
		resourceAnnotations = map[string]string{}
	}

	return resourceAnnotations
}

func checkIfIsDebugModeEnabled(annotations map[string]string) bool {
	if annotations == nil {
		return false
	}

	return annotations[KubeAnnotationYataiEnableDebugMode] == commonconsts.KubeLabelValueTrue
}

func checkIfIsStealingTrafficDebugModeEnabled(annotations map[string]string) bool {
	if annotations == nil {
		return false
	}

	return annotations[KubeAnnotationYataiEnableStealingTrafficDebugMode] == commonconsts.KubeLabelValueTrue
}

func checkIfIsDebugPodReceiveProductionTrafficEnabled(annotations map[string]string) bool {
	if annotations == nil {
		return false
	}

	return annotations[KubeAnnotationYataiEnableDebugPodReceiveProductionTraffic] == commonconsts.KubeLabelValueTrue
}

func checkIfContainsStealingTrafficDebugModeEnabled(bentoDeployment *servingv2alpha1.BentoDeployment) bool {
	if checkIfIsStealingTrafficDebugModeEnabled(bentoDeployment.Spec.Annotations) {
		return true
	}
	for _, runner := range bentoDeployment.Spec.Runners {
		if checkIfIsStealingTrafficDebugModeEnabled(runner.Annotations) {
			return true
		}
	}
	return false
}

type createOrUpdateOrDeleteServicesOption struct {
	bentoDeployment *servingv2alpha1.BentoDeployment
	bento           *resourcesv1alpha1.Bento
	runnerName      *string
}

func (r *BentoDeploymentReconciler) createOrUpdateOrDeleteServices(ctx context.Context, opt createOrUpdateOrDeleteServicesOption) (modified bool, err error) {
	resourceAnnotations := getResourceAnnotations(opt.bentoDeployment, opt.runnerName)
	isStealingTrafficDebugEnabled := checkIfIsStealingTrafficDebugModeEnabled(resourceAnnotations)
	isDebugPodReceiveProductionTrafficEnabled := checkIfIsDebugPodReceiveProductionTrafficEnabled(resourceAnnotations)
	containsStealingTrafficDebugModeEnabled := checkIfContainsStealingTrafficDebugModeEnabled(opt.bentoDeployment)
	modified, err = r.createOrUpdateService(ctx, createOrUpdateServiceOption{
		bentoDeployment:                         opt.bentoDeployment,
		bento:                                   opt.bento,
		runnerName:                              opt.runnerName,
		isStealingTrafficDebugModeEnabled:       false,
		isDebugPodReceiveProductionTraffic:      isDebugPodReceiveProductionTrafficEnabled,
		containsStealingTrafficDebugModeEnabled: containsStealingTrafficDebugModeEnabled,
		isGenericService:                        true,
	})
	if err != nil {
		return
	}
	if (opt.runnerName == nil && containsStealingTrafficDebugModeEnabled) || (opt.runnerName != nil && isStealingTrafficDebugEnabled) {
		var modified_ bool
		modified_, err = r.createOrUpdateService(ctx, createOrUpdateServiceOption{
			bentoDeployment:                         opt.bentoDeployment,
			bento:                                   opt.bento,
			runnerName:                              opt.runnerName,
			isStealingTrafficDebugModeEnabled:       false,
			isDebugPodReceiveProductionTraffic:      isDebugPodReceiveProductionTrafficEnabled,
			containsStealingTrafficDebugModeEnabled: containsStealingTrafficDebugModeEnabled,
			isGenericService:                        false,
		})
		if err != nil {
			return
		}
		if modified_ {
			modified = true
		}
		modified_, err = r.createOrUpdateService(ctx, createOrUpdateServiceOption{
			bentoDeployment:                         opt.bentoDeployment,
			bento:                                   opt.bento,
			runnerName:                              opt.runnerName,
			isStealingTrafficDebugModeEnabled:       true,
			isDebugPodReceiveProductionTraffic:      isDebugPodReceiveProductionTrafficEnabled,
			containsStealingTrafficDebugModeEnabled: containsStealingTrafficDebugModeEnabled,
			isGenericService:                        false,
		})
		if err != nil {
			return
		}
		if modified_ {
			modified = true
		}
	} else {
		productionServiceName := r.getServiceName(opt.bentoDeployment, opt.bento, opt.runnerName, false)
		svc := &corev1.Service{}
		err = r.Get(ctx, types.NamespacedName{Name: productionServiceName, Namespace: opt.bentoDeployment.Namespace}, svc)
		isNotFound := k8serrors.IsNotFound(err)
		if err != nil && !isNotFound {
			err = errors.Wrapf(err, "Failed to get service %s", productionServiceName)
			return
		}
		if !isNotFound {
			modified = true
			err = r.Delete(ctx, svc)
			if err != nil {
				err = errors.Wrapf(err, "Failed to delete service %s", productionServiceName)
				return
			}
		}
		debugServiceName := r.getServiceName(opt.bentoDeployment, opt.bento, opt.runnerName, true)
		svc = &corev1.Service{}
		err = r.Get(ctx, types.NamespacedName{Name: debugServiceName, Namespace: opt.bentoDeployment.Namespace}, svc)
		isNotFound = k8serrors.IsNotFound(err)
		if err != nil && !isNotFound {
			err = errors.Wrapf(err, "Failed to get service %s", debugServiceName)
			return
		}
		err = nil
		if !isNotFound {
			modified = true
			err = r.Delete(ctx, svc)
			if err != nil {
				err = errors.Wrapf(err, "Failed to delete service %s", debugServiceName)
				return
			}
		}
	}
	return
}

type createOrUpdateServiceOption struct {
	bentoDeployment                         *servingv2alpha1.BentoDeployment
	bento                                   *resourcesv1alpha1.Bento
	runnerName                              *string
	isStealingTrafficDebugModeEnabled       bool
	isDebugPodReceiveProductionTraffic      bool
	containsStealingTrafficDebugModeEnabled bool
	isGenericService                        bool
}

func (r *BentoDeploymentReconciler) createOrUpdateService(ctx context.Context, opt createOrUpdateServiceOption) (modified bool, err error) {
	logs := log.FromContext(ctx)

	// nolint: gosimple
	service, err := r.generateService(generateServiceOption{
		bentoDeployment:                         opt.bentoDeployment,
		bento:                                   opt.bento,
		runnerName:                              opt.runnerName,
		isStealingTrafficDebugModeEnabled:       opt.isStealingTrafficDebugModeEnabled,
		isDebugPodReceiveProductionTraffic:      opt.isDebugPodReceiveProductionTraffic,
		containsStealingTrafficDebugModeEnabled: opt.containsStealingTrafficDebugModeEnabled,
		isGenericService:                        opt.isGenericService,
	})
	if err != nil {
		return
	}

	logs = logs.WithValues("namespace", service.Namespace, "serviceName", service.Name, "serviceSelector", service.Spec.Selector)

	serviceNamespacedName := fmt.Sprintf("%s/%s", service.Namespace, service.Name)

	r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "GetService", "Getting Service %s", serviceNamespacedName)

	oldService := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: service.Name, Namespace: service.Namespace}, oldService)
	oldServiceIsNotFound := k8serrors.IsNotFound(err)
	if err != nil && !oldServiceIsNotFound {
		r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "GetService", "Failed to get Service %s: %s", serviceNamespacedName, err)
		logs.Error(err, "Failed to get Service.")
		return
	}

	if oldServiceIsNotFound {
		logs.Info("Service not found. Creating a new one.")

		err = errors.Wrapf(patch.DefaultAnnotator.SetLastAppliedAnnotation(service), "set last applied annotation for service %s", service.Name)
		if err != nil {
			logs.Error(err, "Failed to set last applied annotation.")
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "SetLastAppliedAnnotation", "Failed to set last applied annotation for Service %s: %s", serviceNamespacedName, err)
			return
		}

		r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "CreateService", "Creating a new Service %s", serviceNamespacedName)
		err = r.Create(ctx, service)
		if err != nil {
			logs.Error(err, "Failed to create Service.")
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "CreateService", "Failed to create Service %s: %s", serviceNamespacedName, err)
			return
		}
		logs.Info("Service created.")
		r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "CreateService", "Created Service %s", serviceNamespacedName)
		modified = true
	} else {
		logs.Info("Service found.")

		var patchResult *patch.PatchResult
		patchResult, err = patch.DefaultPatchMaker.Calculate(oldService, service)
		if err != nil {
			logs.Error(err, "Failed to calculate patch.")
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "CalculatePatch", "Failed to calculate patch for Service %s: %s", serviceNamespacedName, err)
			return
		}

		if !patchResult.IsEmpty() {
			logs.Info("Service spec is different. Updating Service.")

			err = errors.Wrapf(patch.DefaultAnnotator.SetLastAppliedAnnotation(service), "set last applied annotation for service %s", service.Name)
			if err != nil {
				logs.Error(err, "Failed to set last applied annotation.")
				r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "SetLastAppliedAnnotation", "Failed to set last applied annotation for Service %s: %s", serviceNamespacedName, err)
				return
			}

			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "UpdateService", "Updating Service %s", serviceNamespacedName)
			oldService.Annotations = service.Annotations
			oldService.Labels = service.Labels
			oldService.Spec = service.Spec
			err = r.Update(ctx, oldService)
			if err != nil {
				logs.Error(err, "Failed to update Service.")
				r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "UpdateService", "Failed to update Service %s: %s", serviceNamespacedName, err)
				return
			}
			logs.Info("Service updated.")
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "UpdateService", "Updated Service %s", serviceNamespacedName)
			modified = true
		} else {
			logs = logs.WithValues("oldServiceSelector", oldService.Spec.Selector)
			logs.Info("Service spec is the same. Skipping update.")
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "UpdateService", "Skipping update Service %s", serviceNamespacedName)
		}
	}

	return
}

type createOrUpdateIngressOption struct {
	yataiClient     **yataiclient.YataiClient
	bentoDeployment *servingv2alpha1.BentoDeployment
	bento           *resourcesv1alpha1.Bento
}

func (r *BentoDeploymentReconciler) createOrUpdateIngresses(ctx context.Context, opt createOrUpdateIngressOption) (modified bool, err error) {
	logs := log.FromContext(ctx)

	bentoDeployment := opt.bentoDeployment
	bento := opt.bento

	ingresses, err := r.generateIngresses(ctx, generateIngressesOption{
		yataiClient:     opt.yataiClient,
		bentoDeployment: bentoDeployment,
		bento:           bento,
	})
	if err != nil {
		return
	}

	for _, ingress := range ingresses {
		logs := logs.WithValues("namespace", ingress.Namespace, "ingressName", ingress.Name)
		ingressNamespacedName := fmt.Sprintf("%s/%s", ingress.Namespace, ingress.Name)

		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "GetIngress", "Getting Ingress %s", ingressNamespacedName)

		oldIngress := &networkingv1.Ingress{}
		err = r.Get(ctx, types.NamespacedName{Name: ingress.Name, Namespace: ingress.Namespace}, oldIngress)
		oldIngressIsNotFound := k8serrors.IsNotFound(err)
		if err != nil && !oldIngressIsNotFound {
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "GetIngress", "Failed to get Ingress %s: %s", ingressNamespacedName, err)
			logs.Error(err, "Failed to get Ingress.")
			return
		}
		err = nil

		if oldIngressIsNotFound {
			if !bentoDeployment.Spec.Ingress.Enabled {
				logs.Info("Ingress not enabled. Skipping.")
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "GetIngress", "Skipping Ingress %s", ingressNamespacedName)
				continue
			}

			logs.Info("Ingress not found. Creating a new one.")

			err = errors.Wrapf(patch.DefaultAnnotator.SetLastAppliedAnnotation(ingress), "set last applied annotation for ingress %s", ingress.Name)
			if err != nil {
				logs.Error(err, "Failed to set last applied annotation.")
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "SetLastAppliedAnnotation", "Failed to set last applied annotation for Ingress %s: %s", ingressNamespacedName, err)
				return
			}

			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "CreateIngress", "Creating a new Ingress %s", ingressNamespacedName)
			err = r.Create(ctx, ingress)
			if err != nil {
				logs.Error(err, "Failed to create Ingress.")
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "CreateIngress", "Failed to create Ingress %s: %s", ingressNamespacedName, err)
				return
			}
			logs.Info("Ingress created.")
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "CreateIngress", "Created Ingress %s", ingressNamespacedName)
			modified = true
		} else {
			logs.Info("Ingress found.")

			if !bentoDeployment.Spec.Ingress.Enabled {
				logs.Info("Ingress not enabled. Deleting.")
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "DeleteIngress", "Deleting Ingress %s", ingressNamespacedName)
				err = r.Delete(ctx, ingress)
				if err != nil {
					logs.Error(err, "Failed to delete Ingress.")
					r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "DeleteIngress", "Failed to delete Ingress %s: %s", ingressNamespacedName, err)
					return
				}
				logs.Info("Ingress deleted.")
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "DeleteIngress", "Deleted Ingress %s", ingressNamespacedName)
				modified = true
				continue
			}

			// Keep host unchanged
			ingress.Spec.Rules[0].Host = oldIngress.Spec.Rules[0].Host

			var patchResult *patch.PatchResult
			patchResult, err = patch.DefaultPatchMaker.Calculate(oldIngress, ingress)
			if err != nil {
				logs.Error(err, "Failed to calculate patch.")
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "CalculatePatch", "Failed to calculate patch for Ingress %s: %s", ingressNamespacedName, err)
				return
			}

			if !patchResult.IsEmpty() {
				logs.Info("Ingress spec is different. Updating Ingress.")

				err = errors.Wrapf(patch.DefaultAnnotator.SetLastAppliedAnnotation(ingress), "set last applied annotation for ingress %s", ingress.Name)
				if err != nil {
					logs.Error(err, "Failed to set last applied annotation.")
					r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "SetLastAppliedAnnotation", "Failed to set last applied annotation for Ingress %s: %s", ingressNamespacedName, err)
					return
				}

				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateIngress", "Updating Ingress %s", ingressNamespacedName)
				err = r.Update(ctx, ingress)
				if err != nil {
					logs.Error(err, "Failed to update Ingress.")
					r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "UpdateIngress", "Failed to update Ingress %s: %s", ingressNamespacedName, err)
					return
				}
				logs.Info("Ingress updated.")
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateIngress", "Updated Ingress %s", ingressNamespacedName)
				modified = true
			} else {
				logs.Info("Ingress spec is the same. Skipping update.")
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateIngress", "Skipping update Ingress %s", ingressNamespacedName)
			}
		}
	}

	return
}

func hash(text string) string {
	// nolint: gosec
	hasher := md5.New()
	hasher.Write([]byte(text))
	return hex.EncodeToString(hasher.Sum(nil))
}

func (r *BentoDeploymentReconciler) getRunnerServiceName(bentoDeployment *servingv2alpha1.BentoDeployment, bento *resourcesv1alpha1.Bento, runnerName string, debug bool) string {
	bentoRepositoryName, bentoVersion := getBentoRepositoryNameAndBentoVersion(bento)
	hashStr := hash(fmt.Sprintf("%s:%s-%s", bentoRepositoryName, bentoVersion, runnerName))
	var svcName string
	if debug {
		svcName = fmt.Sprintf("%s-runner-d-%s", bentoDeployment.Name, hashStr)
	} else {
		svcName = fmt.Sprintf("%s-runner-p-%s", bentoDeployment.Name, hashStr)
	}
	if len(svcName) > 63 {
		if debug {
			svcName = fmt.Sprintf("runner-d-%s", hash(fmt.Sprintf("%s-%s:%s-%s", bentoDeployment.Name, bentoRepositoryName, bentoVersion, runnerName)))
		} else {
			svcName = fmt.Sprintf("runner-p-%s", hash(fmt.Sprintf("%s-%s:%s-%s", bentoDeployment.Name, bentoRepositoryName, bentoVersion, runnerName)))
		}
	}
	return svcName
}

func (r *BentoDeploymentReconciler) getRunnerGenericServiceName(bentoDeployment *servingv2alpha1.BentoDeployment, bento *resourcesv1alpha1.Bento, runnerName string) string {
	bentoRepositoryName, bentoVersion := getBentoRepositoryNameAndBentoVersion(bento)
	hashStr := hash(fmt.Sprintf("%s:%s-%s", bentoRepositoryName, bentoVersion, runnerName))
	var svcName string
	svcName = fmt.Sprintf("%s-runner-%s", bentoDeployment.Name, hashStr)
	if len(svcName) > 63 {
		svcName = fmt.Sprintf("runner-%s", hash(fmt.Sprintf("%s-%s:%s-%s", bentoDeployment.Name, bentoRepositoryName, bentoVersion, runnerName)))
	}
	return svcName
}

func (r *BentoDeploymentReconciler) getKubeName(bentoDeployment *servingv2alpha1.BentoDeployment, bento *resourcesv1alpha1.Bento, runnerName *string, debug bool) string {
	if runnerName != nil && bento.Spec.Runners != nil {
		for idx, runner := range bento.Spec.Runners {
			if runner.Name == *runnerName {
				if debug {
					return fmt.Sprintf("%s-runner-d-%d", bentoDeployment.Name, idx)
				}
				return fmt.Sprintf("%s-runner-%d", bentoDeployment.Name, idx)
			}
		}
	}
	if debug {
		return fmt.Sprintf("%s-d", bentoDeployment.Name)
	}
	return bentoDeployment.Name
}

func (r *BentoDeploymentReconciler) getServiceName(bentoDeployment *servingv2alpha1.BentoDeployment, bento *resourcesv1alpha1.Bento, runnerName *string, debug bool) string {
	var kubeName string
	if runnerName != nil {
		kubeName = r.getRunnerServiceName(bentoDeployment, bento, *runnerName, debug)
	} else {
		if debug {
			kubeName = fmt.Sprintf("%s-d", bentoDeployment.Name)
		} else {
			kubeName = fmt.Sprintf("%s-p", bentoDeployment.Name)
		}
	}
	return kubeName
}

func (r *BentoDeploymentReconciler) getGenericServiceName(bentoDeployment *servingv2alpha1.BentoDeployment, bento *resourcesv1alpha1.Bento, runnerName *string) string {
	var kubeName string
	if runnerName != nil {
		kubeName = r.getRunnerGenericServiceName(bentoDeployment, bento, *runnerName)
	} else {
		kubeName = r.getKubeName(bentoDeployment, bento, runnerName, false)
	}
	return kubeName
}

func (r *BentoDeploymentReconciler) getKubeLabels(bentoDeployment *servingv2alpha1.BentoDeployment, bento *resourcesv1alpha1.Bento, runnerName *string) map[string]string {
	bentoRepositoryName, _, bentoVersion := xstrings.Partition(bento.Spec.Tag, ":")
	labels := map[string]string{
		commonconsts.KubeLabelYataiBentoDeployment:           bentoDeployment.Name,
		commonconsts.KubeLabelBentoRepository:                bentoRepositoryName,
		commonconsts.KubeLabelBentoVersion:                   bentoVersion,
		commonconsts.KubeLabelYataiBentoDeploymentTargetType: DeploymentTargetTypeProduction,
		commonconsts.KubeLabelCreator:                        "yatai-deployment",
	}
	if runnerName != nil {
		labels[commonconsts.KubeLabelYataiBentoDeploymentComponentType] = commonconsts.YataiBentoDeploymentComponentRunner
		labels[commonconsts.KubeLabelYataiBentoDeploymentComponentName] = *runnerName
	} else {
		labels[commonconsts.KubeLabelYataiBentoDeploymentComponentType] = commonconsts.YataiBentoDeploymentComponentApiServer
	}
	extraLabels := bentoDeployment.Spec.Labels
	if runnerName != nil {
		for _, runner := range bentoDeployment.Spec.Runners {
			if runner.Name != *runnerName {
				continue
			}
			extraLabels = runner.Labels
			break
		}
	}
	for k, v := range extraLabels {
		labels[k] = v
	}
	return labels
}

func (r *BentoDeploymentReconciler) getKubeAnnotations(bentoDeployment *servingv2alpha1.BentoDeployment, bento *resourcesv1alpha1.Bento, runnerName *string) map[string]string {
	bentoRepositoryName, bentoVersion := getBentoRepositoryNameAndBentoVersion(bento)
	annotations := map[string]string{
		commonconsts.KubeAnnotationBentoRepository: bentoRepositoryName,
		commonconsts.KubeAnnotationBentoVersion:    bentoVersion,
	}
	var extraAnnotations map[string]string
	if bentoDeployment.Spec.ExtraPodMetadata != nil {
		extraAnnotations = bentoDeployment.Spec.ExtraPodMetadata.Annotations
	} else {
		extraAnnotations = map[string]string{}
	}
	if runnerName != nil {
		for _, runner := range bentoDeployment.Spec.Runners {
			if runner.Name != *runnerName {
				continue
			}
			if runner.ExtraPodMetadata != nil {
				extraAnnotations = runner.ExtraPodMetadata.Annotations
			}
			break
		}
	}
	for k, v := range extraAnnotations {
		annotations[k] = v
	}
	return annotations
}

type generateDeploymentOption struct {
	bentoDeployment                         *servingv2alpha1.BentoDeployment
	bento                                   *resourcesv1alpha1.Bento
	yataiClient                             **yataiclient.YataiClient
	runnerName                              *string
	clusterName                             *string
	isStealingTrafficDebugModeEnabled       bool
	containsStealingTrafficDebugModeEnabled bool
}

func (r *BentoDeploymentReconciler) generateDeployment(ctx context.Context, opt generateDeploymentOption) (kubeDeployment *appsv1.Deployment, err error) {
	kubeNs := opt.bentoDeployment.Namespace

	// nolint: gosimple
	podTemplateSpec, err := r.generatePodTemplateSpec(ctx, generatePodTemplateSpecOption{
		bentoDeployment:                         opt.bentoDeployment,
		bento:                                   opt.bento,
		yataiClient:                             opt.yataiClient,
		runnerName:                              opt.runnerName,
		clusterName:                             opt.clusterName,
		isStealingTrafficDebugModeEnabled:       opt.isStealingTrafficDebugModeEnabled,
		containsStealingTrafficDebugModeEnabled: opt.containsStealingTrafficDebugModeEnabled,
	})
	if err != nil {
		return
	}

	labels := r.getKubeLabels(opt.bentoDeployment, opt.bento, opt.runnerName)

	annotations := r.getKubeAnnotations(opt.bentoDeployment, opt.bento, opt.runnerName)

	kubeName := r.getKubeName(opt.bentoDeployment, opt.bento, opt.runnerName, opt.isStealingTrafficDebugModeEnabled)

	defaultMaxSurge := intstr.FromString("25%")
	defaultMaxUnavailable := intstr.FromString("25%")

	strategy := appsv1.DeploymentStrategy{
		Type: appsv1.RollingUpdateDeploymentStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDeployment{
			MaxSurge:       &defaultMaxSurge,
			MaxUnavailable: &defaultMaxUnavailable,
		},
	}

	resourceAnnotations := getResourceAnnotations(opt.bentoDeployment, opt.runnerName)
	strategyStr := resourceAnnotations[KubeAnnotationDeploymentStrategy]
	if strategyStr != "" {
		strategyType := modelschemas.DeploymentStrategy(strategyStr)
		switch strategyType {
		case modelschemas.DeploymentStrategyRollingUpdate:
			strategy = appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxSurge:       &defaultMaxSurge,
					MaxUnavailable: &defaultMaxUnavailable,
				},
			}
		case modelschemas.DeploymentStrategyRecreate:
			strategy = appsv1.DeploymentStrategy{
				Type: appsv1.RecreateDeploymentStrategyType,
			}
		case modelschemas.DeploymentStrategyRampedSlowRollout:
			strategy = appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxSurge:       &[]intstr.IntOrString{intstr.FromInt(1)}[0],
					MaxUnavailable: &[]intstr.IntOrString{intstr.FromInt(0)}[0],
				},
			}
		case modelschemas.DeploymentStrategyBestEffortControlledRollout:
			strategy = appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxSurge:       &[]intstr.IntOrString{intstr.FromInt(0)}[0],
					MaxUnavailable: &[]intstr.IntOrString{intstr.FromString("20%")}[0],
				},
			}
		}
	}

	var replicas *int32
	if opt.isStealingTrafficDebugModeEnabled {
		replicas = &[]int32{int32(1)}[0]
	}

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
					commonconsts.KubeLabelYataiSelector: kubeName,
				},
			},
			Template: *podTemplateSpec,
			Strategy: strategy,
		},
	}

	err = ctrl.SetControllerReference(opt.bentoDeployment, kubeDeployment, r.Scheme)
	if err != nil {
		err = errors.Wrapf(err, "set deployment %s controller reference", kubeDeployment.Name)
	}

	return
}

func (r *BentoDeploymentReconciler) generateHPA(bentoDeployment *servingv2alpha1.BentoDeployment, bento *resourcesv1alpha1.Bento, runnerName *string) (*autoscalingv2beta2.HorizontalPodAutoscaler, error) {
	labels := r.getKubeLabels(bentoDeployment, bento, runnerName)

	annotations := r.getKubeAnnotations(bentoDeployment, bento, runnerName)

	kubeName := r.getKubeName(bentoDeployment, bento, runnerName, false)

	kubeNs := bentoDeployment.Namespace

	var hpaConf *servingv2alpha1.Autoscaling

	if runnerName != nil {
		for _, runner := range bentoDeployment.Spec.Runners {
			if runner.Name == *runnerName {
				hpaConf = runner.Autoscaling
				break
			}
		}
	} else {
		hpaConf = bentoDeployment.Spec.Autoscaling
	}

	if hpaConf == nil {
		hpaConf = &servingv2alpha1.Autoscaling{
			MinReplicas: 1,
			MaxReplicas: 1,
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
			MinReplicas: &hpaConf.MinReplicas,
			MaxReplicas: hpaConf.MaxReplicas,
			ScaleTargetRef: autoscalingv2beta2.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       kubeName,
			},
			Metrics: hpaConf.Metrics,
		},
	}

	if len(kubeHpa.Spec.Metrics) == 0 {
		averageUtilization := int32(commonconsts.HPACPUDefaultAverageUtilization)
		kubeHpa.Spec.Metrics = []autoscalingv2beta2.MetricSpec{
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

	err := ctrl.SetControllerReference(bentoDeployment, kubeHpa, r.Scheme)
	if err != nil {
		return nil, errors.Wrapf(err, "set hpa %s controller reference", kubeName)
	}

	return kubeHpa, err
}

func (r *BentoDeploymentReconciler) getHPA(ctx context.Context, hpa *autoscalingv2beta2.HorizontalPodAutoscaler) (client.Object, error) {
	name, ns := hpa.Name, hpa.Namespace
	if r.requireLegacyHPA() {
		obj := &autoscalingv2beta2.HorizontalPodAutoscaler{}
		err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, obj)
		if err == nil {
			obj.Status = hpa.Status
		}
		return obj, err
	}
	obj := &autoscalingv2.HorizontalPodAutoscaler{}
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, obj)
	if err == nil {
		legacyStatus := &autoscalingv2.HorizontalPodAutoscalerStatus{}
		if err := copier.Copy(legacyStatus, obj.Status); err != nil {
			return nil, err
		}
		obj.Status = *legacyStatus
	}
	return obj, err
}

func (r *BentoDeploymentReconciler) handleLegacyHPA(hpa *autoscalingv2beta2.HorizontalPodAutoscaler) (client.Object, error) {
	if r.requireLegacyHPA() {
		return hpa, nil
	}
	v2hpa := &autoscalingv2.HorizontalPodAutoscaler{}
	if err := copier.Copy(v2hpa, hpa); err != nil {
		return nil, err
	}
	return v2hpa, nil
}

func (r *BentoDeploymentReconciler) requireLegacyHPA() bool {
	if r.ServerVersion.Major >= legacyHPAMajorVersion && r.ServerVersion.Minor > legacyHPAMinorVersion {
		return false
	}
	return true
}

func getBentoRepositoryNameAndBentoVersion(bento *resourcesv1alpha1.Bento) (repositoryName string, version string) {
	repositoryName, _, version = xstrings.Partition(bento.Spec.Tag, ":")

	return
}

type generatePodTemplateSpecOption struct {
	bentoDeployment                         *servingv2alpha1.BentoDeployment
	bento                                   *resourcesv1alpha1.Bento
	yataiClient                             **yataiclient.YataiClient
	clusterName                             *string
	runnerName                              *string
	isStealingTrafficDebugModeEnabled       bool
	containsStealingTrafficDebugModeEnabled bool
}

func (r *BentoDeploymentReconciler) generatePodTemplateSpec(ctx context.Context, opt generatePodTemplateSpecOption) (podTemplateSpec *corev1.PodTemplateSpec, err error) {
	bentoRepositoryName, bentoVersion := getBentoRepositoryNameAndBentoVersion(opt.bento)
	podLabels := r.getKubeLabels(opt.bentoDeployment, opt.bento, opt.runnerName)
	if opt.runnerName != nil {
		podLabels[commonconsts.KubeLabelBentoRepository] = bentoRepositoryName
		podLabels[commonconsts.KubeLabelBentoVersion] = bentoVersion
	}
	if opt.isStealingTrafficDebugModeEnabled {
		podLabels[commonconsts.KubeLabelYataiBentoDeploymentTargetType] = DeploymentTargetTypeDebug
	}

	podAnnotations := r.getKubeAnnotations(opt.bentoDeployment, opt.bento, opt.runnerName)

	kubeName := r.getKubeName(opt.bentoDeployment, opt.bento, opt.runnerName, opt.isStealingTrafficDebugModeEnabled)

	containerPort := commonconsts.BentoServicePort
	lastPort := containerPort + 1

	monitorExporter := opt.bentoDeployment.Spec.MonitorExporter
	needMonitorContainer := monitorExporter != nil && monitorExporter.Enabled && opt.runnerName == nil

	lastPort++
	monitorExporterPort := lastPort

	var envs []corev1.EnvVar
	envsSeen := make(map[string]struct{})

	var resourceAnnotations map[string]string
	var specEnvs []corev1.EnvVar
	if opt.runnerName != nil {
		for _, runner := range opt.bentoDeployment.Spec.Runners {
			if runner.Name == *opt.runnerName {
				specEnvs = runner.Envs
				resourceAnnotations = runner.Annotations
				break
			}
		}
	} else {
		specEnvs = opt.bentoDeployment.Spec.Envs
		resourceAnnotations = opt.bentoDeployment.Spec.Annotations
	}

	if resourceAnnotations == nil {
		resourceAnnotations = make(map[string]string)
	}

	isDebugModeEnabled := checkIfIsDebugModeEnabled(resourceAnnotations)

	if specEnvs != nil {
		envs = make([]corev1.EnvVar, 0, len(specEnvs)+1)

		for _, env := range specEnvs {
			if _, ok := envsSeen[env.Name]; ok {
				continue
			}
			if env.Name == commonconsts.EnvBentoServicePort {
				// nolint: gosec
				containerPort, err = strconv.Atoi(env.Value)
				if err != nil {
					return nil, errors.Wrapf(err, "invalid port value %s", env.Value)
				}
			}
			envsSeen[env.Name] = struct{}{}
			envs = append(envs, corev1.EnvVar{
				Name:  env.Name,
				Value: env.Value,
			})
		}
	}

	defaultEnvs := []corev1.EnvVar{
		{
			Name:  commonconsts.EnvBentoServicePort,
			Value: fmt.Sprintf("%d", containerPort),
		},
		{
			Name:  commonconsts.EnvYataiDeploymentUID,
			Value: string(opt.bentoDeployment.UID),
		},
		{
			Name:  commonconsts.EnvYataiBentoDeploymentName,
			Value: opt.bentoDeployment.Name,
		},
		{
			Name:  commonconsts.EnvYataiBentoDeploymentNamespace,
			Value: opt.bentoDeployment.Namespace,
		},
	}

	if opt.yataiClient != nil {
		yataiClient := *opt.yataiClient
		var organization *schemasv1.OrganizationFullSchema
		organization, err = yataiClient.GetOrganization(ctx)
		if err != nil {
			return
		}

		var cluster *schemasv1.ClusterFullSchema
		clusterName := DefaultClusterName
		if opt.clusterName != nil {
			clusterName = *opt.clusterName
		}
		cluster, err = yataiClient.GetCluster(ctx, clusterName)
		if err != nil {
			return
		}

		var version *schemasv1.VersionSchema
		version, err = yataiClient.GetVersion(ctx)
		if err != nil {
			return
		}

		defaultEnvs = append(defaultEnvs, []corev1.EnvVar{
			{
				Name:  commonconsts.EnvYataiVersion,
				Value: fmt.Sprintf("%s-%s", version.Version, version.GitCommit),
			},
			{
				Name:  commonconsts.EnvYataiOrgUID,
				Value: organization.Uid,
			},
			{
				Name:  commonconsts.EnvYataiClusterUID,
				Value: cluster.Uid,
			},
		}...)
	}

	for _, env := range defaultEnvs {
		if _, ok := envsSeen[env.Name]; !ok {
			envs = append(envs, env)
		}
	}

	if needMonitorContainer {
		monitoringConfigTemplate := `monitoring.enabled=true
monitoring.type=otlp
monitoring.options.endpoint=http://127.0.0.1:%d
monitoring.options.insecure=true`
		var bentomlOptions string
		index := -1
		for i, env := range envs {
			if env.Name == "BENTOML_CONFIG_OPTIONS" {
				bentomlOptions = env.Value
				index = i
				break
			}
		}
		if index == -1 {
			// BENOML_CONFIG_OPTIONS not defined
			bentomlOptions = fmt.Sprintf(monitoringConfigTemplate, monitorExporterPort)
			envs = append(envs, corev1.EnvVar{
				Name:  "BENTOML_CONFIG_OPTIONS",
				Value: bentomlOptions,
			})
		} else if !strings.Contains(bentomlOptions, "monitoring") {
			// monitoring config not defined
			envs = append(envs[:index], envs[index+1:]...)
			bentomlOptions = strings.TrimSpace(bentomlOptions) // ' ' -> ''
			if bentomlOptions != "" {
				bentomlOptions += "\n"
			}
			bentomlOptions += fmt.Sprintf(monitoringConfigTemplate, monitorExporterPort)
			envs = append(envs, corev1.EnvVar{
				Name:  "BENTOML_CONFIG_OPTIONS",
				Value: bentomlOptions,
			})
		}
		// monitoring config already defined
		// do nothing
	}

	livenessProbe := &corev1.Probe{
		InitialDelaySeconds: 10,
		TimeoutSeconds:      20,
		FailureThreshold:    6,
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/livez",
				Port: intstr.FromString(commonconsts.BentoContainerPortName),
			},
		},
	}

	readinessProbe := &corev1.Probe{
		InitialDelaySeconds: 5,
		TimeoutSeconds:      5,
		FailureThreshold:    12,
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/readyz",
				Port: intstr.FromString(commonconsts.BentoContainerPortName),
			},
		},
	}

	volumes := make([]corev1.Volume, 0)
	volumeMounts := make([]corev1.VolumeMount, 0)

	args := make([]string, 0)

	isOldVersion := false
	if opt.bento.Spec.Context != nil && opt.bento.Spec.Context.BentomlVersion != "" {
		var currentVersion pep440version.Version
		currentVersion, err = pep440version.Parse(opt.bento.Spec.Context.BentomlVersion)
		if err != nil {
			err = errors.Wrapf(err, "invalid bentoml version %s", opt.bento.Spec.Context.BentomlVersion)
			return
		}
		var targetVersion pep440version.Version
		targetVersion, err = pep440version.Parse("1.0.0a7")
		if err != nil {
			err = errors.Wrapf(err, "invalid target version %s", opt.bento.Spec.Context.BentomlVersion)
			return
		}
		isOldVersion = currentVersion.LessThanOrEqual(targetVersion)
	}

	if opt.runnerName != nil {
		// python -m bentoml._internal.server.cli.runner iris_classifier:ohzovcfvvseu3lg6 iris_clf tcp://127.0.0.1:8001 --working-dir .
		if isOldVersion {
			args = append(args, "./env/docker/entrypoint.sh", "python", "-m", "bentoml._internal.server.cli.runner", ".", *opt.runnerName, fmt.Sprintf("tcp://0.0.0.0:%d", containerPort), "--working-dir", ".")
		} else {
			args = append(args, "./env/docker/entrypoint.sh", "python", "-m", "bentoml._internal.server.cli.runner", ".", "--runner-name", *opt.runnerName, "--bind", fmt.Sprintf("tcp://0.0.0.0:%d", containerPort), "--working-dir", ".")
		}
	} else {
		if len(opt.bento.Spec.Runners) > 0 {
			readinessProbeUrls := make([]string, 0)
			livenessProbeUrls := make([]string, 0)
			readinessProbeUrls = append(readinessProbeUrls, fmt.Sprintf("http://localhost:%d/readyz", containerPort))
			livenessProbeUrls = append(livenessProbeUrls, fmt.Sprintf("http://localhost:%d/healthz", containerPort))
			// python -m bentoml._internal.server.cli.api_server  iris_classifier:ohzovcfvvseu3lg6 tcp://127.0.0.1:8000 --runner-map '{"iris_clf": "tcp://127.0.0.1:8001"}' --working-dir .
			runnerMap := make(map[string]string, len(opt.bento.Spec.Runners))
			for _, runner := range opt.bento.Spec.Runners {
				isRunnerStealingTrafficDebugModeEnabled := false
				if opt.bentoDeployment.Spec.Runners != nil {
					for _, runnerResource := range opt.bentoDeployment.Spec.Runners {
						if runnerResource.Name == runner.Name {
							isRunnerStealingTrafficDebugModeEnabled = checkIfIsStealingTrafficDebugModeEnabled(runnerResource.Annotations)
							break
						}
					}
				}
				var runnerServiceName string
				if opt.isStealingTrafficDebugModeEnabled {
					runnerServiceName = r.getRunnerServiceName(opt.bentoDeployment, opt.bento, runner.Name, isRunnerStealingTrafficDebugModeEnabled)
				} else {
					runnerServiceName = r.getRunnerGenericServiceName(opt.bentoDeployment, opt.bento, runner.Name)
				}
				runnerMap[runner.Name] = fmt.Sprintf("tcp://%s:%d", runnerServiceName, commonconsts.BentoServicePort)
				readinessProbeUrls = append(readinessProbeUrls, fmt.Sprintf("http://%s:%d/readyz", runnerServiceName, commonconsts.BentoServicePort))
				livenessProbeUrls = append(livenessProbeUrls, fmt.Sprintf("http://%s:%d/healthz", runnerServiceName, commonconsts.BentoServicePort))
			}

			livenessProbePythonCommandPieces := make([]string, 0, len(opt.bento.Spec.Runners)+1)
			for _, url_ := range livenessProbeUrls {
				livenessProbePythonCommandPieces = append(livenessProbePythonCommandPieces, fmt.Sprintf("urlopen('%s')", url_))
			}

			readinessProbePythonCommandPieces := make([]string, 0, len(opt.bento.Spec.Runners)+1)
			for _, url_ := range readinessProbeUrls {
				readinessProbePythonCommandPieces = append(readinessProbePythonCommandPieces, fmt.Sprintf("urlopen('%s')", url_))
			}

			livenessProbe = &corev1.Probe{
				InitialDelaySeconds: 5,
				TimeoutSeconds:      5,
				FailureThreshold:    6,
				ProbeHandler: corev1.ProbeHandler{
					Exec: &corev1.ExecAction{
						Command: []string{
							"python3",
							"-c",
							fmt.Sprintf(`"from urllib.request import urlopen; %s"`, strings.Join(livenessProbePythonCommandPieces, "; ")),
						},
					},
				},
			}

			readinessProbe = &corev1.Probe{
				InitialDelaySeconds: 5,
				TimeoutSeconds:      5,
				FailureThreshold:    36,
				ProbeHandler: corev1.ProbeHandler{
					Exec: &corev1.ExecAction{
						Command: []string{
							"python3",
							"-c",
							fmt.Sprintf(`"from urllib.request import urlopen; %s"`, strings.Join(readinessProbePythonCommandPieces, "; ")),
						},
					},
				},
			}

			runnerMapStr, err := json.Marshal(runnerMap)
			if err != nil {
				return nil, errors.Wrap(err, "failed to marshal runner map")
			}
			if isOldVersion {
				args = append(args, "./env/docker/entrypoint.sh", "python", "-m", "bentoml._internal.server.cli.api_server", ".", fmt.Sprintf("tcp://0.0.0.0:%d", containerPort), "--runner-map", fmt.Sprintf("'%s'", string(runnerMapStr)), "--working-dir", ".")
			} else {
				args = append(args, "./env/docker/entrypoint.sh", "python", "-m", "bentoml._internal.server.cli.api_server", ".", "--bind", fmt.Sprintf("tcp://0.0.0.0:%d", containerPort), "--runner-map", fmt.Sprintf("'%s'", string(runnerMapStr)), "--working-dir", ".")
			}
		} else {
			args = append(args, "./env/docker/entrypoint.sh", "bentoml", "serve", ".", "--production")
		}
	}

	yataiResources := opt.bentoDeployment.Spec.Resources
	if opt.runnerName != nil {
		for _, runner := range opt.bentoDeployment.Spec.Runners {
			if runner.Name != *opt.runnerName {
				continue
			}
			yataiResources = runner.Resources
			break
		}
	}

	resources, err := getResourcesConfig(yataiResources)
	if err != nil {
		err = errors.Wrap(err, "failed to get resources config")
		return nil, err
	}

	sharedMemorySizeLimit := resource.MustParse("64Mi")
	memoryLimit := resources.Limits[corev1.ResourceMemory]
	if !memoryLimit.IsZero() {
		sharedMemorySizeLimit.SetMilli(memoryLimit.MilliValue() / 2)
	}

	volumes = append(volumes, corev1.Volume{
		Name: KubeValueNameSharedMemory,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{
				Medium:    corev1.StorageMediumMemory,
				SizeLimit: &sharedMemorySizeLimit,
			},
		},
	})
	volumeMounts = append(volumeMounts, corev1.VolumeMount{
		Name:      KubeValueNameSharedMemory,
		MountPath: "/dev/shm",
	})

	imageName := opt.bento.Spec.Image

	var securityContext *corev1.SecurityContext
	var mainContainerSecurityContext *corev1.SecurityContext

	enableRestrictedSecurityContext := os.Getenv("ENABLE_RESTRICTED_SECURITY_CONTEXT") == "true"
	if enableRestrictedSecurityContext {
		securityContext = &corev1.SecurityContext{
			AllowPrivilegeEscalation: pointer.BoolPtr(false),
			RunAsNonRoot:             pointer.BoolPtr(true),
			RunAsUser:                pointer.Int64Ptr(1000),
			RunAsGroup:               pointer.Int64Ptr(1000),
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeRuntimeDefault,
			},
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		}
		mainContainerSecurityContext = securityContext.DeepCopy()
		mainContainerSecurityContext.RunAsUser = pointer.Int64Ptr(1034)
	}

	containers := make([]corev1.Container, 0, 2)

	container := corev1.Container{
		Name:           "main",
		Image:          imageName,
		Command:        []string{"sh", "-c"},
		Args:           []string{strings.Join(args, " ")},
		LivenessProbe:  livenessProbe,
		ReadinessProbe: readinessProbe,
		Resources:      resources,
		Env:            envs,
		TTY:            true,
		Stdin:          true,
		VolumeMounts:   volumeMounts,
		Ports: []corev1.ContainerPort{
			{
				Protocol:      corev1.ProtocolTCP,
				Name:          commonconsts.BentoContainerPortName,
				ContainerPort: int32(containerPort), // nolint: gosec
			},
		},
		SecurityContext: mainContainerSecurityContext,
	}

	if resourceAnnotations["yatai.ai/enable-container-privileged"] == commonconsts.KubeLabelValueTrue {
		if container.SecurityContext == nil {
			container.SecurityContext = &corev1.SecurityContext{}
		}
		container.SecurityContext.Privileged = &[]bool{true}[0]
	}

	if resourceAnnotations["yatai.ai/enable-container-ptrace"] == commonconsts.KubeLabelValueTrue {
		if container.SecurityContext == nil {
			container.SecurityContext = &corev1.SecurityContext{}
		}
		container.SecurityContext.Capabilities = &corev1.Capabilities{
			Add: []corev1.Capability{"SYS_PTRACE"},
		}
	}

	if resourceAnnotations["yatai.ai/run-container-as-root"] == commonconsts.KubeLabelValueTrue {
		if container.SecurityContext == nil {
			container.SecurityContext = &corev1.SecurityContext{}
		}
		container.SecurityContext.RunAsUser = &[]int64{0}[0]
	}

	containers = append(containers, container)

	lastPort++
	metricsPort := lastPort

	containers = append(containers, corev1.Container{
		Name:  "metrics-transformer",
		Image: commonconfig.GetInternalImages().MetricsTransformer,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("10Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("100Mi"),
			},
		},
		ReadinessProbe: &corev1.Probe{
			InitialDelaySeconds: 5,
			TimeoutSeconds:      5,
			FailureThreshold:    10,
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/healthz",
					Port: intstr.FromString("metrics"),
				},
			},
		},
		LivenessProbe: &corev1.Probe{
			InitialDelaySeconds: 5,
			TimeoutSeconds:      5,
			FailureThreshold:    10,
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/healthz",
					Port: intstr.FromString("metrics"),
				},
			},
		},
		Env: []corev1.EnvVar{
			{
				Name:  "BENTOML_SERVER_HOST",
				Value: "localhost",
			},
			{
				Name:  "BENTOML_SERVER_PORT",
				Value: fmt.Sprintf("%d", containerPort),
			},
			{
				Name:  "PORT",
				Value: fmt.Sprintf("%d", metricsPort),
			},
			{
				Name:  "OLD_METRICS_PREFIX",
				Value: fmt.Sprintf("BENTOML_%s_", strings.ReplaceAll(bentoRepositoryName, "-", ":")),
			},
			{
				Name:  "NEW_METRICS_PREFIX",
				Value: "BENTOML_",
			},
		},
		Ports: []corev1.ContainerPort{
			{
				Protocol:      corev1.ProtocolTCP,
				Name:          "metrics",
				ContainerPort: int32(metricsPort),
			},
		},
		SecurityContext: securityContext,
	})

	if opt.runnerName == nil {
		lastPort++
		proxyPort := lastPort

		proxyResourcesRequestsCPUStr := resourceAnnotations[KubeAnnotationYataiProxySidecarResourcesRequestsCPU]
		if proxyResourcesRequestsCPUStr == "" {
			proxyResourcesRequestsCPUStr = "100m"
		}
		var proxyResourcesRequestsCPU resource.Quantity
		proxyResourcesRequestsCPU, err = resource.ParseQuantity(proxyResourcesRequestsCPUStr)
		if err != nil {
			err = errors.Wrapf(err, "failed to parse proxy sidecar resources requests cpu: %s", proxyResourcesRequestsCPUStr)
			return nil, err
		}
		proxyResourcesRequestsMemoryStr := resourceAnnotations[KubeAnnotationYataiProxySidecarResourcesRequestsMemory]
		if proxyResourcesRequestsMemoryStr == "" {
			proxyResourcesRequestsMemoryStr = "200Mi"
		}
		var proxyResourcesRequestsMemory resource.Quantity
		proxyResourcesRequestsMemory, err = resource.ParseQuantity(proxyResourcesRequestsMemoryStr)
		if err != nil {
			err = errors.Wrapf(err, "failed to parse proxy sidecar resources requests memory: %s", proxyResourcesRequestsMemoryStr)
			return nil, err
		}
		proxyResourcesLimitsCPUStr := resourceAnnotations[KubeAnnotationYataiProxySidecarResourcesLimitsCPU]
		if proxyResourcesLimitsCPUStr == "" {
			proxyResourcesLimitsCPUStr = "300m"
		}
		var proxyResourcesLimitsCPU resource.Quantity
		proxyResourcesLimitsCPU, err = resource.ParseQuantity(proxyResourcesLimitsCPUStr)
		if err != nil {
			err = errors.Wrapf(err, "failed to parse proxy sidecar resources limits cpu: %s", proxyResourcesLimitsCPUStr)
			return nil, err
		}
		proxyResourcesLimitsMemoryStr := resourceAnnotations[KubeAnnotationYataiProxySidecarResourcesLimitsMemory]
		if proxyResourcesLimitsMemoryStr == "" {
			proxyResourcesLimitsMemoryStr = "1000Mi"
		}
		var proxyResourcesLimitsMemory resource.Quantity
		proxyResourcesLimitsMemory, err = resource.ParseQuantity(proxyResourcesLimitsMemoryStr)
		if err != nil {
			err = errors.Wrapf(err, "failed to parse proxy sidecar resources limits memory: %s", proxyResourcesLimitsMemoryStr)
			return nil, err
		}
		var envoyConfigContent string
		if opt.isStealingTrafficDebugModeEnabled {
			productionServiceName := r.getServiceName(opt.bentoDeployment, opt.bento, nil, false)
			envoyConfigContent, err = utils.GenerateEnvoyConfigurationContent(utils.CreateEnvoyConfig{
				ListenPort:              proxyPort,
				DebugHeaderName:         HeaderNameDebug,
				DebugHeaderValue:        commonconsts.KubeLabelValueTrue,
				DebugServerAddress:      "localhost",
				DebugServerPort:         containerPort,
				ProductionServerAddress: fmt.Sprintf("%s.%s.svc.cluster.local", productionServiceName, opt.bentoDeployment.Namespace),
				ProductionServerPort:    ServicePortHTTPNonProxy,
			})
		} else {
			debugServiceName := r.getServiceName(opt.bentoDeployment, opt.bento, nil, true)
			envoyConfigContent, err = utils.GenerateEnvoyConfigurationContent(utils.CreateEnvoyConfig{
				ListenPort:              proxyPort,
				DebugHeaderName:         HeaderNameDebug,
				DebugHeaderValue:        commonconsts.KubeLabelValueTrue,
				DebugServerAddress:      fmt.Sprintf("%s.%s.svc.cluster.local", debugServiceName, opt.bentoDeployment.Namespace),
				DebugServerPort:         ServicePortHTTPNonProxy,
				ProductionServerAddress: "localhost",
				ProductionServerPort:    containerPort,
			})
		}
		if err != nil {
			err = errors.Wrapf(err, "failed to generate envoy configuration content")
			return nil, err
		}
		envoyConfigConfigMapName := fmt.Sprintf("%s-envoy-config", kubeName)
		envoyConfigConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      envoyConfigConfigMapName,
				Namespace: opt.bentoDeployment.Namespace,
			},
			Data: map[string]string{
				"envoy.yaml": envoyConfigContent,
			},
		}
		err = ctrl.SetControllerReference(opt.bentoDeployment, envoyConfigConfigMap, r.Scheme)
		if err != nil {
			err = errors.Wrapf(err, "failed to set controller reference for envoy config config map")
			return nil, err
		}
		_, err = ctrl.CreateOrUpdate(ctx, r.Client, envoyConfigConfigMap, func() error {
			envoyConfigConfigMap.Data["envoy.yaml"] = envoyConfigContent
			return nil
		})
		if err != nil {
			err = errors.Wrapf(err, "failed to create or update envoy config configmap")
			return nil, err
		}
		volumes = append(volumes, corev1.Volume{
			Name: "envoy-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: envoyConfigConfigMapName,
					},
				},
			},
		})
		proxyImage := "quay.io/bentoml/bentoml-proxy:0.0.1"
		proxyImage_ := os.Getenv("INTERNAL_IMAGES_PROXY")
		if proxyImage_ != "" {
			proxyImage = proxyImage_
		}
		containers = append(containers, corev1.Container{
			Name:  "proxy",
			Image: proxyImage,
			Command: []string{
				"envoy",
				"--config-path",
				"/etc/envoy/envoy.yaml",
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "envoy-config",
					MountPath: "/etc/envoy",
				},
			},
			Ports: []corev1.ContainerPort{
				{
					Name:          ContainerPortNameHTTPProxy,
					ContainerPort: int32(proxyPort),
					Protocol:      corev1.ProtocolTCP,
				},
			},
			ReadinessProbe: &corev1.Probe{
				InitialDelaySeconds: 5,
				TimeoutSeconds:      5,
				FailureThreshold:    10,
				ProbeHandler: corev1.ProbeHandler{
					Exec: &corev1.ExecAction{
						Command: []string{
							"sh",
							"-c",
							"curl -s localhost:9901/server_info | grep state | grep -q LIVE",
						},
					},
				},
			},
			LivenessProbe: &corev1.Probe{
				InitialDelaySeconds: 5,
				TimeoutSeconds:      5,
				FailureThreshold:    10,
				ProbeHandler: corev1.ProbeHandler{
					Exec: &corev1.ExecAction{
						Command: []string{
							"sh",
							"-c",
							"curl -s localhost:9901/server_info | grep state | grep -q LIVE",
						},
					},
				},
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    proxyResourcesRequestsCPU,
					corev1.ResourceMemory: proxyResourcesRequestsMemory,
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    proxyResourcesLimitsCPU,
					corev1.ResourceMemory: proxyResourcesLimitsMemory,
				},
			},
			SecurityContext: securityContext,
		})
	}

	if needMonitorContainer {
		lastPort++
		monitorExporterProbePort := lastPort

		monitorExporterImage := "quay.io/bentoml/bentoml-monitor-exporter:0.0.3"
		monitorExporterImage_ := os.Getenv("INTERNAL_MONITOR_EXPORTER")
		if monitorExporterImage_ != "" {
			monitorExporterImage = monitorExporterImage_
		}

		monitorOptEnvs := make([]corev1.EnvVar, 0, len(monitorExporter.Options)+len(monitorExporter.StructureOptions))
		monitorOptEnvsSeen := make(map[string]struct{})

		for _, env := range monitorExporter.StructureOptions {
			monitorOptEnvsSeen[strings.ToLower(env.Name)] = struct{}{}
			monitorOptEnvs = append(monitorOptEnvs, corev1.EnvVar{
				Name:      "FLUENTBIT_OUTPUT_OPTION_" + strings.ToUpper(env.Name),
				Value:     env.Value,
				ValueFrom: env.ValueFrom,
			})
		}

		for k, v := range monitorExporter.Options {
			if _, exists := monitorOptEnvsSeen[strings.ToLower(k)]; exists {
				continue
			}
			monitorOptEnvs = append(monitorOptEnvs, corev1.EnvVar{
				Name:  "FLUENTBIT_OUTPUT_OPTION_" + strings.ToUpper(k),
				Value: v,
			})
		}

		monitorVolumeMounts := make([]corev1.VolumeMount, 0, len(monitorExporter.Mounts))
		for idx, mount := range monitorExporter.Mounts {
			volumeName := fmt.Sprintf("monitor-exporter-%d", idx)
			volumes = append(volumes, corev1.Volume{
				Name:         volumeName,
				VolumeSource: mount.VolumeSource,
			})
			monitorVolumeMounts = append(monitorVolumeMounts, corev1.VolumeMount{
				Name:      volumeName,
				MountPath: mount.Path,
				ReadOnly:  mount.ReadOnly,
			})
		}

		containers = append(containers, corev1.Container{
			Name:         "monitor-exporter",
			Image:        monitorExporterImage,
			VolumeMounts: monitorVolumeMounts,
			Env: append([]corev1.EnvVar{
				{
					Name:  "FLUENTBIT_OTLP_PORT",
					Value: fmt.Sprint(monitorExporterPort),
				},
				{
					Name:  "FLUENTBIT_HTTP_PORT",
					Value: fmt.Sprint(monitorExporterProbePort),
				},
				{
					Name:  "FLUENTBIT_OUTPUT",
					Value: monitorExporter.Output,
				},
			}, monitorOptEnvs...),
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("24Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1000m"),
					corev1.ResourceMemory: resource.MustParse("72Mi"),
				},
			},
			ReadinessProbe: &corev1.Probe{
				InitialDelaySeconds: 5,
				TimeoutSeconds:      5,
				FailureThreshold:    10,
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: "/readyz",
						Port: intstr.FromInt(monitorExporterProbePort),
					},
				},
			},
			LivenessProbe: &corev1.Probe{
				InitialDelaySeconds: 5,
				TimeoutSeconds:      5,
				FailureThreshold:    10,
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: "/livez",
						Port: intstr.FromInt(monitorExporterProbePort),
					},
				},
			},
			SecurityContext: securityContext,
		})
	}

	debuggerImage := "quay.io/bentoml/bento-debugger:0.0.8"
	debuggerImage_ := os.Getenv("INTERNAL_IMAGES_DEBUGGER")
	if debuggerImage_ != "" {
		debuggerImage = debuggerImage_
	}

	if opt.isStealingTrafficDebugModeEnabled || isDebugModeEnabled {
		containers = append(containers, corev1.Container{
			Name:  "debugger",
			Image: debuggerImage,
			Command: []string{
				"sleep",
				"infinity",
			},
			SecurityContext: &corev1.SecurityContext{
				Capabilities: &corev1.Capabilities{
					Add: []corev1.Capability{"SYS_PTRACE"},
				},
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("100Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1000m"),
					corev1.ResourceMemory: resource.MustParse("1000Mi"),
				},
			},
			Stdin: true,
			TTY:   true,
		})
	}

	podLabels[commonconsts.KubeLabelYataiSelector] = kubeName

	podSpec := corev1.PodSpec{
		Containers: containers,
		Volumes:    volumes,
	}

	podSpec.ImagePullSecrets = opt.bento.Spec.ImagePullSecrets

	extraPodMetadata := opt.bentoDeployment.Spec.ExtraPodMetadata

	if opt.runnerName != nil {
		for _, runner := range opt.bentoDeployment.Spec.Runners {
			if runner.Name != *opt.runnerName {
				continue
			}
			extraPodMetadata = runner.ExtraPodMetadata
			break
		}
	}

	if extraPodMetadata != nil {
		for k, v := range extraPodMetadata.Annotations {
			podAnnotations[k] = v
		}

		for k, v := range extraPodMetadata.Labels {
			podLabels[k] = v
		}
	}

	extraPodSpec := opt.bentoDeployment.Spec.ExtraPodSpec

	if opt.runnerName != nil {
		for _, runner := range opt.bentoDeployment.Spec.Runners {
			if runner.Name != *opt.runnerName {
				continue
			}
			extraPodSpec = runner.ExtraPodSpec
			break
		}
	}

	if extraPodSpec != nil {
		podSpec.SchedulerName = extraPodSpec.SchedulerName
		podSpec.NodeSelector = extraPodSpec.NodeSelector
		podSpec.Affinity = extraPodSpec.Affinity
		podSpec.Tolerations = extraPodSpec.Tolerations
		podSpec.TopologySpreadConstraints = extraPodSpec.TopologySpreadConstraints
		podSpec.Containers = append(podSpec.Containers, extraPodSpec.Containers...)
		podSpec.ServiceAccountName = extraPodSpec.ServiceAccountName
	}

	if podSpec.ServiceAccountName == "" {
		serviceAccounts := &corev1.ServiceAccountList{}
		err = r.List(ctx, serviceAccounts, client.InNamespace(opt.bentoDeployment.Namespace), client.MatchingLabels{
			commonconsts.KubeLabelBentoDeploymentPod: commonconsts.KubeLabelValueTrue,
		})
		if err != nil {
			err = errors.Wrapf(err, "failed to list service accounts in namespace %s", opt.bentoDeployment.Namespace)
			return
		}
		if len(serviceAccounts.Items) > 0 {
			podSpec.ServiceAccountName = serviceAccounts.Items[0].Name
		} else {
			podSpec.ServiceAccountName = DefaultServiceAccountName
		}
	}

	if resourceAnnotations["yatai.ai/enable-host-ipc"] == commonconsts.KubeLabelValueTrue {
		podSpec.HostIPC = true
	}

	if resourceAnnotations["yatai.ai/enable-host-network"] == commonconsts.KubeLabelValueTrue {
		podSpec.HostNetwork = true
	}

	if resourceAnnotations["yatai.ai/enable-host-pid"] == commonconsts.KubeLabelValueTrue {
		podSpec.HostPID = true
	}

	if opt.isStealingTrafficDebugModeEnabled || isDebugModeEnabled {
		podSpec.ShareProcessNamespace = &[]bool{true}[0]
	}

	podTemplateSpec = &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      podLabels,
			Annotations: podAnnotations,
		},
		Spec: podSpec,
	}

	return
}

func getResourcesConfig(resources *servingcommon.Resources) (corev1.ResourceRequirements, error) {
	currentResources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("300m"),
			corev1.ResourceMemory: resource.MustParse("500Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("1Gi"),
		},
	}

	if resources == nil {
		return currentResources, nil
	}

	if resources.Limits != nil {
		if resources.Limits.CPU != "" {
			q, err := resource.ParseQuantity(resources.Limits.CPU)
			if err != nil {
				return currentResources, errors.Wrapf(err, "parse limits cpu quantity")
			}
			if currentResources.Limits == nil {
				currentResources.Limits = make(corev1.ResourceList)
			}
			currentResources.Limits[corev1.ResourceCPU] = q
		}
		if resources.Limits.Memory != "" {
			q, err := resource.ParseQuantity(resources.Limits.Memory)
			if err != nil {
				return currentResources, errors.Wrapf(err, "parse limits memory quantity")
			}
			if currentResources.Limits == nil {
				currentResources.Limits = make(corev1.ResourceList)
			}
			currentResources.Limits[corev1.ResourceMemory] = q
		}
		if resources.Limits.GPU != "" {
			q, err := resource.ParseQuantity(resources.Limits.GPU)
			if err != nil {
				return currentResources, errors.Wrapf(err, "parse limits gpu quantity")
			}
			if currentResources.Limits == nil {
				currentResources.Limits = make(corev1.ResourceList)
			}
			currentResources.Limits[commonconsts.KubeResourceGPUNvidia] = q
		}
		for k, v := range resources.Limits.Custom {
			q, err := resource.ParseQuantity(v)
			if err != nil {
				return currentResources, errors.Wrapf(err, "parse limits %s quantity", k)
			}
			if currentResources.Limits == nil {
				currentResources.Limits = make(corev1.ResourceList)
			}
			currentResources.Limits[corev1.ResourceName(k)] = q
		}
	}
	if resources.Requests != nil {
		if resources.Requests.CPU != "" {
			q, err := resource.ParseQuantity(resources.Requests.CPU)
			if err != nil {
				return currentResources, errors.Wrapf(err, "parse requests cpu quantity")
			}
			if currentResources.Requests == nil {
				currentResources.Requests = make(corev1.ResourceList)
			}
			currentResources.Requests[corev1.ResourceCPU] = q
		}
		if resources.Requests.Memory != "" {
			q, err := resource.ParseQuantity(resources.Requests.Memory)
			if err != nil {
				return currentResources, errors.Wrapf(err, "parse requests memory quantity")
			}
			if currentResources.Requests == nil {
				currentResources.Requests = make(corev1.ResourceList)
			}
			currentResources.Requests[corev1.ResourceMemory] = q
		}
		for k, v := range resources.Requests.Custom {
			q, err := resource.ParseQuantity(v)
			if err != nil {
				return currentResources, errors.Wrapf(err, "parse requests %s quantity", k)
			}
			if currentResources.Requests == nil {
				currentResources.Requests = make(corev1.ResourceList)
			}
			currentResources.Requests[corev1.ResourceName(k)] = q
		}
	}
	return currentResources, nil
}

type generateServiceOption struct {
	bentoDeployment                         *servingv2alpha1.BentoDeployment
	bento                                   *resourcesv1alpha1.Bento
	runnerName                              *string
	isStealingTrafficDebugModeEnabled       bool
	isDebugPodReceiveProductionTraffic      bool
	containsStealingTrafficDebugModeEnabled bool
	isGenericService                        bool
}

func (r *BentoDeploymentReconciler) generateService(opt generateServiceOption) (kubeService *corev1.Service, err error) {
	var kubeName string
	if opt.isGenericService {
		kubeName = r.getGenericServiceName(opt.bentoDeployment, opt.bento, opt.runnerName)
	} else {
		kubeName = r.getServiceName(opt.bentoDeployment, opt.bento, opt.runnerName, opt.isStealingTrafficDebugModeEnabled)
	}

	labels := r.getKubeLabels(opt.bentoDeployment, opt.bento, opt.runnerName)

	selector := make(map[string]string)

	for k, v := range labels {
		selector[k] = v
	}

	if opt.isStealingTrafficDebugModeEnabled {
		selector[commonconsts.KubeLabelYataiBentoDeploymentTargetType] = DeploymentTargetTypeDebug
	}

	targetPort := intstr.FromString(commonconsts.BentoContainerPortName)
	if opt.runnerName == nil {
		if opt.isGenericService {
			delete(selector, commonconsts.KubeLabelYataiBentoDeploymentTargetType)
			if opt.containsStealingTrafficDebugModeEnabled {
				targetPort = intstr.FromString(ContainerPortNameHTTPProxy)
			}
		}
	} else {
		if opt.isGenericService && opt.isDebugPodReceiveProductionTraffic {
			delete(selector, commonconsts.KubeLabelYataiBentoDeploymentTargetType)
		}
	}

	spec := corev1.ServiceSpec{
		Selector: selector,
		Ports: []corev1.ServicePort{
			{
				Name:       commonconsts.BentoServicePortName,
				Port:       commonconsts.BentoServicePort,
				TargetPort: targetPort,
				Protocol:   corev1.ProtocolTCP,
			},
			{
				Name:       ServicePortNameHTTPNonProxy,
				Port:       int32(ServicePortHTTPNonProxy),
				TargetPort: intstr.FromString(commonconsts.BentoContainerPortName),
				Protocol:   corev1.ProtocolTCP,
			},
		},
	}

	annotations := r.getKubeAnnotations(opt.bentoDeployment, opt.bento, opt.runnerName)

	kubeNs := opt.bentoDeployment.Namespace

	kubeService = &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        kubeName,
			Namespace:   kubeNs,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: spec,
	}

	err = ctrl.SetControllerReference(opt.bentoDeployment, kubeService, r.Scheme)
	if err != nil {
		err = errors.Wrapf(err, "set controller reference for service %s", kubeService.Name)
		return
	}

	return
}

func (r *BentoDeploymentReconciler) generateIngressHost(ctx context.Context, bentoDeployment *servingv2alpha1.BentoDeployment) (string, error) {
	return r.generateDefaultHostname(ctx, bentoDeployment)
}

func (r *BentoDeploymentReconciler) generateDefaultHostname(ctx context.Context, bentoDeployment *servingv2alpha1.BentoDeployment) (string, error) {
	restConfig := config.GetConfigOrDie()
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return "", errors.Wrapf(err, "create kubernetes clientset")
	}

	domainSuffix, err := system.GetDomainSuffix(ctx, clientset)
	if err != nil {
		return "", errors.Wrapf(err, "get domain suffix")
	}
	return fmt.Sprintf("%s-%s.%s", bentoDeployment.Name, bentoDeployment.Namespace, domainSuffix), nil
}

type TLSModeOpt string

const (
	TLSModeNone   TLSModeOpt = "none"
	TLSModeAuto   TLSModeOpt = "auto"
	TLSModeStatic TLSModeOpt = "static"
)

type IngressConfig struct {
	ClassName           *string
	Annotations         map[string]string
	Path                string
	PathType            networkingv1.PathType
	TLSMode             TLSModeOpt
	StaticTLSSecretName string
}

func GetIngressConfig(ctx context.Context, cliset *kubernetes.Clientset) (ingressConfig *IngressConfig, err error) {
	configMap, err := system.GetNetworkConfigConfigMap(ctx, cliset)
	if err != nil {
		err = errors.Wrapf(err, "failed to get configmap %s", commonconsts.KubeConfigMapNameNetworkConfig)
		return
	}

	var className *string

	className_ := strings.TrimSpace(configMap.Data[commonconsts.KubeConfigMapKeyNetworkConfigIngressClass])
	if className_ != "" {
		className = &className_
	}

	annotations := make(map[string]string)

	annotations_ := strings.TrimSpace(configMap.Data[commonconsts.KubeConfigMapKeyNetworkConfigIngressAnnotations])
	if annotations_ != "" {
		err = json.Unmarshal([]byte(annotations_), &annotations)
		if err != nil {
			err = errors.Wrapf(err, "failed to json unmarshal %s in configmap %s: %s", commonconsts.KubeConfigMapKeyNetworkConfigIngressAnnotations, commonconsts.KubeConfigMapNameNetworkConfig, annotations_)
			return
		}
	}

	path := strings.TrimSpace(configMap.Data["ingress-path"])
	if path == "" {
		path = "/"
	}

	pathType := networkingv1.PathTypeImplementationSpecific

	pathType_ := strings.TrimSpace(configMap.Data["ingress-path-type"])
	if pathType_ != "" {
		pathType = networkingv1.PathType(pathType_)
	}

	tlsMode := TLSModeNone
	tlsModeStr := strings.TrimSpace(configMap.Data["ingress-tls-mode"])
	if tlsModeStr != "" && tlsModeStr != "none" {
		if tlsModeStr == "auto" || tlsModeStr == "static" {
			tlsMode = TLSModeOpt(tlsModeStr)
		} else {
			fmt.Println("Invalid TLS mode:", tlsModeStr)
			err = errors.Wrapf(err, "Invalid TLS mode: %s", tlsModeStr)
			return
		}
	}

	staticTLSSecretName := strings.TrimSpace(configMap.Data["ingress-static-tls-secret-name"])
	if tlsMode == TLSModeStatic && staticTLSSecretName == "" {
		err = errors.Wrapf(err, "TLS mode is static but ingress-static-tls-secret isn't set")
		return
	}

	ingressConfig = &IngressConfig{
		ClassName:           className,
		Annotations:         annotations,
		Path:                path,
		PathType:            pathType,
		TLSMode:             tlsMode,
		StaticTLSSecretName: staticTLSSecretName,
	}

	return
}

type generateIngressesOption struct {
	yataiClient     **yataiclient.YataiClient
	bentoDeployment *servingv2alpha1.BentoDeployment
	bento           *resourcesv1alpha1.Bento
}

func (r *BentoDeploymentReconciler) generateIngresses(ctx context.Context, opt generateIngressesOption) (ingresses []*networkingv1.Ingress, err error) {
	bentoRepositoryName, bentoVersion := getBentoRepositoryNameAndBentoVersion(opt.bento)
	bentoDeployment := opt.bentoDeployment
	bento := opt.bento

	kubeName := r.getKubeName(bentoDeployment, bento, nil, false)

	r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "GenerateIngressHost", "Generating hostname for ingress")
	internalHost, err := r.generateIngressHost(ctx, bentoDeployment)
	if err != nil {
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "GenerateIngressHost", "Failed to generate hostname for ingress: %v", err)
		return
	}
	r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "GenerateIngressHost", "Generated hostname for ingress: %s", internalHost)

	annotations := r.getKubeAnnotations(bentoDeployment, bento, nil)

	tag := fmt.Sprintf("%s:%s", bentoRepositoryName, bentoVersion)
	orgName := "unknown"

	if opt.yataiClient != nil {
		yataiClient := *opt.yataiClient
		var organization *schemasv1.OrganizationFullSchema
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "GetOrganization", "Getting organization for bento %s", tag)
		organization, err = yataiClient.GetOrganization(ctx)
		if err != nil {
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "GetOrganization", "Failed to get organization: %v", err)
			return
		}
		orgName = organization.Name
	}

	annotations["nginx.ingress.kubernetes.io/configuration-snippet"] = fmt.Sprintf(`
more_set_headers "X-Powered-By: Yatai";
more_set_headers "X-Yatai-Org-Name: %s";
more_set_headers "X-Yatai-Bento: %s";
`, orgName, tag)

	annotations["nginx.ingress.kubernetes.io/ssl-redirect"] = "false"

	labels := r.getKubeLabels(bentoDeployment, bento, nil)

	kubeNs := bentoDeployment.Namespace

	restConfig := config.GetConfigOrDie()
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		err = errors.Wrapf(err, "create kubernetes clientset")
		return
	}

	ingressConfig, err := GetIngressConfig(ctx, clientset)
	if err != nil {
		err = errors.Wrapf(err, "get ingress config")
		return
	}

	ingressClassName := ingressConfig.ClassName
	ingressAnnotations := ingressConfig.Annotations
	ingressPath := ingressConfig.Path
	ingressPathType := ingressConfig.PathType
	ingressTLSMode := ingressConfig.TLSMode
	ingressStaticTLSSecretName := ingressConfig.StaticTLSSecretName

	for k, v := range ingressAnnotations {
		annotations[k] = v
	}

	for k, v := range opt.bentoDeployment.Spec.Ingress.Annotations {
		annotations[k] = v
	}

	for k, v := range opt.bentoDeployment.Spec.Ingress.Labels {
		labels[k] = v
	}

	var tls []networkingv1.IngressTLS

	// set default tls from network configmap
	switch ingressTLSMode {
	case TLSModeNone:
	case TLSModeAuto:
		tls = make([]networkingv1.IngressTLS, 0, 1)
		tls = append(tls, networkingv1.IngressTLS{
			Hosts:      []string{internalHost},
			SecretName: kubeName,
		})

	case TLSModeStatic:
		tls = make([]networkingv1.IngressTLS, 0, 1)
		tls = append(tls, networkingv1.IngressTLS{
			Hosts:      []string{internalHost},
			SecretName: ingressStaticTLSSecretName,
		})
	default:
		err = errors.Wrapf(err, "TLS mode is invalid: %s", ingressTLSMode)
		return
	}

	// override default tls if BentoDeployment defines its own tls section
	if opt.bentoDeployment.Spec.Ingress.TLS != nil && opt.bentoDeployment.Spec.Ingress.TLS.SecretName != "" {
		tls = make([]networkingv1.IngressTLS, 0, 1)
		tls = append(tls, networkingv1.IngressTLS{
			Hosts:      []string{internalHost},
			SecretName: opt.bentoDeployment.Spec.Ingress.TLS.SecretName,
		})
	}

	serviceName := r.getGenericServiceName(bentoDeployment, bento, nil)

	interIng := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        kubeName,
			Namespace:   kubeNs,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: ingressClassName,
			TLS:              tls,
			Rules: []networkingv1.IngressRule{
				{
					Host: internalHost,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path:     ingressPath,
									PathType: &ingressPathType,
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: serviceName,
											Port: networkingv1.ServiceBackendPort{
												Name: commonconsts.BentoServicePortName,
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
	if err != nil {
		err = errors.Wrapf(err, "set ingress %s controller reference", interIng.Name)
		return
	}

	ings := []*networkingv1.Ingress{interIng}

	return ings, err
}

func (r *BentoDeploymentReconciler) doCleanUpAbandonedRunnerServices() error {
	logs := log.Log.WithValues("func", "doCleanUpAbandonedRunnerServices")
	logs.Info("start cleaning up abandoned runner services")
	ctx, cancel := context.WithTimeout(context.TODO(), time.Minute*10)
	defer cancel()

	restConf := config.GetConfigOrDie()
	cliset, err := kubernetes.NewForConfig(restConf)
	if err != nil {
		err = errors.Wrapf(err, "create kubernetes client for %s", restConf.Host)
		return err
	}

	bentoDeploymentNamespaces, err := commonconfig.GetBentoDeploymentNamespaces(ctx, cliset)
	if err != nil {
		err = errors.Wrapf(err, "get bento deployment namespaces")
		return err
	}

	for _, bentoDeploymentNamespace := range bentoDeploymentNamespaces {
		serviceList := &corev1.ServiceList{}
		serviceListOpts := []client.ListOption{
			client.HasLabels{commonconsts.KubeLabelYataiBentoDeploymentRunner},
			client.InNamespace(bentoDeploymentNamespace),
		}
		err := r.List(ctx, serviceList, serviceListOpts...)
		if err != nil {
			return errors.Wrap(err, "list services")
		}
		for _, service := range serviceList.Items {
			service := service
			podList := &corev1.PodList{}
			podListOpts := []client.ListOption{
				client.InNamespace(service.Namespace),
				client.MatchingLabels(service.Spec.Selector),
			}
			err := r.List(ctx, podList, podListOpts...)
			if err != nil {
				return errors.Wrap(err, "list pods")
			}
			if len(podList.Items) > 0 {
				continue
			}
			createdAt := service.ObjectMeta.CreationTimestamp
			if time.Since(createdAt.Time) < time.Minute*3 {
				continue
			}
			logs.Info("deleting abandoned runner service", "name", service.Name, "namespace", service.Namespace)
			err = r.Delete(ctx, &service)
			if err != nil {
				return errors.Wrapf(err, "delete service %s", service.Name)
			}
		}
	}
	logs.Info("finished cleaning up abandoned runner services")
	return nil
}

func (r *BentoDeploymentReconciler) cleanUpAbandonedRunnerServices() {
	logs := log.Log.WithValues("func", "cleanUpAbandonedRunnerServices")
	err := r.doCleanUpAbandonedRunnerServices()
	if err != nil {
		logs.Error(err, "cleanUpAbandonedRunnerServices")
	}
	ticker := time.NewTicker(time.Second * 30)
	for range ticker.C {
		err := r.doCleanUpAbandonedRunnerServices()
		if err != nil {
			logs.Error(err, "cleanUpAbandonedRunnerServices")
		}
	}
}

func (r *BentoDeploymentReconciler) doRegisterYataiComponent() (err error) {
	logs := log.Log.WithValues("func", "doRegisterYataiComponent")

	ctx, cancel := context.WithTimeout(context.TODO(), time.Minute*5)
	defer cancel()

	logs.Info("getting yatai client")
	yataiClient, clusterName, err := getYataiClient(ctx)
	if err != nil {
		err = errors.Wrap(err, "get yatai client")
		return
	}

	if yataiClient == nil {
		logs.Info("yatai client is nil")
		return
	}

	yataiClient_ := *yataiClient

	restConf := config.GetConfigOrDie()
	cliset, err := kubernetes.NewForConfig(restConf)
	if err != nil {
		err = errors.Wrapf(err, "create kubernetes client for %s", restConf.Host)
		return
	}

	namespace, err := commonconfig.GetYataiDeploymentNamespace(ctx, cliset)
	if err != nil {
		err = errors.Wrap(err, "get yatai deployment namespace")
		return
	}

	_, err = yataiClient_.RegisterYataiComponent(ctx, *clusterName, &schemasv1.RegisterYataiComponentSchema{
		Name:          modelschemas.YataiComponentNameDeployment,
		KubeNamespace: namespace,
		Version:       version.Version,
		SelectorLabels: map[string]string{
			"app.kubernetes.io/name": "yatai-deployment",
		},
		Manifest: &modelschemas.YataiComponentManifestSchema{
			SelectorLabels: map[string]string{
				"app.kubernetes.io/name": "yatai-deployment",
			},
			LatestCRDVersion: "v2alpha1",
		},
	})

	return err
}

func (r *BentoDeploymentReconciler) registerYataiComponent() {
	logs := log.Log.WithValues("func", "registerYataiComponent")
	err := r.doRegisterYataiComponent()
	if err != nil {
		logs.Error(err, "registerYataiComponent")
	}
	ticker := time.NewTicker(time.Minute * 5)
	for range ticker.C {
		err := r.doRegisterYataiComponent()
		if err != nil {
			logs.Error(err, "registerYataiComponent")
		}
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *BentoDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	logs := log.Log.WithValues("func", "SetupWithManager")
	version.Print()

	if os.Getenv("DISABLE_CLEANUP_ABANDONED_RUNNER_SERVICES") != commonconsts.KubeLabelValueTrue {
		go r.cleanUpAbandonedRunnerServices()
	} else {
		logs.Info("cleanup abandoned runner services is disabled")
	}

	if os.Getenv("DISABLE_YATAI_COMPONENT_REGISTRATION") != commonconsts.KubeLabelValueTrue {
		go r.registerYataiComponent()
	} else {
		logs.Info("yatai component registration is disabled")
	}

	pred := predicate.GenerationChangedPredicate{}
	m := ctrl.NewControllerManagedBy(mgr).
		For(&servingv2alpha1.BentoDeployment{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.Ingress{})

	if r.requireLegacyHPA() {
		m.Owns(&autoscalingv2beta2.HorizontalPodAutoscaler{})
	} else {
		m.Owns(&autoscalingv2.HorizontalPodAutoscaler{})
	}
	return m.WithEventFilter(pred).Complete(r)
}

func TransformToOldHPA(hpa *servingv2alpha1.Autoscaling) (oldHpa *modelschemas.DeploymentTargetHPAConf, err error) {
	if hpa == nil {
		return
	}

	oldHpa = &modelschemas.DeploymentTargetHPAConf{
		MinReplicas: commonutils.Int32Ptr(hpa.MinReplicas),
		MaxReplicas: commonutils.Int32Ptr(hpa.MaxReplicas),
	}

	for _, metric := range hpa.Metrics {
		if metric.Type == autoscalingv2beta2.PodsMetricSourceType {
			if metric.Pods == nil {
				continue
			}
			if metric.Pods.Metric.Name == commonconsts.KubeHPAQPSMetric {
				if metric.Pods.Target.Type != autoscalingv2beta2.UtilizationMetricType {
					continue
				}
				if metric.Pods.Target.AverageValue == nil {
					continue
				}
				qps := metric.Pods.Target.AverageValue.Value()
				oldHpa.QPS = &qps
			}
		} else if metric.Type == autoscalingv2beta2.ResourceMetricSourceType {
			if metric.Resource == nil {
				continue
			}
			if metric.Resource.Name == corev1.ResourceCPU {
				if metric.Resource.Target.Type != autoscalingv2beta2.UtilizationMetricType {
					continue
				}
				if metric.Resource.Target.AverageUtilization == nil {
					continue
				}
				cpu := *metric.Resource.Target.AverageUtilization
				oldHpa.CPU = &cpu
			} else if metric.Resource.Name == corev1.ResourceMemory {
				if metric.Resource.Target.Type != autoscalingv2beta2.UtilizationMetricType {
					continue
				}
				if metric.Resource.Target.AverageUtilization == nil {
					continue
				}
				memory := metric.Resource.Target.AverageValue.String()
				oldHpa.Memory = &memory
			}
		}
	}
	return
}
