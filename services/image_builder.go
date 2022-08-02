package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/banzaicloud/k8s-objectmatcher/patch"
	"github.com/huandu/xstrings"
	"github.com/iancoleman/strcase"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	commonconfig "github.com/bentoml/yatai-common/config"
	"github.com/bentoml/yatai-common/consts"
	"github.com/bentoml/yatai-common/k8sutils"
	"github.com/bentoml/yatai-common/utils"
	"github.com/bentoml/yatai-schemas/modelschemas"
	"github.com/bentoml/yatai-schemas/schemasv1"
)

type imageBuilderService struct{}

var ImageBuilderService = &imageBuilderService{}

func MakeSureDockerConfigSecret(ctx context.Context, kubeCli *kubernetes.Clientset, namespace string, dockerRegistry modelschemas.DockerRegistrySchema) (dockerConfigSecret *corev1.Secret, err error) {
	dockerConfigCMKubeName := "docker-config"
	dockerConfigObj := struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths,omitempty"`
	}{}

	if dockerRegistry.Username != "" {
		dockerConfigObj.Auths = map[string]struct {
			Auth string `json:"auth"`
		}{
			dockerRegistry.Server: {
				Auth: base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", dockerRegistry.Username, dockerRegistry.Password))),
			},
		}
	}

	dockerConfigContent, err := json.Marshal(dockerConfigObj)
	if err != nil {
		return nil, err
	}

	secretsCli := kubeCli.CoreV1().Secrets(namespace)

	dockerConfigSecret, err = secretsCli.Get(ctx, dockerConfigCMKubeName, metav1.GetOptions{})
	dockerConfigIsNotFound := apierrors.IsNotFound(err)
	// nolint: gocritic
	if err != nil && !dockerConfigIsNotFound {
		return nil, err
	}
	err = nil
	if dockerConfigIsNotFound {
		dockerConfigSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: dockerConfigCMKubeName},
			StringData: map[string]string{
				"config.json": string(dockerConfigContent),
			},
		}
		_, err_ := secretsCli.Create(ctx, dockerConfigSecret, metav1.CreateOptions{})
		if err_ != nil {
			dockerConfigSecret, err = secretsCli.Get(ctx, dockerConfigCMKubeName, metav1.GetOptions{})
			dockerConfigIsNotFound = apierrors.IsNotFound(err)
			if err != nil && !dockerConfigIsNotFound {
				return nil, err
			}
			if dockerConfigIsNotFound {
				return nil, err_
			}
			if err != nil {
				err = nil
			}
		}
	} else {
		dockerConfigSecret.Data["config.json"] = dockerConfigContent
		_, err = secretsCli.Update(ctx, dockerConfigSecret, metav1.UpdateOptions{})
		if err != nil {
			return nil, err
		}
	}

	return
}

type CreateImageBuilderPodOption struct {
	ImageName      string
	Bento          *schemasv1.BentoWithRepositorySchema
	DockerRegistry modelschemas.DockerRegistrySchema
}

func (s *imageBuilderService) CreateImageBuilderPod(ctx context.Context, opt CreateImageBuilderPodOption) (pod *corev1.Pod, err error) {
	kubeName := strings.ReplaceAll(strcase.ToKebab(fmt.Sprintf("yatai-bento-image-builder-%s-%s", opt.Bento.Repository.Name, opt.Bento.Version)), ".", "-")
	kubeLabels := map[string]string{
		consts.KubeLabelYataiBentoRepository: opt.Bento.Repository.Name,
		consts.KubeLabelYataiBento:           opt.Bento.Version,
	}
	logrus.Infof("Creating image builder pod %s", kubeName)
	restConfig := config.GetConfigOrDie()
	kubeCli, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		err = errors.Wrap(err, "create kubernetes clientset")
		return
	}

	kubeNamespace := consts.KubeNamespaceYataiModelImageBuilder

	err = k8sutils.MakesureNamespaceExists(ctx, kubeCli, kubeNamespace)
	if err != nil {
		return
	}

	dockerConfigSecret, err := MakeSureDockerConfigSecret(ctx, kubeCli, kubeNamespace, opt.DockerRegistry)
	if err != nil {
		return
	}
	dockerConfigSecretKubeName := dockerConfigSecret.Name

	volumes := []corev1.Volume{
		{
			Name: "yatai",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: "workspace",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: dockerConfigSecretKubeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: dockerConfigSecretKubeName,
				},
			},
		},
	}

	volumeMounts := []corev1.VolumeMount{
		{
			Name:      "yatai",
			MountPath: "/yatai",
		},
		{
			Name:      "workspace",
			MountPath: "/workspace",
		},
		{
			Name:      dockerConfigSecretKubeName,
			MountPath: "/kaniko/.docker/",
		},
	}

	imageName := opt.ImageName

	dockerImageBuilder := commonconfig.GetDockerImageBuilderConfig()
	if err != nil {
		err = errors.Wrap(err, "failed to get docker image builder config")
		return
	}

	privileged := dockerImageBuilder.Privileged

	yataiConfig, err := commonconfig.GetYataiConfig(ctx, kubeCli, consts.KubeNamespaceYataiDeploymentComponent, false)
	if err != nil {
		err = errors.Wrap(err, "failed to get yatai config")
		return
	}

	secretName := "yatai"

	yataiSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: secretName,
		},
		StringData: map[string]string{
			consts.EnvYataiApiToken: yataiConfig.ApiToken,
		},
	}

	_, err = kubeCli.CoreV1().Secrets(kubeNamespace).Get(ctx, secretName, metav1.GetOptions{})
	isNotFound := apierrors.IsNotFound(err)
	if err != nil && !isNotFound {
		err = errors.Wrapf(err, "failed to get secret %s", secretName)
		return
	}

	if isNotFound {
		_, err = kubeCli.CoreV1().Secrets(kubeNamespace).Create(ctx, yataiSecret, metav1.CreateOptions{})
		isExists := apierrors.IsAlreadyExists(err)
		if err != nil && !isExists {
			err = errors.Wrapf(err, "failed to create secret %s", secretName)
			return
		}
	} else {
		_, err = kubeCli.CoreV1().Secrets(kubeNamespace).Update(ctx, yataiSecret, metav1.UpdateOptions{})
		if err != nil {
			err = errors.Wrapf(err, "failed to update secret %s", secretName)
			return
		}
	}

	downloadCommand := fmt.Sprintf("mkdir -p /workspace/buildcontext && curl -H \"X-YATAI-API-TOKEN: $YATAI_API_TOKEN\" '%s/api/v1/bento_repositories/%s/bentos/%s/download' --output /tmp/downloaded.tar && cd /workspace/buildcontext && tar -xvf /tmp/downloaded.tar && rm /tmp/downloaded.tar", yataiConfig.Endpoint, opt.Bento.Repository.Name, opt.Bento.Version)
	if !privileged {
		downloadCommand += " && chown -R 1000:1000 /workspace"
	}

	initContainers := []corev1.Container{
		{
			Name:  "bento-downloader",
			Image: "quay.io/bentoml/curl:0.0.1",
			Command: []string{
				"sh",
				"-c",
				downloadCommand,
			},
			VolumeMounts: volumeMounts,
			EnvFrom: []corev1.EnvFromSource{
				{
					SecretRef: &corev1.SecretEnvSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: secretName,
						},
					},
				},
			},
		},
	}

	for idx, model := range opt.Bento.Manifest.Models {
		modelRepositoryName, _, modelVersion := xstrings.Partition(model, ":")
		modelRepositoryDirPath := fmt.Sprintf("/workspace/buildcontext/models/%s", modelRepositoryName)
		modelDirPath := filepath.Join(modelRepositoryDirPath, modelVersion)
		var downloadCommandOutput bytes.Buffer
		err = template.Must(template.New("script").Parse(`
mkdir -p {{.ModelDirPath}} &&
curl -H "X-YATAI-API-TOKEN: $YATAI_API_TOKEN" "{{.Endpoint}}/api/v1/model_repositories/{{.ModelRepositoryName}}/models/{{.ModelVersion}}/download" --output /tmp/downloaded.tar &&
cd {{.ModelDirPath}} &&
tar -xvf /tmp/downloaded.tar &&
echo -n '{{.ModelVersion}}' > {{.ModelRepositoryDirPath}}/latest &&
rm /tmp/downloaded.tar`)).Execute(&downloadCommandOutput, map[string]interface{}{
			"ModelDirPath":           modelDirPath,
			"ModelRepositoryDirPath": modelRepositoryDirPath,
			"ModelRepositoryName":    modelRepositoryName,
			"ModelVersion":           modelVersion,
			"Endpoint":               yataiConfig.Endpoint,
		})
		if err != nil {
			err = errors.Wrap(err, "failed to generate download command")
			return
		}
		downloadCommand := downloadCommandOutput.String()
		if !privileged {
			downloadCommand += " && chown -R 1000:1000 /workspace"
		}
		initContainers = append(initContainers, corev1.Container{
			Name:  fmt.Sprintf("model-downloader-%d", idx),
			Image: "quay.io/bentoml/curl:0.0.1",
			Command: []string{
				"sh",
				"-c",
				downloadCommand,
			},
			VolumeMounts: volumeMounts,
			EnvFrom: []corev1.EnvFromSource{
				{
					SecretRef: &corev1.SecretEnvSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: secretName,
						},
					},
				},
			},
		})
	}

	dockerFilePath := "/workspace/buildcontext/env/docker/Dockerfile"

	envs := []corev1.EnvVar{
		{
			Name:  "DOCKER_CONFIG",
			Value: "/kaniko/.docker/",
		},
	}

	if !privileged {
		envs = append(envs, corev1.EnvVar{
			Name:  "BUILDKITD_FLAGS",
			Value: "--oci-worker-no-process-sandbox",
		})
	}

	args := []string{
		"build",
		"--frontend",
		"dockerfile.v0",
		"--local",
		"context=/workspace/buildcontext",
		"--local",
		fmt.Sprintf("dockerfile=%s", filepath.Dir(dockerFilePath)),
		"--output",
		fmt.Sprintf("type=image,name=%s,push=true,registry.insecure=%v", imageName, !opt.DockerRegistry.Secure),
	}

	annotations := make(map[string]string, 1)
	if !privileged {
		annotations["container.apparmor.security.beta.kubernetes.io/builder"] = "unconfined"
	}

	image := "quay.io/bentoml/buildkit:master-rootless"
	if privileged {
		image = "quay.io/bentoml/buildkit:master"
	}

	securityContext_ := &corev1.SecurityContext{
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeUnconfined,
		},
		RunAsUser:  utils.Int64Ptr(1000),
		RunAsGroup: utils.Int64Ptr(1000),
	}
	if privileged {
		securityContext_ = &corev1.SecurityContext{
			Privileged: utils.BoolPtr(true),
		}
	}

	podsCli := kubeCli.CoreV1().Pods(kubeNamespace)

	pod = &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        kubeName,
			Labels:      kubeLabels,
			Annotations: annotations,
		},
		Spec: corev1.PodSpec{
			RestartPolicy:  corev1.RestartPolicyNever,
			Volumes:        volumes,
			InitContainers: initContainers,
			Containers: []corev1.Container{
				{
					Name:            "builder",
					Image:           image,
					ImagePullPolicy: corev1.PullAlways,
					Command:         []string{"buildctl-daemonless.sh"},
					Args:            args,
					VolumeMounts:    volumeMounts,
					Env:             envs,
					TTY:             true,
					Stdin:           true,
					SecurityContext: securityContext_,
				},
			},
		},
	}

	selectorPieces := make([]string, 0, len(kubeLabels))
	for k, v := range kubeLabels {
		selectorPieces = append(selectorPieces, fmt.Sprintf("%s = %s", k, v))
	}

	if len(selectorPieces) > 0 {
		var pods *corev1.PodList
		pods, err = podsCli.List(ctx, metav1.ListOptions{
			LabelSelector: strings.Join(selectorPieces, ", "),
		})
		if err != nil {
			return
		}
		for _, pod_ := range pods.Items {
			err = podsCli.Delete(ctx, pod_.Name, metav1.DeleteOptions{})
			if err != nil {
				return
			}
		}
	}

	oldPod, err := podsCli.Get(ctx, kubeName, metav1.GetOptions{})
	isNotFound = apierrors.IsNotFound(err)
	if !isNotFound && err != nil {
		return
	}
	if isNotFound {
		_, err = podsCli.Create(ctx, pod, metav1.CreateOptions{})
		isExists := apierrors.IsAlreadyExists(err)
		if err != nil && !isExists {
			err = errors.Wrapf(err, "failed to create pod %s", kubeName)
			return
		}
		err = nil
	} else {
		var patchResult *patch.PatchResult
		patchResult, err = patch.DefaultPatchMaker.Calculate(oldPod, pod)
		if err != nil {
			err = errors.Wrapf(err, "failed to calculate patch for pod %s", kubeName)
			return
		}

		if !patchResult.IsEmpty() || oldPod.Status.Phase == corev1.PodFailed {
			err = podsCli.Delete(ctx, kubeName, metav1.DeleteOptions{})
			if err != nil {
				err = errors.Wrapf(err, "failed to delete pod %s", kubeName)
				return
			}
			_, err = podsCli.Create(ctx, pod, metav1.CreateOptions{})
			isExists := apierrors.IsAlreadyExists(err)
			if err != nil && !isExists {
				err = errors.Wrapf(err, "failed to create pod %s", kubeName)
				return
			}
			err = nil
		}
	}

	return
}
