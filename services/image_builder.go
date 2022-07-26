package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	commonconfig "github.com/bentoml/yatai-common/config"
	"github.com/bentoml/yatai-common/consts"
	"github.com/bentoml/yatai-common/k8sutils"
	"github.com/bentoml/yatai-common/utils"
)

type imageBuilderService struct{}

var ImageBuilderService = &imageBuilderService{}

func MakeSureDockerConfigSecret(ctx context.Context, kubeCli *kubernetes.Clientset, namespace string) (dockerConfigSecret *corev1.Secret, err error) {
	dockerRegistry, err := commonconfig.GetDockerRegistryConfig(ctx, kubeCli)
	if err != nil {
		err = errors.Wrap(err, "failed to get docker registry config")
		return nil, err
	}

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
	KubeName          string
	ImageName         string
	DockerFileContent *string
	DockerFilePath    *string
	KubeLabels        map[string]string
	PresignedURL      string
}

func (s *imageBuilderService) CreateImageBuilderPod(ctx context.Context, opt CreateImageBuilderPodOption) (pod *corev1.Pod, err error) {
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

	dockerConfigSecret, err := MakeSureDockerConfigSecret(ctx, kubeCli, kubeNamespace)
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

	dockerRegistry, err := commonconfig.GetDockerRegistryConfig(ctx, kubeCli)
	if err != nil {
		err = errors.Wrap(err, "failed to get docker registry config")
		return
	}

	kubeName := opt.KubeName
	imageName := opt.ImageName

	dockerImageBuilder, err := commonconfig.GetDockerImageBuilderConfig(ctx, kubeCli)
	if err != nil {
		err = errors.Wrap(err, "failed to get docker image builder config")
		return
	}

	privileged := dockerImageBuilder.Privileged

	securityContext := &corev1.SecurityContext{
		RunAsUser:  utils.Int64Ptr(1000),
		RunAsGroup: utils.Int64Ptr(1000),
	}

	if privileged {
		securityContext = nil
	}

	downloadCommand := fmt.Sprintf("curl '%s' --output /tmp/downloaded.tar && mkdir -p /workspace/buildcontext && cd /workspace/buildcontext && tar -xvf /tmp/downloaded.tar && rm /tmp/downloaded.tar", opt.PresignedURL)
	if !privileged {
		downloadCommand += " && chown -R 1000:1000 /workspace"
	}

	initContainers := []corev1.Container{
		{
			Name:  "downloader",
			Image: "quay.io/bentoml/curlimages-curl:7.84.0",
			Command: []string{
				"sh",
				"-c",
				downloadCommand,
			},
			VolumeMounts: volumeMounts,
		},
	}

	dockerFilePath := ""
	if opt.DockerFilePath != nil {
		dockerFilePath = filepath.Join("/workspace/buildcontext", *opt.DockerFilePath)
	}
	if opt.DockerFileContent != nil {
		dockerFilePath = "/yatai/Dockerfile"
		initContainers = append(initContainers, corev1.Container{
			Name:  "init-dockerfile",
			Image: "quay.io/bentoml/busybox:1.33",
			Command: []string{
				"sh",
				"-c",
				fmt.Sprintf("echo \"%s\" > %s", *opt.DockerFileContent, dockerFilePath),
			},
			VolumeMounts:    volumeMounts,
			SecurityContext: securityContext,
		})
	}

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
		fmt.Sprintf("type=image,name=%s,push=true,registry.insecure=%v", imageName, !dockerRegistry.Secure),
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
			Labels:      opt.KubeLabels,
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

	selectorPieces := make([]string, 0, len(opt.KubeLabels))
	for k, v := range opt.KubeLabels {
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
	isNotFound := apierrors.IsNotFound(err)
	if !isNotFound && err != nil {
		return
	}
	if isNotFound {
		_, err = podsCli.Create(ctx, pod, metav1.CreateOptions{})
		if err != nil {
			return
		}
	} else {
		oldPod.Spec = pod.Spec
		_, err = podsCli.Update(ctx, oldPod, metav1.UpdateOptions{})
		if err != nil {
			return
		}
	}

	return
}
