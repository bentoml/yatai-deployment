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

	goversion "github.com/hashicorp/go-version"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2beta2 "k8s.io/api/autoscaling/v2beta2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"

	duration "github.com/golang/protobuf/ptypes/duration"
	istionetworking "istio.io/api/networking/v1alpha3"
	istio "istio.io/client-go/pkg/apis/networking/v1alpha3"

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

	servingv1alpha3 "github.com/bentoml/yatai-deployment-operator/apis/serving/v1alpha3"
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
//+kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingressclasses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.istio.io,resources=virtualservices,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.istio.io,resources=virtualservices/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=networking.istio.io,resources=destinationrules,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.istio.io,resources=destinationrules/status,verbs=get;update;patch

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

	bentoDeployment := &servingv1alpha3.BentoDeployment{}
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

	clusterName := getClusterName()

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
	r.Recorder.Event(bentoDeployment, corev1.EventTypeNormal, "GetDockerRegistryConfig", "Successfully unmarshaled docker registry config from secret")

	r.Recorder.Event(bentoDeployment, corev1.EventTypeNormal, "GetMajorCluster", "Fetching major cluster")
	majorCluster, err := yataiClient.GetMajorCluster(ctx)
	if err != nil {
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "GetMajorCluster", "Failed to fetch major cluster: %v", err)
		return
	}
	r.Recorder.Event(bentoDeployment, corev1.EventTypeNormal, "GetMajorCluster", "Successfully fetched major cluster")

	r.Recorder.Event(bentoDeployment, corev1.EventTypeNormal, "GetYataiVersion", "Fetching yatai version")
	version, err := yataiClient.GetVersion(ctx)
	if err != nil {
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "GetYataiVersion", "Failed to fetch yatai version: %v", err)
		return
	}
	r.Recorder.Event(bentoDeployment, corev1.EventTypeNormal, "GetYataiVersion", "Successfully fetched yatai version")

	modified := false

	if bento.Manifest != nil {
		for _, runner := range bento.Manifest.Runners {
			var modified_ bool
			// create or update deployment
			modified_, err = r.createOrUpdateDeployment(ctx, createOrUpdateDeploymentOption{
				yataiClient:     yataiClient,
				bentoDeployment: bentoDeployment,
				bento:           bento,
				dockerRegistry:  dockerRegistry,
				majorCluster:    majorCluster,
				version:         version,
				runnerName:      &runner.Name,
			})
			if err != nil {
				return
			}

			if modified_ {
				modified = true
			}

			// create or update hpa
			modified_, err = r.createOrUpdateHPA(ctx, bentoDeployment, bento, &runner.Name)
			if err != nil {
				return
			}

			if modified_ {
				modified = true
			}

			// create or update service
			modified_, err = r.createOrUpdateService(ctx, createOrUpdateServiceOption{
				bentoDeployment: bentoDeployment,
				bento:           bento,
				runnerName:      &runner.Name,
			})
			if err != nil {
				return
			}

			if modified_ {
				modified = true
			}

			if istioEnabled(bentoDeployment) {
				// create or update istio resources
				modified_, err = r.createOrUpdateIstioResources(ctx, createOrUpdateIstioResourcesOption{
					bentoDeployment: bentoDeployment,
					bento:           bento,
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
	}

	// create or update api-server deployment
	modified_, err := r.createOrUpdateDeployment(ctx, createOrUpdateDeploymentOption{
		yataiClient:     yataiClient,
		bentoDeployment: bentoDeployment,
		bento:           bento,
		dockerRegistry:  dockerRegistry,
		majorCluster:    majorCluster,
		version:         version,
		runnerName:      nil,
	})
	if err != nil {
		return
	}

	if modified_ {
		modified = true
	}

	// create or update api-server hpa
	modified_, err = r.createOrUpdateHPA(ctx, bentoDeployment, bento, nil)
	if err != nil {
		return
	}

	if modified_ {
		modified = true
	}

	// create or update api-server service
	modified_, err = r.createOrUpdateService(ctx, createOrUpdateServiceOption{
		bentoDeployment: bentoDeployment,
		bento:           bento,
		runnerName:      nil,
	})
	if err != nil {
		return
	}

	if modified_ {
		modified = true
	}

	if istioEnabled(bentoDeployment) {
		// create or update istio resources
		modified_, err = r.createOrUpdateIstioResources(ctx, createOrUpdateIstioResourcesOption{
			bentoDeployment: bentoDeployment,
			bento:           bento,
			runnerName:      nil,
		})
		if err != nil {
			return
		}

		if modified_ {
			modified = true
		}
	}

	// create or update api-server ingresses
	modified_, err = r.createOrUpdateIngresses(ctx, bentoDeployment, bento)
	if err != nil {
		return
	}

	if modified_ {
		modified = true
	}

	if modified {
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "GetYataiDeployment", "Fetching yatai deployment %s", bentoDeployment.Name)
		_, err = yataiClient.GetDeployment(ctx, clusterName, bentoDeployment.Namespace, bentoDeployment.Name)
		isNotFound := err != nil && strings.Contains(strings.ToLower(err.Error()), "not found")
		if err != nil && !isNotFound {
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "GetYataiDeployment", "Failed to fetch yatai deployment %s: %s", bentoDeployment.Name, err)
			return
		}
		err = nil

		envs := make([]*modelschemas.LabelItemSchema, 0)

		specEnvs := bentoDeployment.Spec.Envs

		if specEnvs != nil {
			for _, env := range *specEnvs {
				envs = append(envs, &modelschemas.LabelItemSchema{
					Key:   env.Key,
					Value: env.Value,
				})
			}
		}

		runners := make(map[string]modelschemas.DeploymentTargetRunnerConfig, 0)
		for _, runner := range bentoDeployment.Spec.Runners {
			envs_ := make([]*modelschemas.LabelItemSchema, 0)
			if runner.Envs != nil {
				for _, env := range *runner.Envs {
					env := env
					envs_ = append(envs_, &env)
				}
			}
			runners[runner.Name] = modelschemas.DeploymentTargetRunnerConfig{
				Resources: runner.Resources,
				HPAConf:   runner.Autoscaling,
				Envs:      &envs_,
			}
		}

		deploymentTargets := make([]*schemasv1.CreateDeploymentTargetSchema, 0, 1)
		deploymentTargets = append(deploymentTargets, &schemasv1.CreateDeploymentTargetSchema{
			DeploymentTargetTypeSchema: schemasv1.DeploymentTargetTypeSchema{
				Type: modelschemas.DeploymentTargetTypeStable,
			},
			BentoRepository: bento.Repository.Name,
			Bento:           bento.Name,
			Config: &modelschemas.DeploymentTargetConfig{
				KubeResourceUid: string(bentoDeployment.UID),
				Resources:       bentoDeployment.Spec.Resources,
				HPAConf:         bentoDeployment.Spec.Autoscaling,
				Envs:            &envs,
				Runners:         runners,
				EnableIngress:   &bentoDeployment.Spec.Ingress.Enabled,
			},
		})
		updateSchema := &schemasv1.UpdateDeploymentSchema{
			Targets: deploymentTargets,
		}
		if isNotFound {
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "CreateYataiDeployment", "Creating yatai deployment %s", bentoDeployment.Name)
			_, err = yataiClient.CreateDeployment(ctx, clusterName, &schemasv1.CreateDeploymentSchema{
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
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateYataiDeployment", "Updating yatai deployment %s", bentoDeployment.Name)
			_, err = yataiClient.UpdateDeployment(ctx, clusterName, bentoDeployment.Namespace, bentoDeployment.Name, updateSchema)
			if err != nil {
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "UpdateYataiDeployment", "Failed to update yatai deployment %s: %s", bentoDeployment.Name, err)
				return
			}
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateYataiDeployment", "Updated yatai deployment %s", bentoDeployment.Name)
		}
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "SyncYataiDeploymentStatus", "Syncing yatai deployment %s status", bentoDeployment.Name)
		_, err = yataiClient.SyncDeploymentStatus(ctx, clusterName, bentoDeployment.Namespace, bentoDeployment.Name)
		if err != nil {
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "SyncYataiDeploymentStatus", "Failed to sync yatai deployment %s status: %s", bentoDeployment.Name, err)
			return
		}
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "SyncYataiDeploymentStatus", "Synced yatai deployment %s status", bentoDeployment.Name)
	} else {
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateYataiDeployment", "No changes to yatai deployment %s", bentoDeployment.Name)
	}

	logs.Info("Finished reconciling.")
	r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "Update", "Updated")
	return
}

func istioEnabled(bentoDeployment *servingv1alpha3.BentoDeployment) bool {
	return bentoDeployment.Annotations[consts.KubeAnnotationServiceMesh] == "istio"
}

type createOrUpdateDeploymentOption struct {
	yataiClient     *yataiclient.YataiClient
	bentoDeployment *servingv1alpha3.BentoDeployment
	bento           *schemasv1.BentoFullSchema
	dockerRegistry  modelschemas.DockerRegistrySchema
	majorCluster    *schemasv1.ClusterFullSchema
	version         *schemasv1.VersionSchema
	runnerName      *string
}

func (r *BentoDeploymentReconciler) createOrUpdateDeployment(ctx context.Context, opt createOrUpdateDeploymentOption) (modified bool, err error) {
	logs := log.FromContext(ctx)

	organization_, err := opt.yataiClient.GetOrganization(ctx)
	if err != nil {
		return
	}

	clusterName := getClusterName()
	cluster_, err := opt.yataiClient.GetCluster(ctx, clusterName)
	if err != nil {
		return
	}

	deployment, err := r.generateDeployment(generateDeploymentOption{
		bentoDeployment: opt.bentoDeployment,
		bento:           opt.bento,
		dockerRegistry:  opt.dockerRegistry,
		majorCluster:    opt.majorCluster,
		version:         opt.version,
		runnerName:      opt.runnerName,
		organization:    organization_,
		cluster:         cluster_,
	})
	if err != nil {
		return
	}

	deploymentLogKeysAndValues := []interface{}{"namespace", deployment.Namespace, "name", deployment.Name}
	deploymentNamespacedName := fmt.Sprintf("%s/%s", deployment.Namespace, deployment.Name)

	r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "GetDeployment", "Getting Deployment %s", deploymentNamespacedName)

	oldDeployment := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: deployment.Name, Namespace: deployment.Namespace}, oldDeployment)
	oldDeploymentIsNotFound := k8serrors.IsNotFound(err)
	if err != nil && !oldDeploymentIsNotFound {
		r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "GetDeployment", "Failed to get Deployment %s: %s", deploymentNamespacedName, err)
		logs.Error(err, "Failed to get Deployment.", deploymentLogKeysAndValues...)
		return
	}

	if oldDeploymentIsNotFound {
		logs.Info("Deployment not found. Creating a new one.", deploymentLogKeysAndValues...)

		r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "CreateDeployment", "Creating a new Deployment %s", deploymentNamespacedName)
		err = r.Create(ctx, deployment)
		if err != nil {
			logs.Error(err, "Failed to create Deployment.", deploymentLogKeysAndValues...)
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "CreateDeployment", "Failed to create Deployment %s: %s", deploymentNamespacedName, err)
			return
		}
		logs.Info("Deployment created.", deploymentLogKeysAndValues...)
		r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "CreateDeployment", "Created Deployment %s", deploymentNamespacedName)
		modified = true
	} else {
		logs.Info("Deployment found.", deploymentLogKeysAndValues...)

		status := r.generateStatus(opt.bentoDeployment, oldDeployment)

		if !reflect.DeepEqual(status, opt.bentoDeployment.Status) {
			opt.bentoDeployment.Status = status
			err = r.Status().Update(ctx, opt.bentoDeployment)
			if err != nil {
				logs.Error(err, "Failed to update BentoDeployment status.")
				return
			}
			clusterName := getClusterName()
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "GetYataiDeployment", "Fetching yatai deployment %s", opt.bentoDeployment.Name)
			_, err = opt.yataiClient.GetDeployment(ctx, clusterName, opt.bentoDeployment.Namespace, opt.bentoDeployment.Name)
			isNotFound := err != nil && strings.Contains(strings.ToLower(err.Error()), "not found")
			if err != nil && !isNotFound {
				r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "GetYataiDeployment", "Failed to fetch yatai deployment %s: %s", opt.bentoDeployment.Name, err)
				return
			}
			err = nil
			if !isNotFound {
				r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "SyncYataiDeploymentStatus", "Syncing yatai deployment %s status: %s", opt.bentoDeployment.Name, err)
				_, err = opt.yataiClient.SyncDeploymentStatus(ctx, clusterName, opt.bentoDeployment.Namespace, opt.bentoDeployment.Name)
				if err != nil {
					r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "SyncYataiDeploymentStatus", "Failed to sync yatai deployment %s status: %s", opt.bentoDeployment.Name, err)
					return
				}
				r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "SyncYataiDeploymentStatus", "Synced yatai deployment %s status", opt.bentoDeployment.Name)
			}
		}

		var patchResult *patch.PatchResult
		patchResult, err = patch.DefaultPatchMaker.Calculate(oldDeployment, deployment)
		if err != nil {
			logs.Error(err, "Failed to calculate patch.", deploymentLogKeysAndValues...)
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "CalculatePatch", "Failed to calculate patch for Deployment %s: %s", deploymentNamespacedName, err)
			return
		}

		if !patchResult.IsEmpty() {
			logs.Info("Deployment spec is different. Updating Deployment.", deploymentLogKeysAndValues...)

			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "UpdateDeployment", "Updating Deployment %s", deploymentNamespacedName)
			err = r.Update(ctx, deployment)
			if err != nil {
				logs.Error(err, "Failed to update Deployment.", deploymentLogKeysAndValues...)
				r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "UpdateDeployment", "Failed to update Deployment %s: %s", deploymentNamespacedName, err)
				return
			}
			logs.Info("Deployment updated.", deploymentLogKeysAndValues...)
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "UpdateDeployment", "Updated Deployment %s", deploymentNamespacedName)
			modified = true
		} else {
			logs.Info("Deployment spec is the same. Skipping update.", deploymentLogKeysAndValues...)
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "UpdateDeployment", "Skipping update Deployment %s", deploymentNamespacedName)
		}
	}

	return
}

func (r *BentoDeploymentReconciler) createOrUpdateHPA(ctx context.Context, bentoDeployment *servingv1alpha3.BentoDeployment, bento *schemasv1.BentoFullSchema, runnerName *string) (modified bool, err error) {
	logs := log.FromContext(ctx)

	hpa, err := r.generateHPA(bentoDeployment, bento, runnerName)
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
		modified = true
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
			modified = true
		} else {
			logs.Info("HPA spec is the same. Skipping update.", hpaLogKeysAndValues...)
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateHPA", "Skipping update HPA %s", hpaNamespacedName)
		}
	}

	return
}

type createOrUpdateServiceOption struct {
	bentoDeployment *servingv1alpha3.BentoDeployment
	bento           *schemasv1.BentoFullSchema
	runnerName      *string
}

func (r *BentoDeploymentReconciler) createOrUpdateService(ctx context.Context, opt createOrUpdateServiceOption) (modified bool, err error) {
	logs := log.FromContext(ctx)

	service, err := r.generateService(opt.bentoDeployment, opt.bento, opt.runnerName)
	if err != nil {
		return
	}

	serviceLogKeysAndValues := []interface{}{"namespace", service.Namespace, "name", service.Name}
	serviceNamespacedName := fmt.Sprintf("%s/%s", service.Namespace, service.Name)

	r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "GetService", "Getting Service %s", serviceNamespacedName)

	oldService := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: service.Name, Namespace: service.Namespace}, oldService)
	oldServiceIsNotFound := k8serrors.IsNotFound(err)
	if err != nil && !oldServiceIsNotFound {
		r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "GetService", "Failed to get Service %s: %s", serviceNamespacedName, err)
		logs.Error(err, "Failed to get Service.", serviceLogKeysAndValues...)
		return
	}

	if oldServiceIsNotFound {
		logs.Info("Service not found. Creating a new one.", serviceLogKeysAndValues...)

		r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "CreateService", "Creating a new Service %s", serviceNamespacedName)
		err = r.Create(ctx, service)
		if err != nil {
			logs.Error(err, "Failed to create Service.", serviceLogKeysAndValues...)
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "CreateService", "Failed to create Service %s: %s", serviceNamespacedName, err)
			return
		}
		logs.Info("Service created.", serviceLogKeysAndValues...)
		r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "CreateService", "Created Service %s", serviceNamespacedName)
		modified = true
	} else {
		logs.Info("Service found.", serviceLogKeysAndValues...)

		var patchResult *patch.PatchResult
		patchResult, err = patch.DefaultPatchMaker.Calculate(oldService, service)
		if err != nil {
			logs.Error(err, "Failed to calculate patch.", serviceLogKeysAndValues...)
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "CalculatePatch", "Failed to calculate patch for Service %s: %s", serviceNamespacedName, err)
			return
		}

		if !patchResult.IsEmpty() {
			logs.Info("Service spec is different. Updating Service.", serviceLogKeysAndValues...)

			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "UpdateService", "Updating Service %s", serviceNamespacedName)
			err = r.Update(ctx, service)
			if err != nil {
				logs.Error(err, "Failed to update Service.", serviceLogKeysAndValues...)
				r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "UpdateService", "Failed to update Service %s: %s", serviceNamespacedName, err)
				return
			}
			logs.Info("Service updated.", serviceLogKeysAndValues...)
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "UpdateService", "Updated Service %s", serviceNamespacedName)
			modified = true
		} else {
			logs.Info("Service spec is the same. Skipping update.", serviceLogKeysAndValues...)
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "UpdateService", "Skipping update Service %s", serviceNamespacedName)
		}
	}

	return
}

func (r *BentoDeploymentReconciler) createOrUpdateIngresses(ctx context.Context, bentoDeployment *servingv1alpha3.BentoDeployment, bento *schemasv1.BentoFullSchema) (modified bool, err error) {
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
		err = nil

		if oldIngressIsNotFound {
			if !bentoDeployment.Spec.Ingress.Enabled {
				logs.Info("Ingress not enabled. Skipping.", ingressLogKeysAndValues...)
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "GetIngress", "Skipping Ingress %s", ingressNamespacedName)
				continue
			}

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
			modified = true
		} else {
			logs.Info("Ingress found.", ingressLogKeysAndValues...)

			if !bentoDeployment.Spec.Ingress.Enabled {
				logs.Info("Ingress not enabled. Deleting.", ingressLogKeysAndValues...)
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "DeleteIngress", "Deleting Ingress %s", ingressNamespacedName)
				err = r.Delete(ctx, ingress)
				if err != nil {
					logs.Error(err, "Failed to delete Ingress.", ingressLogKeysAndValues...)
					r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "DeleteIngress", "Failed to delete Ingress %s: %s", ingressNamespacedName, err)
					return
				}
				logs.Info("Ingress deleted.", ingressLogKeysAndValues...)
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "DeleteIngress", "Deleted Ingress %s", ingressNamespacedName)
				modified = true
				continue
			}

			// Keep host unchanged
			ingress.Spec.Rules[0].Host = oldIngress.Spec.Rules[0].Host

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
				modified = true
			} else {
				logs.Info("Ingress spec is the same. Skipping update.", ingressLogKeysAndValues...)
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateIngress", "Skipping update Ingress %s", ingressNamespacedName)
			}
		}
	}

	return
}

func (r *BentoDeploymentReconciler) generateStatus(bentoDeployment *servingv1alpha3.BentoDeployment, deployment *appsv1.Deployment) servingv1alpha3.BentoDeploymentStatus {
	labels := r.getKubeLabels(bentoDeployment, nil)
	status := servingv1alpha3.BentoDeploymentStatus{
		PodSelector:         labels,
		Replicas:            deployment.Status.Replicas,
		ReadyReplicas:       deployment.Status.ReadyReplicas,
		UpdatedReplicas:     deployment.Status.UpdatedReplicas,
		AvailableReplicas:   deployment.Status.AvailableReplicas,
		UnavailableReplicas: deployment.Status.UnavailableReplicas,
		PrinterReady:        fmt.Sprintf("%d/%d", deployment.Status.ReadyReplicas, deployment.Status.Replicas),
	}
	return status
}

func (r *BentoDeploymentReconciler) getKubeName(bentoDeployment *servingv1alpha3.BentoDeployment, bento *schemasv1.BentoFullSchema, runnerName *string) string {
	if runnerName != nil && bento.Manifest != nil {
		for idx, runner := range bento.Manifest.Runners {
			if runner.Name == *runnerName {
				return fmt.Sprintf("%s-runner-%d", bentoDeployment.Name, idx)
			}
		}
	}
	return bentoDeployment.Name
}

func (r *BentoDeploymentReconciler) getKubeLabels(bentoDeployment *servingv1alpha3.BentoDeployment, runnerName *string) map[string]string {
	labels := map[string]string{
		consts.KubeLabelYataiDeployment: bentoDeployment.Name,
		consts.KubeLabelCreator:         consts.KubeCreator,
	}
	if runnerName != nil {
		labels[consts.KubeLabelYataiBentoRunner] = *runnerName
	} else {
		labels[consts.KubeLabelYataiIsBentoApiServer] = "true"
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

type generateDeploymentOption struct {
	bentoDeployment *servingv1alpha3.BentoDeployment
	bento           *schemasv1.BentoFullSchema
	dockerRegistry  modelschemas.DockerRegistrySchema
	majorCluster    *schemasv1.ClusterFullSchema
	version         *schemasv1.VersionSchema
	runnerName      *string
	organization    *schemasv1.OrganizationFullSchema
	cluster         *schemasv1.ClusterFullSchema
}

func (r *BentoDeploymentReconciler) generateDeployment(opt generateDeploymentOption) (kubeDeployment *appsv1.Deployment, err error) {
	kubeNs := opt.bentoDeployment.Namespace

	// nolint: gosimple
	podTemplateSpec, err := r.generatePodTemplateSpec(generatePodTemplateSpecOption{
		bentoDeployment: opt.bentoDeployment,
		bento:           opt.bento,
		dockerRegistry:  opt.dockerRegistry,
		majorCluster:    opt.majorCluster,
		version:         opt.version,
		runnerName:      opt.runnerName,
		organization:    opt.organization,
		cluster:         opt.cluster,
	})
	if err != nil {
		return
	}

	labels := r.getKubeLabels(opt.bentoDeployment, opt.runnerName)

	annotations := r.getKubeAnnotations(opt.bento)

	kubeName := r.getKubeName(opt.bentoDeployment, opt.bento, opt.runnerName)

	defaultMaxSurge := intstr.FromString("25%")
	defaultMaxUnavailable := intstr.FromString("25%")

	strategy := appsv1.DeploymentStrategy{
		Type: appsv1.RollingUpdateDeploymentStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDeployment{
			MaxSurge:       &defaultMaxSurge,
			MaxUnavailable: &defaultMaxUnavailable,
		},
	}

	replicas := utils.Int32Ptr(2)
	var autoscaling *modelschemas.DeploymentTargetHPAConf

	if opt.runnerName != nil {
		for _, runner := range opt.bentoDeployment.Spec.Runners {
			if runner.Name == *opt.runnerName {
				autoscaling = runner.Autoscaling
				break
			}
		}
	} else {
		autoscaling = opt.bentoDeployment.Spec.Autoscaling
	}

	if autoscaling != nil {
		replicas = autoscaling.MinReplicas
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
					consts.KubeLabelYataiSelector: kubeName,
				},
			},
			Template: *podTemplateSpec,
			Strategy: strategy,
		},
	}

	err = ctrl.SetControllerReference(opt.bentoDeployment, kubeDeployment, r.Scheme)

	return
}

func (r *BentoDeploymentReconciler) generateHPA(bentoDeployment *servingv1alpha3.BentoDeployment, bento *schemasv1.BentoFullSchema, runnerName *string) (hpa *autoscalingv2beta2.HorizontalPodAutoscaler, err error) {
	labels := r.getKubeLabels(bentoDeployment, runnerName)

	annotations := r.getKubeAnnotations(bento)

	kubeName := r.getKubeName(bentoDeployment, bento, runnerName)

	kubeNs := bentoDeployment.Namespace

	var hpaConf *modelschemas.DeploymentTargetHPAConf

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

	maxReplicas := utils.Int32Ptr(consts.HPADefaultMaxReplicas)
	if hpaConf != nil && hpaConf.MaxReplicas != nil {
		maxReplicas = hpaConf.MaxReplicas
	}

	var metrics []autoscalingv2beta2.MetricSpec
	if hpaConf != nil && hpaConf.QPS != nil && *hpaConf.QPS > 0 {
		metrics = append(metrics, autoscalingv2beta2.MetricSpec{
			Type: autoscalingv2beta2.PodsMetricSourceType,
			Pods: &autoscalingv2beta2.PodsMetricSource{
				Metric: autoscalingv2beta2.MetricIdentifier{
					Name: consts.KubeHPAQPSMetric,
				},
				Target: autoscalingv2beta2.MetricTarget{
					Type:         autoscalingv2beta2.UtilizationMetricType,
					AverageValue: resource.NewQuantity(*hpaConf.QPS, resource.DecimalSI),
				},
			},
		})
	}

	if hpaConf != nil && hpaConf.CPU != nil && *hpaConf.CPU > 0 {
		metrics = append(metrics, autoscalingv2beta2.MetricSpec{
			Type: autoscalingv2beta2.ResourceMetricSourceType,
			Resource: &autoscalingv2beta2.ResourceMetricSource{
				Name: corev1.ResourceCPU,
				Target: autoscalingv2beta2.MetricTarget{
					Type:               autoscalingv2beta2.UtilizationMetricType,
					AverageUtilization: hpaConf.CPU,
				},
			},
		})
	}

	if hpaConf != nil && hpaConf.Memory != nil && *hpaConf.Memory != "" {
		var quantity resource.Quantity
		quantity, err = resource.ParseQuantity(*hpaConf.Memory)
		if err != nil {
			err = errors.Wrapf(err, "parse memory %s", *hpaConf.Memory)
			return
		}
		metrics = append(metrics, autoscalingv2beta2.MetricSpec{
			Type: autoscalingv2beta2.ResourceMetricSourceType,
			Resource: &autoscalingv2beta2.ResourceMetricSource{
				Name: corev1.ResourceMemory,
				Target: autoscalingv2beta2.MetricTarget{
					Type:         autoscalingv2beta2.UtilizationMetricType,
					AverageValue: &quantity,
				},
			},
		})
	}

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

	minReplicas := utils.Int32Ptr(2)
	if hpaConf != nil && hpaConf.MinReplicas != nil {
		minReplicas = hpaConf.MinReplicas
	}

	kubeHpa := &autoscalingv2beta2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:        kubeName,
			Namespace:   kubeNs,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: autoscalingv2beta2.HorizontalPodAutoscalerSpec{
			MinReplicas: minReplicas,
			MaxReplicas: *maxReplicas,
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

func getClusterName() string {
	clusterName := os.Getenv(consts.EnvYataiClusterName)
	if clusterName == "" {
		clusterName = "default"
	}
	return clusterName
}

type generatePodTemplateSpecOption struct {
	bentoDeployment *servingv1alpha3.BentoDeployment
	bento           *schemasv1.BentoFullSchema
	dockerRegistry  modelschemas.DockerRegistrySchema
	majorCluster    *schemasv1.ClusterFullSchema
	version         *schemasv1.VersionSchema
	runnerName      *string
	organization    *schemasv1.OrganizationFullSchema
	cluster         *schemasv1.ClusterFullSchema
}

func (r *BentoDeploymentReconciler) generatePodTemplateSpec(opt generatePodTemplateSpecOption) (podTemplateSpec *corev1.PodTemplateSpec, err error) {
	podLabels := r.getKubeLabels(opt.bentoDeployment, opt.runnerName)

	annotations := r.getKubeAnnotations(opt.bento)

	clusterName := getClusterName()

	kubeName := r.getKubeName(opt.bentoDeployment, opt.bento, opt.runnerName)

	inCluster := clusterName == opt.majorCluster.Name

	imageName := opt.bento.ImageName
	if inCluster {
		imageName = opt.bento.InClusterImageName
	}

	containerPort := consts.BentoServicePort
	var envs []corev1.EnvVar
	envsSeen := make(map[string]struct{})

	var specEnvs *[]modelschemas.LabelItemSchema
	if opt.runnerName != nil {
		for _, runner := range opt.bentoDeployment.Spec.Runners {
			if runner.Name == *opt.runnerName {
				specEnvs = runner.Envs
				break
			}
		}
	} else {
		specEnvs = opt.bentoDeployment.Spec.Envs
	}

	if specEnvs != nil {
		envs = make([]corev1.EnvVar, 0, len(*specEnvs)+1)

		for _, env := range *specEnvs {
			if _, ok := envsSeen[env.Key]; ok {
				continue
			}
			if env.Key == consts.BentoServicePortEnvName {
				containerPort, err = strconv.Atoi(env.Value)
				if err != nil {
					return nil, errors.Wrapf(err, "invalid port value %s", env.Value)
				}
			}
			envsSeen[env.Key] = struct{}{}
			envs = append(envs, corev1.EnvVar{
				Name:  env.Key,
				Value: env.Value,
			})
		}
	}

	defaultEnvs := map[string]string{
		consts.BentoServicePortEnvName:               fmt.Sprintf("%d", containerPort),
		consts.BentoServiceYataiVersionEnvName:       fmt.Sprintf("%s-%s", opt.version.Version, opt.version.GitCommit),
		consts.BentoServiceYataiOrgUIDEnvName:        opt.organization.Uid,
		consts.BentoServiceYataiDeploymentUIDEnvName: string(opt.bentoDeployment.UID),
		consts.BentoServiceYataiClusterUIDEnvName:    opt.cluster.Uid,
	}

	for k, v := range defaultEnvs {
		if _, ok := envsSeen[consts.BentoServicePortEnvName]; !ok {
			envs = append(envs, corev1.EnvVar{
				Name:  k,
				Value: v,
			})
		}
	}

	livenessProbe := &corev1.Probe{
		InitialDelaySeconds: 10,
		TimeoutSeconds:      20,
		FailureThreshold:    6,
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/livez",
				Port: intstr.FromInt(containerPort),
			},
		},
	}

	readinessProbe := &corev1.Probe{
		InitialDelaySeconds: 10,
		TimeoutSeconds:      20,
		FailureThreshold:    6,
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/readyz",
				Port: intstr.FromInt(containerPort),
			},
		},
	}

	var runnerInfo *modelschemas.BentoRunnerSchema
	if opt.runnerName != nil && opt.bento.Manifest != nil && opt.bento.Manifest.Runners != nil {
		for _, r := range opt.bento.Manifest.Runners {
			r := r
			if r.Name == *opt.runnerName {
				runnerInfo = &r
				break
			}
		}
	}

	containers := make([]corev1.Container, 0, 1)

	vs := make([]corev1.Volume, 0)
	vms := make([]corev1.VolumeMount, 0)

	models_ := opt.bento.Models

	mountedModelNames := make(map[string]struct{}, len(models_))
	if runnerInfo != nil && runnerInfo.Models != nil {
		for _, m := range runnerInfo.Models {
			mountedModelNames[m] = struct{}{}
		}
	} else {
		for _, m := range models_ {
			mountedModelNames[fmt.Sprintf("%s:%s", m.Repository.Name, m.Version)] = struct{}{}
		}
	}

	args := make([]string, 0)
	imageTlsVerify := "false"
	if opt.dockerRegistry.Secure {
		imageTlsVerify = "true"
	}

	for _, model := range models_ {
		if _, ok := mountedModelNames[fmt.Sprintf("%s:%s", model.Repository.Name, model.Version)]; !ok {
			continue
		}

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

	isOldVersion := false
	if opt.bento.Manifest != nil && opt.bento.Manifest.BentomlVersion != "" {
		var currentVersion *goversion.Version
		currentVersion, err = goversion.NewVersion(opt.bento.Manifest.BentomlVersion)
		if err != nil {
			err = errors.Wrapf(err, "invalid bentoml version %s", opt.bento.Manifest.BentomlVersion)
			return
		}
		var targetVersion *goversion.Version
		targetVersion, err = goversion.NewVersion("1.0.0a7")
		if err != nil {
			err = errors.Wrapf(err, "invalid target version %s", opt.bento.Manifest.BentomlVersion)
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
		if opt.bento.Manifest != nil && len(opt.bento.Manifest.Runners) > 0 {
			// python -m bentoml._internal.server.cli.api_server  iris_classifier:ohzovcfvvseu3lg6 tcp://127.0.0.1:8000 --runner-map '{"iris_clf": "tcp://127.0.0.1:8001"}' --working-dir .
			runnerMap := make(map[string]string, len(opt.bento.Manifest.Runners))
			for _, runner := range opt.bento.Manifest.Runners {
				runnerServiceName := r.getKubeName(opt.bentoDeployment, opt.bento, &runner.Name)
				runnerMap[runner.Name] = fmt.Sprintf("tcp://%s:%d", runnerServiceName, consts.BentoServicePort)
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

	var resources corev1.ResourceRequirements
	if opt.bentoDeployment.Spec.Resources != nil {
		resources, err = getResourcesConfig(opt.bentoDeployment.Spec.Resources)
		if err != nil {
			err = errors.Wrap(err, "failed to get resources config")
			return
		}
	}

	container := corev1.Container{
		Name:           kubeName,
		Image:          imageName,
		Command:        []string{"sh", "-c"},
		Args:           []string{strings.Join(args, " ")},
		LivenessProbe:  livenessProbe,
		ReadinessProbe: readinessProbe,
		Resources:      resources,
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

	if opt.dockerRegistry.Username != "" {
		podTemplateSpec.Spec.ImagePullSecrets = []corev1.LocalObjectReference{
			{
				Name: consts.KubeSecretNameRegcred,
			},
		}
	}

	return
}

func getResourcesConfig(resources *modelschemas.DeploymentTargetResources) (corev1.ResourceRequirements, error) {
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

	resourceConf := resources
	if resourceConf != nil {
		if resourceConf.Limits != nil {
			if resourceConf.Limits.CPU != "" {
				q, err := resource.ParseQuantity(resourceConf.Limits.CPU)
				if err != nil {
					return currentResources, errors.Wrapf(err, "parse limits cpu quantity")
				}
				if currentResources.Limits == nil {
					currentResources.Limits = make(corev1.ResourceList)
				}
				currentResources.Limits[corev1.ResourceCPU] = q
			}
			if resourceConf.Limits.Memory != "" {
				q, err := resource.ParseQuantity(resourceConf.Limits.Memory)
				if err != nil {
					return currentResources, errors.Wrapf(err, "parse limits memory quantity")
				}
				if currentResources.Limits == nil {
					currentResources.Limits = make(corev1.ResourceList)
				}
				currentResources.Limits[corev1.ResourceMemory] = q
			}
			if resourceConf.Limits.GPU != "" {
				q, err := resource.ParseQuantity(resourceConf.Limits.GPU)
				if err != nil {
					return currentResources, errors.Wrapf(err, "parse limits gpu quantity")
				}
				if currentResources.Limits == nil {
					currentResources.Limits = make(corev1.ResourceList)
				}
				currentResources.Limits[consts.KubeResourceGPUNvidia] = q
			}
		}
		if resourceConf.Requests != nil {
			if resourceConf.Requests.CPU != "" {
				q, err := resource.ParseQuantity(resourceConf.Requests.CPU)
				if err != nil {
					return currentResources, errors.Wrapf(err, "parse requests cpu quantity")
				}
				if currentResources.Requests == nil {
					currentResources.Requests = make(corev1.ResourceList)
				}
				currentResources.Requests[corev1.ResourceCPU] = q
			}
			if resourceConf.Requests.Memory != "" {
				q, err := resource.ParseQuantity(resourceConf.Requests.Memory)
				if err != nil {
					return currentResources, errors.Wrapf(err, "parse requests memory quantity")
				}
				if currentResources.Requests == nil {
					currentResources.Requests = make(corev1.ResourceList)
				}
				currentResources.Requests[corev1.ResourceMemory] = q
			}
		}
	}
	return currentResources, nil
}

func (r *BentoDeploymentReconciler) generateService(bentoDeployment *servingv1alpha3.BentoDeployment, bento *schemasv1.BentoFullSchema, runnerName *string) (kubeService *corev1.Service, err error) {
	kubeName := r.getKubeName(bentoDeployment, bento, runnerName)

	targetPort := consts.BentoServicePort

	var specEnvs *[]modelschemas.LabelItemSchema
	if runnerName != nil {
		for _, runner := range bentoDeployment.Spec.Runners {
			if runner.Name == *runnerName {
				specEnvs = runner.Envs
				break
			}
		}
	} else {
		specEnvs = bentoDeployment.Spec.Envs
	}

	if specEnvs != nil {
		for _, env := range *specEnvs {
			if env.Key == consts.BentoServicePortEnvName {
				port_, err := strconv.Atoi(env.Value)
				if err != nil {
					return nil, errors.Wrapf(err, "convert port %s to int", env.Value)
				}
				targetPort = port_
				break
			}
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

	labels := r.getKubeLabels(bentoDeployment, runnerName)

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

func getAnnotation(bentoDeployment *servingv1alpha3.BentoDeployment, key, default_ string, runnerName *string) (value string) {
	if runnerName != nil {
		for _, runner := range bentoDeployment.Spec.Runners {
			if runner.Name == *runnerName {
				if value, ok := runner.Annotations[key]; ok {
					return value
				}
				return default_
			}
		}
		return default_
	}

	if value, ok := bentoDeployment.Annotations[key]; ok {
		return value
	}
	return default_
}

type generateIstioResourcesOption struct {
	bentoDeployment *servingv1alpha3.BentoDeployment
	bento           *schemasv1.BentoFullSchema
	runnerName      *string
}

func (r *BentoDeploymentReconciler) generateIstioResources(opt generateIstioResourcesOption) (virtualServices []*istio.VirtualService, destinationRules []*istio.DestinationRule, err error) {
	istioRetriesAnnotation := getAnnotation(opt.bentoDeployment, consts.KubeAnnotationIstioRetries, "", opt.runnerName)
	istioRetriesTimeoutAnnotation := getAnnotation(opt.bentoDeployment, consts.KubeAnnotationIstioRetriesTimeout, "1", opt.runnerName)
	istioRetries := 0
	istioRetriesTimeout := 1

	if istioRetriesAnnotation != "" {
		// nolint: gosec
		istioRetries, err = strconv.Atoi(istioRetriesAnnotation)
		if err != nil {
			return nil, nil, err
		}
		// nolint: gosec
		istioRetriesTimeout, err = strconv.Atoi(istioRetriesTimeoutAnnotation)
		if err != nil {
			return nil, nil, err
		}
	}

	kubeName := r.getKubeName(opt.bentoDeployment, opt.bento, opt.runnerName)
	kubeNs := opt.bentoDeployment.Namespace

	vsvc := &istio.VirtualService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kubeName,
			Namespace: kubeNs,
		},
		Spec: istionetworking.VirtualService{
			Hosts: []string{getAnnotation(opt.bentoDeployment, consts.KubeAnnotationIstioHost, kubeName, opt.runnerName)},
			Http: []*istionetworking.HTTPRoute{
				{
					Route: []*istionetworking.HTTPRouteDestination{
						{

							Destination: &istionetworking.Destination{
								Host:   kubeName,
								Subset: getAnnotation(opt.bentoDeployment, consts.KubeAnnotationIstioSubset, "v1", opt.runnerName),
							},
						},
					},
				},
			},
		},
	}

	if istioRetries > 0 {
		vsvc.Spec.Http[0].Retries = &istionetworking.HTTPRetry{Attempts: int32(istioRetries), PerTryTimeout: &duration.Duration{Seconds: int64(istioRetriesTimeout)}, RetryOn: "gateway-error,connect-failure,refused-stream"}
		vsvc.Spec.Http[1].Retries = &istionetworking.HTTPRetry{Attempts: int32(istioRetries), PerTryTimeout: &duration.Duration{Seconds: int64(istioRetriesTimeout)}, RetryOn: "gateway-error,connect-failure,refused-stream"}
	}

	err = ctrl.SetControllerReference(opt.bentoDeployment, vsvc, r.Scheme)
	if err != nil {
		err = errors.Wrapf(err, "set controller reference for virtual service %s", kubeName)
		return
	}

	virtualServices = append(virtualServices, vsvc)

	labels := r.getKubeLabels(opt.bentoDeployment, opt.runnerName)

	drule := &istio.DestinationRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kubeName,
			Namespace: kubeNs,
		},
		Spec: istionetworking.DestinationRule{
			Host: kubeName,
			Subsets: []*istionetworking.Subset{
				{
					Name:   "v1",
					Labels: labels,
				},
			},
			TrafficPolicy: &istionetworking.TrafficPolicy{ConnectionPool: &istionetworking.ConnectionPoolSettings{Http: &istionetworking.ConnectionPoolSettings_HTTPSettings{IdleTimeout: &duration.Duration{Seconds: 60}}}},
		},
	}

	err = ctrl.SetControllerReference(opt.bentoDeployment, drule, r.Scheme)
	if err != nil {
		err = errors.Wrapf(err, "set controller reference for destination rule %s", kubeName)
		return
	}

	destinationRules = append(destinationRules, drule)

	return
}

type createOrUpdateIstioResourcesOption struct {
	bentoDeployment *servingv1alpha3.BentoDeployment
	bento           *schemasv1.BentoFullSchema
	runnerName      *string
}

func (r *BentoDeploymentReconciler) createOrUpdateIstioResources(ctx context.Context, opt createOrUpdateIstioResourcesOption) (modified bool, err error) {
	logs := log.FromContext(ctx)

	// nolint: gosimple
	virtualServices, destinationRules, err := r.generateIstioResources(generateIstioResourcesOption{
		bentoDeployment: opt.bentoDeployment,
		bento:           opt.bento,
		runnerName:      opt.runnerName,
	})
	if err != nil {
		return
	}

	for _, destRule := range destinationRules {
		destRuleLogKeysAndValues := []interface{}{"namespace", destRule.Namespace, "name", destRule.Name}
		destRuleNamespacedName := fmt.Sprintf("%s/%s", destRule.Namespace, destRule.Name)

		r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "GetDestinationRule", "Getting DestinationRule %s", destRuleNamespacedName)

		oldDestRule := &istio.DestinationRule{}
		err = r.Get(ctx, types.NamespacedName{Name: destRule.Name, Namespace: destRule.Namespace}, oldDestRule)
		oldDestRuleIsNotFound := k8serrors.IsNotFound(err)
		if err != nil && !oldDestRuleIsNotFound {
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "GetDestinationRule", "Failed to get DestinationRule %s: %s", destRuleNamespacedName, err)
			logs.Error(err, "Failed to get DestinationRule.", destRuleLogKeysAndValues...)
			return
		}

		if oldDestRuleIsNotFound {
			logs.Info("DestinationRule not found. Creating a new one.", destRuleLogKeysAndValues...)

			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "CreateDestinationRule", "Creating a new DestinationRule %s", destRuleNamespacedName)
			err = r.Create(ctx, destRule)
			if err != nil {
				logs.Error(err, "Failed to create DestinationRule.", destRuleLogKeysAndValues...)
				r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "CreateDestinationRule", "Failed to create DestinationRule %s: %s", destRuleNamespacedName, err)
				return
			}
			logs.Info("DestinationRule created.", destRuleLogKeysAndValues...)
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "CreateDestinationRule", "Created DestinationRule %s", destRuleNamespacedName)
			modified = true
		} else {
			logs.Info("DestinationRule found.", destRuleLogKeysAndValues...)

			var patchResult *patch.PatchResult
			patchResult, err = patch.DefaultPatchMaker.Calculate(oldDestRule, destRule)
			if err != nil {
				logs.Error(err, "Failed to calculate patch.", destRuleLogKeysAndValues...)
				r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "CalculatePatch", "Failed to calculate patch for DestinationRule %s: %s", destRuleNamespacedName, err)
				return
			}

			if !patchResult.IsEmpty() {
				logs.Info("DestinationRule spec is different. Updating DestinationRule.", destRuleLogKeysAndValues...)

				r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "UpdateDestinationRule", "Updating DestinationRule %s", destRuleNamespacedName)
				err = r.Update(ctx, destRule)
				if err != nil {
					logs.Error(err, "Failed to update DestinationRule.", destRuleLogKeysAndValues...)
					r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "UpdateDestinationRule", "Failed to update DestinationRule %s: %s", destRuleNamespacedName, err)
					return
				}
				logs.Info("DestinationRule updated.", destRuleLogKeysAndValues...)
				r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "UpdateDestinationRule", "Updated DestinationRule %s", destRuleNamespacedName)
				modified = true
			} else {
				logs.Info("DestinationRule spec is the same. Skipping update.", destRuleLogKeysAndValues...)
				r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "UpdateDestinationRule", "Skipping update DestinationRule %s", destRuleNamespacedName)
			}
		}
	}

	for _, virtualService := range virtualServices {
		virtualServiceLogKeysAndValues := []interface{}{"namespace", virtualService.Namespace, "name", virtualService.Name}
		virtualServiceNamespacedName := fmt.Sprintf("%s/%s", virtualService.Namespace, virtualService.Name)

		r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "GetVirtualService", "Getting VirtualService %s", virtualServiceNamespacedName)

		oldVirtualService := &istio.VirtualService{}
		err = r.Get(ctx, types.NamespacedName{Name: virtualService.Name, Namespace: virtualService.Namespace}, oldVirtualService)
		oldVirtualServiceIsNotFound := k8serrors.IsNotFound(err)
		if err != nil && !oldVirtualServiceIsNotFound {
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "GetVirtualService", "Failed to get VirtualService %s: %s", virtualServiceNamespacedName, err)
			logs.Error(err, "Failed to get VirtualService.", virtualServiceLogKeysAndValues...)
			return
		}

		if oldVirtualServiceIsNotFound {
			logs.Info("VirtualService not found. Creating a new one.", virtualServiceLogKeysAndValues...)

			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "CreateVirtualService", "Creating a new VirtualService %s", virtualServiceNamespacedName)
			err = r.Create(ctx, virtualService)
			if err != nil {
				logs.Error(err, "Failed to create VirtualService.", virtualServiceLogKeysAndValues...)
				r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "CreateVirtualService", "Failed to create VirtualService %s: %s", virtualServiceNamespacedName, err)
				return
			}
			logs.Info("VirtualService created.", virtualServiceLogKeysAndValues...)
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "CreateVirtualService", "Created VirtualService %s", virtualServiceNamespacedName)
			modified = true
		} else {
			logs.Info("VirtualService found.", virtualServiceLogKeysAndValues...)

			var patchResult *patch.PatchResult
			patchResult, err = patch.DefaultPatchMaker.Calculate(oldVirtualService, virtualService)
			if err != nil {
				logs.Error(err, "Failed to calculate patch.", virtualServiceLogKeysAndValues...)
				r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "CalculatePatch", "Failed to calculate patch for VirtualService %s: %s", virtualServiceNamespacedName, err)
				return
			}

			if !patchResult.IsEmpty() {
				logs.Info("VirtualService spec is different. Updating VirtualService.", virtualServiceLogKeysAndValues...)

				r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "UpdateVirtualService", "Updating VirtualService %s", virtualServiceNamespacedName)
				err = r.Update(ctx, virtualService)
				if err != nil {
					logs.Error(err, "Failed to update VirtualService.", virtualServiceLogKeysAndValues...)
					r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "UpdateVirtualService", "Failed to update VirtualService %s: %s", virtualServiceNamespacedName, err)
					return
				}
				logs.Info("VirtualService updated.", virtualServiceLogKeysAndValues...)
				r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "UpdateVirtualService", "Updated VirtualService %s", virtualServiceNamespacedName)
				modified = true
			} else {
				logs.Info("VirtualService spec is the same. Skipping update.", virtualServiceLogKeysAndValues...)
				r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "UpdateVirtualService", "Skipping update VirtualService %s", virtualServiceNamespacedName)
			}
		}
	}

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

func (r *BentoDeploymentReconciler) generateIngressHost(ctx context.Context, bentoDeployment *servingv1alpha3.BentoDeployment) (string, error) {
	return r.generateDefaultHostname(ctx, bentoDeployment)
}

func (r *BentoDeploymentReconciler) generateDefaultHostname(ctx context.Context, bentoDeployment *servingv1alpha3.BentoDeployment) (string, error) {
	ip, err := r.getIngressIp(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-yatai-%s.apps.yatai.dev", bentoDeployment.Name, bentoDeployment.Namespace, strings.ReplaceAll(ip, ".", "-")), nil
}

func (r *BentoDeploymentReconciler) generateIngresses(ctx context.Context, bentoDeployment *servingv1alpha3.BentoDeployment, bento *schemasv1.BentoFullSchema) (ingresses []*networkingv1.Ingress, err error) {
	kubeName := r.getKubeName(bentoDeployment, bento, nil)

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

	labels := r.getKubeLabels(bentoDeployment, nil)

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
		For(&servingv1alpha3.BentoDeployment{}).
		Owns(&appsv1.Deployment{}).
		Owns(&autoscalingv2beta2.HorizontalPodAutoscaler{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.Ingress{}).
		Owns(&istio.VirtualService{}).
		Owns(&istio.DestinationRule{}).
		WithEventFilter(pred).
		Complete(r)
}
