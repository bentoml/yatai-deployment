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
	// nolint: gosec
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	pep440version "github.com/aquasecurity/go-pep440-version"
	"github.com/prune998/docker-registry-client/registry"
	"github.com/sirupsen/logrus"
	"go.uber.org/multierr"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2beta2 "k8s.io/api/autoscaling/v2beta2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"

	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/banzaicloud/k8s-objectmatcher/patch"
	"github.com/huandu/xstrings"
	"github.com/pkg/errors"

	"github.com/bentoml/yatai-schemas/modelschemas"
	"github.com/bentoml/yatai-schemas/schemasv1"

	"github.com/bentoml/yatai-common/consts"
	"github.com/bentoml/yatai-common/sync/errsgroup"
	"github.com/bentoml/yatai-common/system"
	commonutils "github.com/bentoml/yatai-common/utils"

	commonconfig "github.com/bentoml/yatai-common/config"

	servingv1alpha3 "github.com/bentoml/yatai-deployment/apis/serving/v1alpha3"
	"github.com/bentoml/yatai-deployment/services"
	"github.com/bentoml/yatai-deployment/utils"
	"github.com/bentoml/yatai-deployment/version"
	yataiclient "github.com/bentoml/yatai-deployment/yatai-client"
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

	defer func() {
		if err == nil {
			return
		}
		logs.Error(err, "Failed to reconcile BentoDeployment.")
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "ReconcileError", "Failed to reconcile BentoDeployment: %v", err)
	}()

	yataiClient, clusterName, err := getYataiClient(ctx)
	if err != nil {
		err = errors.Wrap(err, "get yatai client")
		return
	}

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

	organization, err := yataiClient.GetOrganization(ctx)
	if err != nil {
		return
	}

	cluster, err := yataiClient.GetCluster(ctx, clusterName)
	if err != nil {
		return
	}

	bento, err := getBento()
	if err != nil {
		return
	}

	dockerRegistry, err := r.getDockerRegistry(ctx)
	if err != nil {
		err = errors.Wrap(err, "get docker registry")
		return
	}

	_, err = r.makeSureDockerRegcred(ctx, dockerRegistry, bentoDeployment.Namespace)
	if err != nil {
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "MakeSureDockerRegcred", "Failed to make sure docker registry credentials: %v", err)
		return
	}
	r.Recorder.Event(bentoDeployment, corev1.EventTypeNormal, "MakeSureDockerRegcred", "Successfully made sure docker registry credentials")

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
			// create or update runner deployment
			modified_, err = r.createOrUpdateOrDeleteDeployments(ctx, createOrUpdateOrDeleteDeploymentsOption{
				yataiClient:     yataiClient,
				bentoDeployment: bentoDeployment,
				organization:    organization,
				cluster:         cluster,
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
			modified_, err = r.createOrUpdateOrDeleteServices(ctx, createOrUpdateOrDeleteServicesOption{
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

	// create or update api-server deployment
	modified_, err := r.createOrUpdateOrDeleteDeployments(ctx, createOrUpdateOrDeleteDeploymentsOption{
		yataiClient:     yataiClient,
		bentoDeployment: bentoDeployment,
		organization:    organization,
		cluster:         cluster,
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
	modified_, err = r.createOrUpdateOrDeleteServices(ctx, createOrUpdateOrDeleteServicesOption{
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

	// create or update api-server ingresses
	modified_, err = r.createOrUpdateIngresses(ctx, createOrUpdateIngressOption{
		organization:    organization,
		bentoDeployment: bentoDeployment,
		bento:           bento,
	})
	if err != nil {
		return
	}

	if modified_ {
		modified = true
	}

	if modified {
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "GetYataiDeployment", "Fetching yatai deployment %s", bentoDeployment.Name)
		var oldYataiDeployment *schemasv1.DeploymentSchema
		oldYataiDeployment, err = yataiClient.GetDeployment(ctx, clusterName, bentoDeployment.Namespace, bentoDeployment.Name)
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
				Resources:                              runner.Resources,
				HPAConf:                                runner.Autoscaling,
				Envs:                                   &envs_,
				EnableStealingTrafficDebugMode:         &[]bool{checkIfIsStealingTrafficDebugModeEnabled(runner.Annotations)}[0],
				EnableDebugMode:                        &[]bool{checkIfIsDebugModeEnabled(runner.Annotations)}[0],
				EnableDebugPodReceiveProductionTraffic: &[]bool{checkIfIsDebugPodReceiveProductionTrafficEnabled(runner.Annotations)}[0],
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
				KubeResourceUid:                        string(bentoDeployment.UID),
				KubeResourceVersion:                    bentoDeployment.ResourceVersion,
				Resources:                              bentoDeployment.Spec.Resources,
				HPAConf:                                bentoDeployment.Spec.Autoscaling,
				Envs:                                   &envs,
				Runners:                                runners,
				EnableIngress:                          &bentoDeployment.Spec.Ingress.Enabled,
				EnableStealingTrafficDebugMode:         &[]bool{checkIfIsStealingTrafficDebugModeEnabled(bentoDeployment.Spec.Annotations)}[0],
				EnableDebugMode:                        &[]bool{checkIfIsDebugModeEnabled(bentoDeployment.Spec.Annotations)}[0],
				EnableDebugPodReceiveProductionTraffic: &[]bool{checkIfIsDebugPodReceiveProductionTrafficEnabled(bentoDeployment.Spec.Annotations)}[0],
			},
		})
		updateSchema := &schemasv1.UpdateDeploymentSchema{
			Targets:     deploymentTargets,
			DoNotDeploy: true,
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
				_, err = yataiClient.UpdateDeployment(ctx, clusterName, bentoDeployment.Namespace, bentoDeployment.Name, updateSchema)
				if err != nil {
					r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "UpdateYataiDeployment", "Failed to update yatai deployment %s: %s", bentoDeployment.Name, err)
					return
				}
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateYataiDeployment", "Updated yatai deployment %s", bentoDeployment.Name)
			}
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
	r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "Update", "All resources updated!")
	return
}

func getYataiClient(ctx context.Context) (yataiClient *yataiclient.YataiClient, clusterName string, err error) {
	restConfig := config.GetConfigOrDie()
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		err = errors.Wrap(err, "create kubernetes clientset")
		return
	}

	yataiConf, err := commonconfig.GetYataiConfig(ctx, clientset, consts.KubeNamespaceYataiDeploymentComponent, false)
	if err != nil {
		err = errors.Wrap(err, "get yatai config")
		return
	}

	yataiEndpoint := yataiConf.Endpoint
	yataiApiToken := yataiConf.ApiToken
	clusterName = yataiConf.ClusterName
	if clusterName == "" {
		clusterName = "default"
	}
	yataiClient = yataiclient.NewYataiClient(yataiEndpoint, fmt.Sprintf("%s:%s:%s", consts.YataiApiTokenPrefixYataiDeploymentOperator, clusterName, yataiApiToken))
	return
}

func (r *BentoDeploymentReconciler) getDockerRegistry(ctx context.Context) (dockerRegistry modelschemas.DockerRegistrySchema, err error) {
	dockerRegistryConfig, err := commonconfig.GetDockerRegistryConfig(ctx)
	if err != nil {
		err = errors.Wrap(err, "get docker registry")
		return
	}

	bentoRepositoryName := "yatai-bentos"
	modelRepositoryName := "yatai-models"
	if dockerRegistryConfig.BentoRepositoryName != "" {
		bentoRepositoryName = dockerRegistryConfig.BentoRepositoryName
	}
	if dockerRegistryConfig.ModelRepositoryName != "" {
		modelRepositoryName = dockerRegistryConfig.ModelRepositoryName
	}
	bentoRepositoryURI := fmt.Sprintf("%s/%s", strings.TrimRight(dockerRegistryConfig.Server, "/"), bentoRepositoryName)
	modelRepositoryURI := fmt.Sprintf("%s/%s", strings.TrimRight(dockerRegistryConfig.Server, "/"), modelRepositoryName)
	if strings.Contains(dockerRegistryConfig.Server, "docker.io") {
		bentoRepositoryURI = fmt.Sprintf("docker.io/%s", bentoRepositoryName)
		modelRepositoryURI = fmt.Sprintf("docker.io/%s", modelRepositoryName)
	}
	bentoRepositoryInClusterURI := bentoRepositoryURI
	modelRepositoryInClusterURI := modelRepositoryURI
	if dockerRegistryConfig.InClusterServer != "" {
		bentoRepositoryInClusterURI = fmt.Sprintf("%s/%s", strings.TrimRight(dockerRegistryConfig.InClusterServer, "/"), bentoRepositoryName)
		modelRepositoryInClusterURI = fmt.Sprintf("%s/%s", strings.TrimRight(dockerRegistryConfig.InClusterServer, "/"), modelRepositoryName)
		if strings.Contains(dockerRegistryConfig.InClusterServer, "docker.io") {
			bentoRepositoryInClusterURI = fmt.Sprintf("docker.io/%s", bentoRepositoryName)
			modelRepositoryInClusterURI = fmt.Sprintf("docker.io/%s", modelRepositoryName)
		}
	}
	dockerRegistry = modelschemas.DockerRegistrySchema{
		Server:                       dockerRegistryConfig.Server,
		Username:                     dockerRegistryConfig.Username,
		Password:                     dockerRegistryConfig.Password,
		Secure:                       dockerRegistryConfig.Secure,
		BentosRepositoryURI:          bentoRepositoryURI,
		BentosRepositoryURIInCluster: bentoRepositoryInClusterURI,
		ModelsRepositoryURI:          modelRepositoryURI,
		ModelsRepositoryURIInCluster: modelRepositoryInClusterURI,
	}

	return
}

type createOrUpdateOrDeleteDeploymentsOption struct {
	yataiClient     *yataiclient.YataiClient
	bentoDeployment *servingv1alpha3.BentoDeployment
	organization    *schemasv1.OrganizationFullSchema
	cluster         *schemasv1.ClusterFullSchema
	bento           *schemasv1.BentoFullSchema
	dockerRegistry  modelschemas.DockerRegistrySchema
	majorCluster    *schemasv1.ClusterFullSchema
	version         *schemasv1.VersionSchema
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
		dockerRegistry:                          opt.dockerRegistry,
		majorCluster:                            opt.majorCluster,
		version:                                 opt.version,
		runnerName:                              opt.runnerName,
		organization:                            opt.organization,
		cluster:                                 opt.cluster,
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

		status := r.generateStatus(opt.bentoDeployment, opt.bento)

		if !reflect.DeepEqual(status, opt.bentoDeployment.Status) {
			opt.bentoDeployment.Status = status
			err = r.Status().Update(ctx, opt.bentoDeployment)
			if err != nil {
				logs.Error(err, "Failed to update BentoDeployment status.")
				return
			}
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "GetYataiDeployment", "Fetching yatai deployment %s", opt.bentoDeployment.Name)
			_, err = opt.yataiClient.GetDeployment(ctx, opt.cluster.Name, opt.bentoDeployment.Namespace, opt.bentoDeployment.Name)
			isNotFound := err != nil && strings.Contains(strings.ToLower(err.Error()), "not found")
			if err != nil && !isNotFound {
				r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "GetYataiDeployment", "Failed to fetch yatai deployment %s: %s", opt.bentoDeployment.Name, err)
				return
			}
			err = nil
			if !isNotFound {
				r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "SyncYataiDeploymentStatus", "Syncing yatai deployment %s status: %s", opt.bentoDeployment.Name, err)
				_, err = opt.yataiClient.SyncDeploymentStatus(ctx, opt.cluster.Name, opt.bentoDeployment.Namespace, opt.bentoDeployment.Name)
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

func (r *BentoDeploymentReconciler) createOrUpdateHPA(ctx context.Context, bentoDeployment *servingv1alpha3.BentoDeployment, bento *schemasv1.BentoFullSchema, runnerName *string) (modified bool, err error) {
	logs := log.FromContext(ctx)

	hpa, err := r.generateHPA(bentoDeployment, bento, runnerName)
	if err != nil {
		return
	}

	logs = logs.WithValues("namespace", hpa.Namespace, "hpaName", hpa.Name)
	hpaNamespacedName := fmt.Sprintf("%s/%s", hpa.Namespace, hpa.Name)

	r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "GetHPA", "Getting HPA %s", hpaNamespacedName)

	oldHPA := &autoscalingv2beta2.HorizontalPodAutoscaler{}
	err = r.Get(ctx, types.NamespacedName{Name: hpa.Name, Namespace: hpa.Namespace}, oldHPA)
	oldHPAIsNotFound := k8serrors.IsNotFound(err)
	if err != nil && !oldHPAIsNotFound {
		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "GetHPA", "Failed to get HPA %s: %s", hpaNamespacedName, err)
		logs.Error(err, "Failed to get HPA.")
		return
	}

	if oldHPAIsNotFound {
		logs.Info("HPA not found. Creating a new one.")

		err = errors.Wrapf(patch.DefaultAnnotator.SetLastAppliedAnnotation(hpa), "set last applied annotation for hpa %s", hpa.Name)
		if err != nil {
			logs.Error(err, "Failed to set last applied annotation.")
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "SetLastAppliedAnnotation", "Failed to set last applied annotation for HPA %s: %s", hpaNamespacedName, err)
			return
		}

		r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "CreateHPA", "Creating a new HPA %s", hpaNamespacedName)
		err = r.Create(ctx, hpa)
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

		oldHPA.Status = hpa.Status
		var patchResult *patch.PatchResult
		patchResult, err = patch.DefaultPatchMaker.Calculate(oldHPA, hpa)
		if err != nil {
			logs.Error(err, "Failed to calculate patch.")
			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "CalculatePatch", "Failed to calculate patch for HPA %s: %s", hpaNamespacedName, err)
			return
		}

		if !patchResult.IsEmpty() {
			logs.Info(fmt.Sprintf("HPA spec is different. Updating HPA. The patch result is: %s", patchResult.String()))

			err = errors.Wrapf(patch.DefaultAnnotator.SetLastAppliedAnnotation(hpa), "set last applied annotation for hpa %s", hpa.Name)
			if err != nil {
				logs.Error(err, "Failed to set last applied annotation.")
				r.Recorder.Eventf(bentoDeployment, corev1.EventTypeWarning, "SetLastAppliedAnnotation", "Failed to set last applied annotation for HPA %s: %s", hpaNamespacedName, err)
				return
			}

			r.Recorder.Eventf(bentoDeployment, corev1.EventTypeNormal, "UpdateHPA", "Updating HPA %s", hpaNamespacedName)
			err = r.Update(ctx, hpa)
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

func getResourceAnnotations(bentoDeployment *servingv1alpha3.BentoDeployment, runnerName *string) map[string]string {
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

	return annotations[KubeAnnotationYataiEnableDebugMode] == consts.KubeLabelTrue
}

func checkIfIsStealingTrafficDebugModeEnabled(annotations map[string]string) bool {
	if annotations == nil {
		return false
	}

	return annotations[KubeAnnotationYataiEnableStealingTrafficDebugMode] == consts.KubeLabelTrue
}

func checkIfIsDebugPodReceiveProductionTrafficEnabled(annotations map[string]string) bool {
	if annotations == nil {
		return false
	}

	return annotations[KubeAnnotationYataiEnableDebugPodReceiveProductionTraffic] == consts.KubeLabelTrue
}

func checkIfContainsStealingTrafficDebugModeEnabled(bentoDeployment *servingv1alpha3.BentoDeployment) bool {
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
	bentoDeployment *servingv1alpha3.BentoDeployment
	bento           *schemasv1.BentoFullSchema
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
		err = nil
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
	bentoDeployment                         *servingv1alpha3.BentoDeployment
	bento                                   *schemasv1.BentoFullSchema
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
	organization    *schemasv1.OrganizationFullSchema
	bentoDeployment *servingv1alpha3.BentoDeployment
	bento           *schemasv1.BentoFullSchema
}

func (r *BentoDeploymentReconciler) createOrUpdateIngresses(ctx context.Context, opt createOrUpdateIngressOption) (modified bool, err error) {
	logs := log.FromContext(ctx)

	bentoDeployment := opt.bentoDeployment
	bento := opt.bento

	ingresses, err := r.generateIngresses(ctx, generateIngressesOption{
		organization:    opt.organization,
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

func (r *BentoDeploymentReconciler) generateStatus(bentoDeployment *servingv1alpha3.BentoDeployment, bento *schemasv1.BentoFullSchema) servingv1alpha3.BentoDeploymentStatus {
	labels := r.getKubeLabels(bentoDeployment, bento, nil)
	status := servingv1alpha3.BentoDeploymentStatus{
		PodSelector: labels,
	}
	return status
}

func hash(text string) string {
	// nolint: gosec
	hasher := md5.New()
	hasher.Write([]byte(text))
	return hex.EncodeToString(hasher.Sum(nil))
}

func (r *BentoDeploymentReconciler) getRunnerServiceName(bentoDeployment *servingv1alpha3.BentoDeployment, bento *schemasv1.BentoFullSchema, runnerName string, debug bool) string {
	hashStr := hash(fmt.Sprintf("%s:%s-%s", bento.Repository.Name, bento.Version, runnerName))
	var svcName string
	if debug {
		svcName = fmt.Sprintf("%s-runner-d-%s", bentoDeployment.Name, hashStr)
	} else {
		svcName = fmt.Sprintf("%s-runner-p-%s", bentoDeployment.Name, hashStr)
	}
	if len(svcName) > 63 {
		if debug {
			svcName = fmt.Sprintf("runner-d-%s", hash(fmt.Sprintf("%s-%s:%s-%s", bentoDeployment.Name, bento.Repository.Name, bento.Version, runnerName)))
		} else {
			svcName = fmt.Sprintf("runner-p-%s", hash(fmt.Sprintf("%s-%s:%s-%s", bentoDeployment.Name, bento.Repository.Name, bento.Version, runnerName)))
		}
	}
	return svcName
}

func (r *BentoDeploymentReconciler) getRunnerGenericServiceName(bentoDeployment *servingv1alpha3.BentoDeployment, bento *schemasv1.BentoFullSchema, runnerName string) string {
	hashStr := hash(fmt.Sprintf("%s:%s-%s", bento.Repository.Name, bento.Version, runnerName))
	var svcName string
	svcName = fmt.Sprintf("%s-runner-%s", bentoDeployment.Name, hashStr)
	if len(svcName) > 63 {
		svcName = fmt.Sprintf("runner-%s", hash(fmt.Sprintf("%s-%s:%s-%s", bentoDeployment.Name, bento.Repository.Name, bento.Version, runnerName)))
	}
	return svcName
}

func (r *BentoDeploymentReconciler) getKubeName(bentoDeployment *servingv1alpha3.BentoDeployment, bento *schemasv1.BentoFullSchema, runnerName *string, debug bool) string {
	if runnerName != nil && bento.Manifest != nil {
		for idx, runner := range bento.Manifest.Runners {
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

func (r *BentoDeploymentReconciler) getServiceName(bentoDeployment *servingv1alpha3.BentoDeployment, bento *schemasv1.BentoFullSchema, runnerName *string, debug bool) string {
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

func (r *BentoDeploymentReconciler) getGenericServiceName(bentoDeployment *servingv1alpha3.BentoDeployment, bento *schemasv1.BentoFullSchema, runnerName *string) string {
	var kubeName string
	if runnerName != nil {
		kubeName = r.getRunnerGenericServiceName(bentoDeployment, bento, *runnerName)
	} else {
		kubeName = r.getKubeName(bentoDeployment, bento, runnerName, false)
	}
	return kubeName
}

const (
	KubeValueNameSharedMemory                                 = "shared-memory"
	KubeAnnotationDeploymentStrategy                          = "yatai.ai/deployment-strategy"
	KubeAnnotationYataiEnableStealingTrafficDebugMode         = "yatai.ai/enable-stealing-traffic-debug-mode"
	KubeAnnotationYataiEnableDebugMode                        = "yatai.ai/enable-debug-mode"
	KubeAnnotationYataiEnableDebugPodReceiveProductionTraffic = "yatai.ai/enable-debug-pod-receive-production-traffic"
	KubeAnnotationYataiProxySidecarResourcesLimitsCpu         = "yatai.ai/proxy-sidecar-resources-limits-cpu"
	KubeAnnotationYataiProxySidecarResourcesLimitsMemory      = "yatai.ai/proxy-sidecar-resources-limits-memory"
	KubeAnnotationYataiProxySidecarResourcesRequestsCpu       = "yatai.ai/proxy-sidecar-resources-requests-cpu"
	KubeAnnotationYataiProxySidecarResourcesRequestsMemory    = "yatai.ai/proxy-sidecar-resources-requests-memory"
	DeploymentTargetTypeProduction                            = "production"
	DeploymentTargetTypeDebug                                 = "debug"
	ContainerPortNameHTTPProxy                                = "http-proxy"
	ServicePortNameHTTPNonProxy                               = "http-non-proxy"
	HeaderNameDebug                                           = "X-Yatai-Debug"
)

var (
	ServicePortHTTPNonProxy = consts.BentoServicePort + 1
)

func (r *BentoDeploymentReconciler) getKubeLabels(bentoDeployment *servingv1alpha3.BentoDeployment, bento *schemasv1.BentoFullSchema, runnerName *string) map[string]string {
	labels := map[string]string{
		consts.KubeLabelYataiBentoDeployment:           bentoDeployment.Name,
		consts.KubeLabelBentoRepository:                bento.Repository.Name,
		consts.KubeLabelBentoVersion:                   bento.Version,
		consts.KubeLabelYataiBentoDeploymentTargetType: DeploymentTargetTypeProduction,
		consts.KubeLabelCreator:                        "yatai-deployment",
	}
	if runnerName != nil {
		labels[consts.KubeLabelYataiBentoDeploymentComponentType] = consts.YataiBentoDeploymentComponentRunner
		labels[consts.KubeLabelYataiBentoDeploymentComponentName] = *runnerName
	} else {
		labels[consts.KubeLabelYataiBentoDeploymentComponentType] = consts.YataiBentoDeploymentComponentApiServer
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

func (r *BentoDeploymentReconciler) getKubeAnnotations(bentoDeployment *servingv1alpha3.BentoDeployment, bento *schemasv1.BentoFullSchema, runnerName *string) map[string]string {
	annotations := map[string]string{
		consts.KubeAnnotationBentoRepository: bento.Repository.Name,
		consts.KubeAnnotationBentoVersion:    bento.Version,
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
	bentoDeployment                         *servingv1alpha3.BentoDeployment
	bento                                   *schemasv1.BentoFullSchema
	yataiClient                             *yataiclient.YataiClient
	dockerRegistry                          modelschemas.DockerRegistrySchema
	majorCluster                            *schemasv1.ClusterFullSchema
	version                                 *schemasv1.VersionSchema
	runnerName                              *string
	organization                            *schemasv1.OrganizationFullSchema
	cluster                                 *schemasv1.ClusterFullSchema
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
		dockerRegistry:                          opt.dockerRegistry,
		majorCluster:                            opt.majorCluster,
		version:                                 opt.version,
		runnerName:                              opt.runnerName,
		organization:                            opt.organization,
		cluster:                                 opt.cluster,
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
		strategyType := appsv1.DeploymentStrategyType(strategyStr)
		if strategyType == appsv1.RecreateDeploymentStrategyType {
			strategy = appsv1.DeploymentStrategy{
				Type: appsv1.RecreateDeploymentStrategyType,
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
					consts.KubeLabelYataiSelector: kubeName,
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

func (r *BentoDeploymentReconciler) generateHPA(bentoDeployment *servingv1alpha3.BentoDeployment, bento *schemasv1.BentoFullSchema, runnerName *string) (hpa *autoscalingv2beta2.HorizontalPodAutoscaler, err error) {
	labels := r.getKubeLabels(bentoDeployment, bento, runnerName)

	annotations := r.getKubeAnnotations(bentoDeployment, bento, runnerName)

	kubeName := r.getKubeName(bentoDeployment, bento, runnerName, false)

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

	maxReplicas := commonutils.Int32Ptr(consts.HPADefaultMaxReplicas)
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

	minReplicas := commonutils.Int32Ptr(2)
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
	if err != nil {
		err = errors.Wrapf(err, "set hpa %s controller reference", kubeHpa.Name)
		return
	}

	return kubeHpa, err
}

func (r *BentoDeploymentReconciler) makeSureDockerRegcred(ctx context.Context, dockerRegistry modelschemas.DockerRegistrySchema, namespace string) (secret *corev1.Secret, err error) {
	if dockerRegistry.Username == "" {
		return
	}
	secret = &corev1.Secret{}
	err = r.Get(ctx, types.NamespacedName{Name: consts.KubeSecretNameRegcred, Namespace: namespace}, secret)
	isNotFound := k8serrors.IsNotFound(err)
	if err != nil && !isNotFound {
		return
	}
	dockerConfig := struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}{
		Auths: map[string]struct {
			Auth string `json:"auth"`
		}{
			dockerRegistry.Server: {
				Auth: base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", dockerRegistry.Username, dockerRegistry.Password))),
			},
		},
	}
	var dockerConfigContent []byte
	dockerConfigContent, err = json.Marshal(&dockerConfig)
	if err != nil {
		return
	}
	if isNotFound {
		secret = &corev1.Secret{
			Type: corev1.SecretTypeDockerConfigJson,
			ObjectMeta: metav1.ObjectMeta{
				Name:      consts.KubeSecretNameRegcred,
				Namespace: namespace,
			},
			Data: map[string][]byte{
				".dockerconfigjson": dockerConfigContent,
			},
		}
		err = r.Create(ctx, secret)
		if err != nil {
			return
		}
	} else {
		secret.Data[".dockerconfigjson"] = dockerConfigContent
		err = r.Update(ctx, secret)
		if err != nil {
			return
		}
	}
	return
}

func checkImageExists(dockerRegistry modelschemas.DockerRegistrySchema, imageName string) (bool, error) {
	if services.UsingAWSECRWithIAMRole() {
		return services.CheckECRImageExists(imageName)
	}

	server, _, imageName := xstrings.Partition(imageName, "/")
	if strings.Contains(server, "docker.io") {
		server = "index.docker.io"
	}
	if dockerRegistry.Secure {
		server = fmt.Sprintf("https://%s", server)
	} else {
		server = fmt.Sprintf("http://%s", server)
	}
	hub, err := registry.New(server, dockerRegistry.Username, dockerRegistry.Password, logrus.Debugf)
	if err != nil {
		err = errors.Wrapf(err, "create docker registry client for %s", server)
		return false, err
	}
	imageName, _, tag := xstrings.LastPartition(imageName, ":")
	tags, err := hub.Tags(imageName)
	isNotFound := err != nil && strings.Contains(err.Error(), "404")
	if isNotFound {
		return false, nil
	}
	if err != nil {
		err = errors.Wrapf(err, "get tags for docker image %s", imageName)
		return false, err
	}
	for _, tag_ := range tags {
		if tag_ == tag {
			return true, nil
		}
	}
	return false, nil
}

func GetBentoImageName(dockerRegistry modelschemas.DockerRegistrySchema, bento *schemasv1.BentoWithRepositorySchema, inCluster bool) string {
	var imageName string
	if inCluster {
		imageName = fmt.Sprintf("%s:yatai.%s.%s", dockerRegistry.BentosRepositoryURIInCluster, bento.Repository.Name, bento.Version)
	} else {
		imageName = fmt.Sprintf("%s:yatai.%s.%s", dockerRegistry.BentosRepositoryURI, bento.Repository.Name, bento.Version)
	}
	return imageName
}

func GetModelImageName(dockerRegistry modelschemas.DockerRegistrySchema, model *schemasv1.ModelWithRepositorySchema, inCluster bool) string {
	var imageName string
	if inCluster {
		imageName = fmt.Sprintf("%s:yatai.%s.%s", dockerRegistry.ModelsRepositoryURIInCluster, model.Repository.Name, model.Version)
	} else {
		imageName = fmt.Sprintf("%s:yatai.%s.%s", dockerRegistry.ModelsRepositoryURI, model.Repository.Name, model.Version)
	}
	return imageName
}

// wait image builder pod complete
func (r *BentoDeploymentReconciler) waitImageBuilderPodComplete(ctx context.Context, namespace, podName string) (modelschemas.ImageBuildStatus, error) {
	logs := log.Log.WithValues("func", "waitImageBuilderPodComplete", "namespace", namespace, "pod", podName)

	// Interval to poll for objects.
	pollInterval := 3 * time.Second
	// How long to wait for objects.
	waitTimeout := 60 * time.Minute

	imageBuildStatus := modelschemas.ImageBuildStatusPending

	restConf := config.GetConfigOrDie()
	cliset, err := kubernetes.NewForConfig(restConf)
	if err != nil {
		err = errors.Wrapf(err, "create kubernetes client for %s", restConf.Host)
		return imageBuildStatus, err
	}

	podCli := cliset.CoreV1().Pods(namespace)

	// Wait for the image builder pod to be Complete.
	if err := wait.PollImmediate(pollInterval, waitTimeout, func() (done bool, err error) {
		pod, err_ := podCli.Get(ctx, podName, metav1.GetOptions{})
		if err_ != nil {
			logs.Error(err_, "failed to get pod")
			return true, err_
		}
		if pod.Status.Phase == corev1.PodSucceeded {
			imageBuildStatus = modelschemas.ImageBuildStatusSuccess
			return true, nil
		}
		if pod.Status.Phase == corev1.PodFailed {
			imageBuildStatus = modelschemas.ImageBuildStatusFailed
			return true, errors.Errorf("pod %s in namespace %s failed", pod.Name, pod.Namespace)
		}
		if pod.Status.Phase == corev1.PodUnknown {
			imageBuildStatus = modelschemas.ImageBuildStatusFailed
			return true, errors.Errorf("pod %s in namespace %s is in unknown state", pod.Name, pod.Namespace)
		}
		if pod.Status.Phase == corev1.PodRunning {
			imageBuildStatus = modelschemas.ImageBuildStatusBuilding
		}
		return false, nil
	}); err != nil {
		err = errors.Wrapf(err, "failed to wait for pod %s in namespace %s to be ready", podName, namespace)
		return imageBuildStatus, err
	}
	return imageBuildStatus, nil
}

type generatePodTemplateSpecOption struct {
	bentoDeployment                         *servingv1alpha3.BentoDeployment
	bento                                   *schemasv1.BentoFullSchema
	yataiClient                             *yataiclient.YataiClient
	dockerRegistry                          modelschemas.DockerRegistrySchema
	majorCluster                            *schemasv1.ClusterFullSchema
	version                                 *schemasv1.VersionSchema
	runnerName                              *string
	organization                            *schemasv1.OrganizationFullSchema
	cluster                                 *schemasv1.ClusterFullSchema
	isStealingTrafficDebugModeEnabled       bool
	containsStealingTrafficDebugModeEnabled bool
}

func (r *BentoDeploymentReconciler) generatePodTemplateSpec(ctx context.Context, opt generatePodTemplateSpecOption) (podTemplateSpec *corev1.PodTemplateSpec, err error) {
	podLabels := r.getKubeLabels(opt.bentoDeployment, opt.bento, opt.runnerName)
	if opt.runnerName != nil {
		podLabels[consts.KubeLabelBentoRepository] = opt.bento.Repository.Name
		podLabels[consts.KubeLabelBentoVersion] = opt.bento.Version
	}
	if opt.isStealingTrafficDebugModeEnabled {
		podLabels[consts.KubeLabelYataiBentoDeploymentTargetType] = DeploymentTargetTypeDebug
	}

	podAnnotations := r.getKubeAnnotations(opt.bentoDeployment, opt.bento, opt.runnerName)

	kubeName := r.getKubeName(opt.bentoDeployment, opt.bento, opt.runnerName, opt.isStealingTrafficDebugModeEnabled)

	containerPort := consts.BentoServicePort
	var envs []corev1.EnvVar
	envsSeen := make(map[string]struct{})

	var resourceAnnotations map[string]string
	var specEnvs *[]modelschemas.LabelItemSchema
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
		envs = make([]corev1.EnvVar, 0, len(*specEnvs)+1)

		for _, env := range *specEnvs {
			if _, ok := envsSeen[env.Key]; ok {
				continue
			}
			if env.Key == consts.EnvBentoServicePort {
				// nolint: gosec
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

	defaultEnvs := []corev1.EnvVar{
		{
			Name:  consts.EnvBentoServicePort,
			Value: fmt.Sprintf("%d", containerPort),
		},
		{
			Name:  consts.EnvYataiVersion,
			Value: fmt.Sprintf("%s-%s", opt.version.Version, opt.version.GitCommit),
		},
		{
			Name:  consts.EnvYataiOrgUID,
			Value: opt.organization.Uid,
		},
		{
			Name:  consts.EnvYataiDeploymentUID,
			Value: string(opt.bentoDeployment.UID),
		},
		{
			Name:  consts.EnvYataiClusterUID,
			Value: opt.cluster.Uid,
		},
		{
			Name:  consts.EnvYataiBentoDeploymentName,
			Value: opt.bentoDeployment.Name,
		},
		{
			Name:  consts.EnvYataiBentoDeploymentNamespace,
			Value: opt.bentoDeployment.Namespace,
		},
	}

	for _, env := range defaultEnvs {
		if _, ok := envsSeen[env.Name]; !ok {
			envs = append(envs, env)
		}
	}

	livenessProbe := &corev1.Probe{
		InitialDelaySeconds: 10,
		TimeoutSeconds:      20,
		FailureThreshold:    6,
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/livez",
				Port: intstr.FromString(consts.BentoContainerPortName),
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
				Port: intstr.FromString(consts.BentoContainerPortName),
			},
		},
	}

	volumes := make([]corev1.Volume, 0)
	volumeMounts := make([]corev1.VolumeMount, 0)

	// prepare images
	var eg errsgroup.Group
	eg.Go(func() error {
		bentoTag := fmt.Sprintf("%s:%s", opt.bento.Repository.Name, opt.bento.Version)
		imageName := GetBentoImageName(opt.dockerRegistry, &opt.bento.BentoWithRepositorySchema, true)
		r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "CheckImageExists", "Checking image %s exists", imageName)
		imageExists, err := checkImageExists(opt.dockerRegistry, imageName)
		if err != nil {
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "CheckImageExists", "Failed to check image %s exists: %v", imageName, err)
			err = errors.Wrapf(err, "failed to check image %s exists for bento %s", imageName, bentoTag)
			return err
		}
		if imageExists {
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "CheckImageExists", "Image %s exists", imageName)
			return nil
		}
		r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "CheckImageExists", "Image %s does not exist", imageName)
		r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "BentoImageBuilder", "Bento image builder is starting")
		pod, err := services.ImageBuilderService.CreateImageBuilderPod(ctx, services.CreateImageBuilderPodOption{
			ImageName:        imageName,
			Bento:            &opt.bento.BentoWithRepositorySchema,
			YataiClient:      opt.yataiClient,
			DockerRegistry:   opt.dockerRegistry,
			RecreateIfFailed: true,
		})
		if err != nil {
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "BentoImageBuilder", "Failed to create image builder pod: %v", err)
			err = errors.Wrapf(err, "failed to create image builder pod for bento %s", bentoTag)
			return err
		}
		r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "BentoImageBuilder", "Building image %s..., the image builder pod is %s in namespace %s", imageName, pod.Name, pod.Namespace)

		_, err = r.waitImageBuilderPodComplete(ctx, pod.Namespace, pod.Name)

		if err != nil {
			r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeWarning, "BentoImageBuilder", "Failed to build image %s, the image builder pod is %s in namespace %s has an error: %s", imageName, pod.Name, pod.Namespace, err.Error())
			err = errors.Wrapf(err, "failed to build image %s for bento %s", imageName, bentoTag)
			return err
		}

		r.Recorder.Eventf(opt.bentoDeployment, corev1.EventTypeNormal, "BentoImageBuilder", "Image %s has been built successfully", imageName)

		return nil
	})

	err = eg.Wait()
	if err != nil {
		return
	}

	args := make([]string, 0)

	isOldVersion := false
	if opt.bento.Manifest != nil && opt.bento.Manifest.BentomlVersion != "" {
		var currentVersion pep440version.Version
		currentVersion, err = pep440version.Parse(opt.bento.Manifest.BentomlVersion)
		if err != nil {
			err = errors.Wrapf(err, "invalid bentoml version %s", opt.bento.Manifest.BentomlVersion)
			return
		}
		var targetVersion pep440version.Version
		targetVersion, err = pep440version.Parse("1.0.0a7")
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
			readinessProbeUrls := make([]string, 0)
			livenessProbeUrls := make([]string, 0)
			readinessProbeUrls = append(readinessProbeUrls, fmt.Sprintf("http://localhost:%d/readyz", containerPort))
			livenessProbeUrls = append(livenessProbeUrls, fmt.Sprintf("http://localhost:%d/healthz", containerPort))
			// python -m bentoml._internal.server.cli.api_server  iris_classifier:ohzovcfvvseu3lg6 tcp://127.0.0.1:8000 --runner-map '{"iris_clf": "tcp://127.0.0.1:8001"}' --working-dir .
			runnerMap := make(map[string]string, len(opt.bento.Manifest.Runners))
			for _, runner := range opt.bento.Manifest.Runners {
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
				runnerMap[runner.Name] = fmt.Sprintf("tcp://%s:%d", runnerServiceName, consts.BentoServicePort)
				readinessProbeUrls = append(readinessProbeUrls, fmt.Sprintf("http://%s:%d/readyz", runnerServiceName, consts.BentoServicePort))
				livenessProbeUrls = append(livenessProbeUrls, fmt.Sprintf("http://%s:%d/healthz", runnerServiceName, consts.BentoServicePort))
			}

			livenessProbePythonCommandPieces := make([]string, 0, len(opt.bento.Manifest.Runners)+1)
			for _, url_ := range livenessProbeUrls {
				livenessProbePythonCommandPieces = append(livenessProbePythonCommandPieces, fmt.Sprintf("urlopen('%s')", url_))
			}

			readinessProbePythonCommandPieces := make([]string, 0, len(opt.bento.Manifest.Runners)+1)
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

	imageName := GetBentoImageName(opt.dockerRegistry, &opt.bento.BentoWithRepositorySchema, false)

	containers := make([]corev1.Container, 0, 2)

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
		VolumeMounts:   volumeMounts,
		Ports: []corev1.ContainerPort{
			{
				Protocol:      corev1.ProtocolTCP,
				Name:          consts.BentoContainerPortName,
				ContainerPort: int32(containerPort), // nolint: gosec
			},
		},
		SecurityContext: &corev1.SecurityContext{
			Capabilities: &corev1.Capabilities{
				Add: []corev1.Capability{"SYS_PTRACE"},
			},
		},
	}

	if resourceAnnotations["yatai.ai/enable-container-privileged"] == consts.KubeLabelTrue {
		if container.SecurityContext == nil {
			container.SecurityContext = &corev1.SecurityContext{}
		}
		container.SecurityContext.Privileged = &[]bool{true}[0]
	}

	if resourceAnnotations["yatai.ai/enable-container-ptrace"] == consts.KubeLabelTrue {
		if container.SecurityContext == nil {
			container.SecurityContext = &corev1.SecurityContext{}
		}
		container.SecurityContext.Capabilities = &corev1.Capabilities{
			Add: []corev1.Capability{"SYS_PTRACE"},
		}
	}

	if resourceAnnotations["yatai.ai/run-container-as-root"] == consts.KubeLabelTrue {
		if container.SecurityContext == nil {
			container.SecurityContext = &corev1.SecurityContext{}
		}
		container.SecurityContext.RunAsUser = &[]int64{0}[0]
	}

	containers = append(containers, container)

	metricsPort := containerPort + 1

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
				Value: fmt.Sprintf("BENTOML_%s_", opt.bento.Repository.Name),
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
	})

	if opt.runnerName == nil {
		proxyPort := metricsPort + 1
		proxyResourcesRequestsCpuStr := resourceAnnotations[KubeAnnotationYataiProxySidecarResourcesRequestsCpu]
		if proxyResourcesRequestsCpuStr == "" {
			proxyResourcesRequestsCpuStr = "100m"
		}
		var proxyResourcesRequestsCpu resource.Quantity
		proxyResourcesRequestsCpu, err = resource.ParseQuantity(proxyResourcesRequestsCpuStr)
		if err != nil {
			err = errors.Wrapf(err, "failed to parse proxy sidecar resources requests cpu: %s", proxyResourcesRequestsCpuStr)
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
		proxyResourcesLimitsCpuStr := resourceAnnotations[KubeAnnotationYataiProxySidecarResourcesLimitsCpu]
		if proxyResourcesLimitsCpuStr == "" {
			proxyResourcesLimitsCpuStr = "300m"
		}
		var proxyResourcesLimitsCpu resource.Quantity
		proxyResourcesLimitsCpu, err = resource.ParseQuantity(proxyResourcesLimitsCpuStr)
		if err != nil {
			err = errors.Wrapf(err, "failed to parse proxy sidecar resources limits cpu: %s", proxyResourcesLimitsCpuStr)
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
				DebugHeaderValue:        consts.KubeLabelTrue,
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
				DebugHeaderValue:        consts.KubeLabelTrue,
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
		containers = append(containers, corev1.Container{
			Name:  "proxy",
			Image: "quay.io/bentoml/envoy:1.24.1",
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
					corev1.ResourceCPU:    proxyResourcesRequestsCpu,
					corev1.ResourceMemory: proxyResourcesRequestsMemory,
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    proxyResourcesLimitsCpu,
					corev1.ResourceMemory: proxyResourcesLimitsMemory,
				},
			},
		})
	}

	debuggerImage := "quay.io/bentoml/bento-debugger:0.0.5"
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

	podLabels[consts.KubeLabelYataiSelector] = kubeName

	podSpec := corev1.PodSpec{
		Containers: containers,
		Volumes:    volumes,
	}

	if opt.dockerRegistry.Username != "" {
		podSpec.ImagePullSecrets = []corev1.LocalObjectReference{
			{
				Name: consts.KubeSecretNameRegcred,
			},
		}
	}

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
	}

	if resourceAnnotations["yatai.ai/enable-host-ipc"] == consts.KubeLabelTrue {
		podSpec.HostIPC = true
	}

	if resourceAnnotations["yatai.ai/enable-host-network"] == consts.KubeLabelTrue {
		podSpec.HostNetwork = true
	}

	if resourceAnnotations["yatai.ai/enable-host-pid"] == consts.KubeLabelTrue {
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
			currentResources.Limits[consts.KubeResourceGPUNvidia] = q
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
	bentoDeployment                         *servingv1alpha3.BentoDeployment
	bento                                   *schemasv1.BentoFullSchema
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
		selector[consts.KubeLabelYataiBentoDeploymentTargetType] = DeploymentTargetTypeDebug
	}

	targetPort := intstr.FromString(consts.BentoContainerPortName)
	if opt.runnerName == nil {
		if opt.isGenericService {
			delete(selector, consts.KubeLabelYataiBentoDeploymentTargetType)
			if opt.containsStealingTrafficDebugModeEnabled {
				targetPort = intstr.FromString(ContainerPortNameHTTPProxy)
			}
		}
	} else {
		if opt.isGenericService && opt.isDebugPodReceiveProductionTraffic {
			delete(selector, consts.KubeLabelYataiBentoDeploymentTargetType)
		}
	}

	spec := corev1.ServiceSpec{
		Selector: selector,
		Ports: []corev1.ServicePort{
			{
				Name:       consts.BentoServicePortName,
				Port:       consts.BentoServicePort,
				TargetPort: targetPort,
				Protocol:   corev1.ProtocolTCP,
			},
			{
				Name:       ServicePortNameHTTPNonProxy,
				Port:       int32(ServicePortHTTPNonProxy),
				TargetPort: intstr.FromString(consts.BentoContainerPortName),
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

func (r *BentoDeploymentReconciler) generateIngressHost(ctx context.Context, bentoDeployment *servingv1alpha3.BentoDeployment) (string, error) {
	return r.generateDefaultHostname(ctx, bentoDeployment)
}

func (r *BentoDeploymentReconciler) generateDefaultHostname(ctx context.Context, bentoDeployment *servingv1alpha3.BentoDeployment) (string, error) {
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

type IngressConfig struct {
	ClassName   *string
	Annotations map[string]string
	Path        string
	PathType    networkingv1.PathType
}

func GetIngressConfig(ctx context.Context, cliset *kubernetes.Clientset) (ingressConfig *IngressConfig, err error) {
	configMap, err := system.GetNetworkConfigConfigMap(ctx, cliset)
	if err != nil {
		err = errors.Wrapf(err, "failed to get configmap %s", consts.KubeConfigMapNameNetworkConfig)
		return
	}

	var className *string

	className_ := strings.TrimSpace(configMap.Data[consts.KubeConfigMapKeyNetworkConfigIngressClass])
	if className_ != "" {
		className = &className_
	}

	annotations := make(map[string]string)

	annotations_ := strings.TrimSpace(configMap.Data[consts.KubeConfigMapKeyNetworkConfigIngressAnnotations])
	if annotations_ != "" {
		err = json.Unmarshal([]byte(annotations_), &annotations)
		if err != nil {
			err = errors.Wrapf(err, "failed to json unmarshal %s in configmap %s: %s", consts.KubeConfigMapKeyNetworkConfigIngressAnnotations, consts.KubeConfigMapNameNetworkConfig, annotations_)
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

	ingressConfig = &IngressConfig{
		ClassName:   className,
		Annotations: annotations,
		Path:        path,
		PathType:    pathType,
	}

	return
}

type generateIngressesOption struct {
	organization    *schemasv1.OrganizationFullSchema
	bentoDeployment *servingv1alpha3.BentoDeployment
	bento           *schemasv1.BentoFullSchema
}

func (r *BentoDeploymentReconciler) generateIngresses(ctx context.Context, opt generateIngressesOption) (ingresses []*networkingv1.Ingress, err error) {
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

	tag := fmt.Sprintf("%s:%s", bento.Repository.Name, bento.Version)

	annotations["nginx.ingress.kubernetes.io/configuration-snippet"] = fmt.Sprintf(`
more_set_headers "X-Powered-By: Yatai";
more_set_headers "X-Yatai-Org-Name: %s";
more_set_headers "X-Yatai-Bento: %s";
`, opt.organization.Name, tag)

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
												Name: consts.BentoServicePortName,
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

	bentoDeploymentNamespaces := GetBentoDeploymentNamespaces()

	for _, bentoDeploymentNamespace := range bentoDeploymentNamespaces {
		serviceList := &corev1.ServiceList{}
		serviceListOpts := []client.ListOption{
			client.HasLabels{consts.KubeLabelYataiBentoDeploymentRunner},
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

func (r *BentoDeploymentReconciler) doBuildBentoImages() (err error) {
	logs := log.Log.WithValues("func", "doBuildBentoImages")
	ctx, cancel := context.WithTimeout(context.TODO(), time.Minute*90)
	defer cancel()

	logs.Info("getting yatai client")
	yataiClient, _, err := getYataiClient(ctx)
	if err != nil {
		err = errors.Wrap(err, "get yatai client")
		return
	}

	logs.Info("getting docker registry")
	dockerRegistry, err := r.getDockerRegistry(ctx)
	if err != nil {
		err = errors.Wrap(err, "get docker registry")
		return
	}

	restConfig := config.GetConfigOrDie()
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		err = errors.Wrap(err, "create kubernetes clientset")
		return
	}

	cmCli := clientset.CoreV1().ConfigMaps(consts.KubeNamespaceYataiBentoImageBuilder)

	imageBuilderMetaCmName := "image-builder-meta"
	lastSyncedCreatedAtKey := "last-synced-created-at"

	oldImageBuilderMetaCm, err := cmCli.Get(ctx, imageBuilderMetaCmName, metav1.GetOptions{})
	imageBuilderMetaCmIsNotFound := k8serrors.IsNotFound(err)
	if err != nil && !imageBuilderMetaCmIsNotFound {
		err = errors.Wrapf(err, "get config map %s", imageBuilderMetaCmName)
		return
	}

	var lastSyncedCreatedAt *time.Time
	var lastSyncedCreatedAtMu sync.Mutex

	if !imageBuilderMetaCmIsNotFound {
		lastSyncedCreatedAtStr := oldImageBuilderMetaCm.Data[lastSyncedCreatedAtKey]
		if lastSyncedCreatedAtStr != "" {
			var lastSyncedCreatedAt_ time.Time
			lastSyncedCreatedAt_, err = time.Parse(time.RFC3339, lastSyncedCreatedAtStr)
			if err != nil {
				err = errors.Wrapf(err, "parse last synced created at %s", lastSyncedCreatedAtStr)
				return
			}
			lastSyncedCreatedAt = &lastSyncedCreatedAt_
		}
	}

	start := 0
	count := 20

	logs.Info("listing bentos from yatai")
	bentos := make([]*schemasv1.BentoWithRepositorySchema, 0)
out:
	for {
		var bentos_ *schemasv1.BentoWithRepositoryListSchema
		bentos_, err = yataiClient.ListBentos(ctx, schemasv1.ListQuerySchema{
			Start: uint(start),
			Count: uint(count),
			Q:     "sort:created_at-desc",
		})
		if err != nil {
			err = errors.Wrap(err, "list bentos")
			return
		}
		if lastSyncedCreatedAt != nil {
			for _, bento := range bentos_.Items {
				if bento.CreatedAt.Before(*lastSyncedCreatedAt) {
					break out
				}
				bentos = append(bentos, bento)
			}
		} else {
			bentos = append(bentos, bentos_.Items...)
		}
		start += count
		if start >= int(bentos_.Total) {
			break
		}
	}

	logs.Info(fmt.Sprintf("found %d bentos need to build image", len(bentos)))

	var eg errsgroup.Group

	eg.SetPoolSize(10)

	for _, bento := range bentos {
		if bento.UploadFinishedAt == nil {
			continue
		}
		bento := bento
		eg.Go(func() error {
			bentoTag := fmt.Sprintf("%s:%s", bento.Repository.Name, bento.Version)
			logs := logs.WithValues("bentoTag", bentoTag)
			imageName := GetBentoImageName(dockerRegistry, bento, true)
			logs.Info(fmt.Sprintf("checking image %s exists", imageName))
			imageExists, err := checkImageExists(dockerRegistry, imageName)
			if err != nil {
				err = errors.Wrapf(err, "failed to check image %s exists for bento %s", imageName, bentoTag)
				return err
			}
			if imageExists {
				logs.Info(fmt.Sprintf("image %s exists", imageName))
				return nil
			}
			logs.Info(fmt.Sprintf("image %s does not exist, creating image builder pod to build it", imageName))
			_, err = services.ImageBuilderService.CreateImageBuilderPod(ctx, services.CreateImageBuilderPodOption{
				ImageName:      imageName,
				Bento:          bento,
				YataiClient:    yataiClient,
				DockerRegistry: dockerRegistry,
			})
			if err != nil {
				err = errors.Wrapf(err, "failed to create image builder pod for bento %s", bentoTag)
				return err
			}

			logs.Info("image builder pod created")

			func() {
				lastSyncedCreatedAtMu.Lock()
				defer lastSyncedCreatedAtMu.Unlock()

				if lastSyncedCreatedAt == nil || bento.CreatedAt.After(*lastSyncedCreatedAt) {
					lastSyncedCreatedAt = &bento.CreatedAt
				}
			}()

			logs.Info(fmt.Sprintf("image %s built successfully", imageName))

			return nil
		})
	}

	err = eg.Wait()

	lastSyncedCreatedAtMu.Lock()
	defer lastSyncedCreatedAtMu.Unlock()

	if lastSyncedCreatedAt != nil {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      imageBuilderMetaCmName,
				Namespace: consts.KubeNamespaceYataiBentoImageBuilder,
			},
		}
		if !imageBuilderMetaCmIsNotFound {
			_, err = cmCli.Patch(ctx, cm.Name, types.MergePatchType, []byte(fmt.Sprintf(`{"data":{"%s":"%s"}}`, lastSyncedCreatedAtKey, lastSyncedCreatedAt.Format(time.RFC3339))), metav1.PatchOptions{})
			err = multierr.Append(err, errors.Wrapf(err, "update config map %s", imageBuilderMetaCmName))
		} else {
			_, err = cmCli.Create(ctx, cm, metav1.CreateOptions{})
			err = multierr.Append(err, errors.Wrapf(err, "create config map %s", imageBuilderMetaCmName))
		}
	}

	return err
}

func (r *BentoDeploymentReconciler) buildBentoImages() {
	logs := log.Log.WithValues("func", "buildBentoImages")
	err := r.doBuildBentoImages()
	if err != nil {
		logs.Error(err, "buildBentoImages")
	}
	ticker := time.NewTicker(time.Second * 30)
	for range ticker.C {
		err := r.doBuildBentoImages()
		if err != nil {
			logs.Error(err, "buildBentoImages")
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

	_, err = yataiClient.RegisterYataiComponent(ctx, clusterName, &schemasv1.RegisterYataiComponentSchema{
		Name:          modelschemas.YataiComponentNameDeployment,
		KubeNamespace: consts.KubeNamespaceYataiDeploymentComponent,
		Version:       version.Version,
		SelectorLabels: map[string]string{
			"app.kubernetes.io/name": "yatai-deployment",
		},
		Manifest: &modelschemas.YataiComponentManifestSchema{
			SelectorLabels: map[string]string{
				"app.kubernetes.io/name": "yatai-deployment",
			},
			LatestCRDVersion: "v1alpha3",
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

func GetBentoDeploymentNamespaces() []string {
	bentoDeploymentNamespacesStr := os.Getenv("BENTO_DEPLOYMENT_NAMESPACES")
	pieces := strings.Split(bentoDeploymentNamespacesStr, ",")
	bentoDeploymentNamespaces := make([]string, 0, len(pieces))
	for _, piece := range pieces {
		bentoDeploymentNamespaces = append(bentoDeploymentNamespaces, strings.TrimSpace(piece))
	}
	return bentoDeploymentNamespaces
}

// SetupWithManager sets up the controller with the Manager.
func (r *BentoDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	logs := log.Log.WithValues("func", "SetupWithManager")
	version.Print()

	if os.Getenv("DISABLE_AUTOMATE_BENTO_IMAGE_BUILDER") != consts.KubeLabelTrue {
		go r.buildBentoImages()
	} else {
		logs.Info("auto image builder is disabled")
	}

	if os.Getenv("DISABLE_CLEANUP_ABANDONED_RUNNER_SERVICES") != consts.KubeLabelTrue {
		go r.cleanUpAbandonedRunnerServices()
	} else {
		logs.Info("cleanup abandoned runner services is disabled")
	}

	if os.Getenv("DISABLE_YATAI_COMPONENT_REGISTRATION") != consts.KubeLabelTrue {
		go r.registerYataiComponent()
	} else {
		logs.Info("yatai component registration is disabled")
	}

	pred := predicate.GenerationChangedPredicate{}
	return ctrl.NewControllerManagedBy(mgr).
		For(&servingv1alpha3.BentoDeployment{}).
		Owns(&appsv1.Deployment{}).
		Owns(&autoscalingv2beta2.HorizontalPodAutoscaler{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.Ingress{}).
		WithEventFilter(pred).
		Complete(r)
}
