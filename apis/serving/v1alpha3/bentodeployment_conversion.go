package v1alpha3

import (
	"context"
	"strings"

	// nolint: gosec
	"crypto/md5"
	"encoding/hex"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/conversion"

	"github.com/iancoleman/strcase"
	"github.com/pkg/errors"
	autoscalingv2beta2 "k8s.io/api/autoscaling/v2beta2"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/bentoml/yatai-common/consts"
	"github.com/bentoml/yatai-common/utils"
	resourcesv1alpha1 "github.com/bentoml/yatai-image-builder/apis/resources/v1alpha1"
	resourcesclient "github.com/bentoml/yatai-image-builder/generated/resources/clientset/versioned/typed/resources/v1alpha1"

	"github.com/bentoml/yatai-deployment/apis/serving/common"
	"github.com/bentoml/yatai-deployment/apis/serving/v2alpha1"
)

func TransformToOldHPA(hpa *v2alpha1.Autoscaling) (oldHpa *common.DeploymentTargetHPAConf, err error) {
	if hpa == nil {
		return
	}

	oldHpa = &common.DeploymentTargetHPAConf{
		MinReplicas: utils.Int32Ptr(hpa.MinReplicas),
		MaxReplicas: utils.Int32Ptr(hpa.MaxReplicas),
	}

	for _, metric := range hpa.Metrics {
		if metric.Type == autoscalingv2beta2.PodsMetricSourceType {
			if metric.Pods == nil {
				continue
			}
			if metric.Pods.Metric.Name == consts.KubeHPAQPSMetric {
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

func TransformToNewHPA(oldHpa *common.DeploymentTargetHPAConf) (hpa *v2alpha1.Autoscaling, err error) {
	if oldHpa == nil {
		return
	}

	maxReplicas := utils.Int32Ptr(consts.HPADefaultMaxReplicas)
	if oldHpa != nil && oldHpa.MaxReplicas != nil {
		maxReplicas = oldHpa.MaxReplicas
	}

	var metrics []autoscalingv2beta2.MetricSpec
	if oldHpa != nil && oldHpa.QPS != nil && *oldHpa.QPS > 0 {
		metrics = append(metrics, autoscalingv2beta2.MetricSpec{
			Type: autoscalingv2beta2.PodsMetricSourceType,
			Pods: &autoscalingv2beta2.PodsMetricSource{
				Metric: autoscalingv2beta2.MetricIdentifier{
					Name: consts.KubeHPAQPSMetric,
				},
				Target: autoscalingv2beta2.MetricTarget{
					Type:         autoscalingv2beta2.UtilizationMetricType,
					AverageValue: resource.NewQuantity(*oldHpa.QPS, resource.DecimalSI),
				},
			},
		})
	}

	if oldHpa != nil && oldHpa.CPU != nil && *oldHpa.CPU > 0 {
		metrics = append(metrics, autoscalingv2beta2.MetricSpec{
			Type: autoscalingv2beta2.ResourceMetricSourceType,
			Resource: &autoscalingv2beta2.ResourceMetricSource{
				Name: corev1.ResourceCPU,
				Target: autoscalingv2beta2.MetricTarget{
					Type:               autoscalingv2beta2.UtilizationMetricType,
					AverageUtilization: oldHpa.CPU,
				},
			},
		})
	}

	if oldHpa != nil && oldHpa.Memory != nil && *oldHpa.Memory != "" {
		var quantity resource.Quantity
		quantity, err = resource.ParseQuantity(*oldHpa.Memory)
		if err != nil {
			err = errors.Wrapf(err, "parse memory %s", *oldHpa.Memory)
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
	if oldHpa != nil && oldHpa.MinReplicas != nil {
		minReplicas = oldHpa.MinReplicas
	}
	hpa = &v2alpha1.Autoscaling{
		MinReplicas: *minReplicas,
		MaxReplicas: *maxReplicas,
		Metrics:     metrics,
	}

	return
}

func TransformToNewExtraPodMetadata(src *ExtraPodMetadata) (dst *v2alpha1.ExtraPodMetadata) {
	if src == nil {
		return nil
	}
	dst = &v2alpha1.ExtraPodMetadata{
		Annotations: src.Annotations,
		Labels:      src.Labels,
	}
	return
}

func TransformToOldExtraPodMetadata(src *v2alpha1.ExtraPodMetadata) (dst *ExtraPodMetadata) {
	if src == nil {
		return nil
	}
	dst = &ExtraPodMetadata{
		Annotations: src.Annotations,
		Labels:      src.Labels,
	}
	return
}

func TransformToNewExtraPodSpec(src *ExtraPodSpec) (dst *v2alpha1.ExtraPodSpec) {
	if src == nil {
		return
	}
	dst = &v2alpha1.ExtraPodSpec{
		SchedulerName:             src.SchedulerName,
		NodeSelector:              src.NodeSelector,
		Affinity:                  src.Affinity,
		Tolerations:               src.Tolerations,
		TopologySpreadConstraints: src.TopologySpreadConstraints,
	}
	return
}

func TransformToOldExtraPodSpec(src *v2alpha1.ExtraPodSpec) (dst *ExtraPodSpec) {
	if src == nil {
		return
	}
	dst = &ExtraPodSpec{
		SchedulerName:             src.SchedulerName,
		NodeSelector:              src.NodeSelector,
		Affinity:                  src.Affinity,
		Tolerations:               src.Tolerations,
		TopologySpreadConstraints: src.TopologySpreadConstraints,
	}
	return
}

func TransformToNewEnvs(oldEnvs *[]common.LabelItemSchema) (envs []corev1.EnvVar) {
	if oldEnvs == nil {
		return
	}
	envs = make([]corev1.EnvVar, 0)
	for _, item := range *oldEnvs {
		envs = append(envs, corev1.EnvVar{
			Name:  item.Key,
			Value: item.Value,
		})
	}
	return
}

func TransformToOldEnvs(envs []corev1.EnvVar) (oldEnvs *[]common.LabelItemSchema) {
	if envs == nil {
		return
	}

	oldEnvs_ := []common.LabelItemSchema{}
	for _, item := range envs {
		if item.Value == "" {
			continue
		}

		oldEnvs_ = append(oldEnvs_, common.LabelItemSchema{
			Key:   item.Name,
			Value: item.Value,
		})
	}

	oldEnvs = &oldEnvs_
	return
}

func hash(text string) string {
	// nolint: gosec
	hasher := md5.New()
	hasher.Write([]byte(text))
	return hex.EncodeToString(hasher.Sum(nil))
}

func GetBentoNameFromBentoTag(bentoTag string) string {
	bentoName := strings.ReplaceAll(strcase.ToKebab(bentoTag), ":", "--")
	if len(bentoName) > 63 {
		bentoName = fmt.Sprintf("bento-%s", hash(bentoTag))
		if len(bentoName) > 63 {
			bentoName = bentoName[:63]
		}
	}
	return bentoName
}

func getBentoTagFromBentoName(namesapce, bentoName string) (string, error) {
	restConf := clientconfig.GetConfigOrDie()
	bentocli, err := resourcesclient.NewForConfig(restConf)
	if err != nil {
		err = errors.Wrap(err, "create Bento client")
		return "", err
	}
	bento, err := bentocli.Bentoes(namesapce).Get(context.Background(), bentoName, metav1.GetOptions{})
	if err != nil {
		err = errors.Wrapf(err, "get Bento CR %s", bentoName)
		return "", err
	}
	if bento.Spec.Tag == "" {
		err = errors.New("Bento Tag is empty")
		return "", err
	}
	return bento.Spec.Tag, nil
}

func (src *BentoDeployment) ConvertToBentoRequest() *resourcesv1alpha1.BentoRequest {
	bentoName := GetBentoNameFromBentoTag(src.Spec.BentoTag)

	bentoRequest := &resourcesv1alpha1.BentoRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      bentoName,
			Namespace: src.Namespace,
		},
		Spec: resourcesv1alpha1.BentoRequestSpec{
			BentoTag: src.Spec.BentoTag,
		},
	}

	return bentoRequest
}

func (src *BentoDeployment) ConvertToV2alpha1(dstRaw conversion.Hub, bentoName string) error {
	dst := dstRaw.(*v2alpha1.BentoDeployment)
	dst.ObjectMeta = src.ObjectMeta
	dst.Spec.Bento = bentoName
	dst.Spec.Resources = src.Spec.Resources
	hpa, err := TransformToNewHPA(src.Spec.Autoscaling)
	if err != nil {
		return err
	}
	dst.Spec.Autoscaling = hpa
	dst.Spec.Envs = TransformToNewEnvs(src.Spec.Envs)
	dst.Spec.Runners = make([]v2alpha1.BentoDeploymentRunnerSpec, 0, len(src.Spec.Runners))
	for _, runner := range src.Spec.Runners {
		hpa, err := TransformToNewHPA(runner.Autoscaling)
		if err != nil {
			return err
		}
		newRunner := v2alpha1.BentoDeploymentRunnerSpec{
			Name:             runner.Name,
			Resources:        runner.Resources,
			Autoscaling:      hpa,
			Envs:             TransformToNewEnvs(runner.Envs),
			ExtraPodMetadata: TransformToNewExtraPodMetadata(runner.ExtraPodMetadata),
			ExtraPodSpec:     TransformToNewExtraPodSpec(runner.ExtraPodSpec),
		}
		dst.Spec.Runners = append(dst.Spec.Runners, newRunner)
	}
	dst.Spec.Ingress = v2alpha1.BentoDeploymentIngressSpec{
		Enabled:     src.Spec.Ingress.Enabled,
		Annotations: src.Spec.Ingress.Annotations,
		Labels:      src.Spec.Ingress.Labels,
	}
	if src.Spec.Ingress.TLS != nil {
		dst.Spec.Ingress.TLS = &v2alpha1.BentoDeploymentIngressTLSSpec{
			SecretName: src.Spec.Ingress.TLS.SecretName,
		}
	}
	dst.Spec.ExtraPodMetadata = TransformToNewExtraPodMetadata(src.Spec.ExtraPodMetadata)
	dst.Spec.ExtraPodSpec = TransformToNewExtraPodSpec(src.Spec.ExtraPodSpec)

	return nil
}

func (src *BentoDeployment) ConvertTo(dstRaw conversion.Hub) error {
	restConf := clientconfig.GetConfigOrDie()
	bentorequestcli, err := resourcesclient.NewForConfig(restConf)
	if err != nil {
		err = errors.Wrap(err, "create BentoRequest client")
		return err
	}

	bentoRequest := src.ConvertToBentoRequest()

	_, err = bentorequestcli.BentoRequests(src.Namespace).Create(context.Background(), bentoRequest, metav1.CreateOptions{})
	if k8serrors.IsAlreadyExists(err) {
		var oldBentoRequest *resourcesv1alpha1.BentoRequest
		oldBentoRequest, err = bentorequestcli.BentoRequests(src.Namespace).Get(context.Background(), bentoRequest.Name, metav1.GetOptions{})
		if err != nil {
			err = errors.Wrap(err, "get BentoRequest")
			return err
		}
		oldBentoRequest.Spec.BentoTag = src.Spec.BentoTag
		_, err = bentorequestcli.BentoRequests(src.Namespace).Update(context.Background(), oldBentoRequest, metav1.UpdateOptions{})
		if err != nil {
			err = errors.Wrap(err, "update BentoRequest")
			return err
		}
	}

	if err != nil {
		err = errors.Wrap(err, "create BentoRequest")
		return err
	}

	return src.ConvertToV2alpha1(dstRaw, bentoRequest.Name)
}

func (dst *BentoDeployment) ConvertFrom(srcRaw conversion.Hub) error { //nolint:stylecheck
	src := srcRaw.(*v2alpha1.BentoDeployment)
	dst.ObjectMeta = src.ObjectMeta
	bentoTag, err := getBentoTagFromBentoName(src.Namespace, src.Spec.Bento)
	if err != nil {
		return err
	}
	dst.Spec.BentoTag = bentoTag
	dst.Spec.Resources = src.Spec.Resources
	oldHpa, err := TransformToOldHPA(src.Spec.Autoscaling)
	if err != nil {
		return err
	}
	dst.Spec.Autoscaling = oldHpa
	dst.Spec.Envs = TransformToOldEnvs(src.Spec.Envs)
	dst.Spec.Runners = make([]BentoDeploymentRunnerSpec, 0, len(src.Spec.Runners))
	for _, runner := range src.Spec.Runners {
		oldHpa, err := TransformToOldHPA(runner.Autoscaling)
		if err != nil {
			return err
		}
		oldRunner := BentoDeploymentRunnerSpec{
			Name:        runner.Name,
			Resources:   runner.Resources,
			Autoscaling: oldHpa,
			Envs:        TransformToOldEnvs(runner.Envs),
		}
		if runner.ExtraPodMetadata != nil {
			oldRunner.ExtraPodMetadata = &ExtraPodMetadata{
				Annotations: runner.ExtraPodMetadata.Annotations,
				Labels:      runner.ExtraPodMetadata.Labels,
			}
		}
		if runner.ExtraPodSpec != nil {
			oldRunner.ExtraPodSpec = &ExtraPodSpec{
				SchedulerName:             runner.ExtraPodSpec.SchedulerName,
				NodeSelector:              runner.ExtraPodSpec.NodeSelector,
				Affinity:                  runner.ExtraPodSpec.Affinity,
				Tolerations:               runner.ExtraPodSpec.Tolerations,
				TopologySpreadConstraints: runner.ExtraPodSpec.TopologySpreadConstraints,
			}
		}
		dst.Spec.Runners = append(dst.Spec.Runners, oldRunner)
	}
	dst.Spec.Ingress = BentoDeploymentIngressSpec{
		Enabled: src.Spec.Ingress.Enabled,
	}
	dst.Spec.Ingress = BentoDeploymentIngressSpec{
		Enabled:     src.Spec.Ingress.Enabled,
		Annotations: src.Spec.Ingress.Annotations,
		Labels:      src.Spec.Ingress.Labels,
	}
	if src.Spec.Ingress.TLS != nil {
		dst.Spec.Ingress.TLS = &BentoDeploymentIngressTLSSpec{
			SecretName: src.Spec.Ingress.TLS.SecretName,
		}
	}
	if src.Spec.ExtraPodMetadata != nil {
		dst.Spec.ExtraPodMetadata = &ExtraPodMetadata{
			Annotations: src.Spec.ExtraPodMetadata.Annotations,
			Labels:      src.Spec.ExtraPodMetadata.Labels,
		}
	}
	if src.Spec.ExtraPodSpec != nil {
		dst.Spec.ExtraPodSpec = &ExtraPodSpec{
			SchedulerName:             src.Spec.ExtraPodSpec.SchedulerName,
			NodeSelector:              src.Spec.ExtraPodSpec.NodeSelector,
			Affinity:                  src.Spec.ExtraPodSpec.Affinity,
			Tolerations:               src.Spec.ExtraPodSpec.Tolerations,
			TopologySpreadConstraints: src.Spec.ExtraPodSpec.TopologySpreadConstraints,
		}
	}
	return nil
}
