package v1alpha2

import (
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	"github.com/bentoml/yatai-deployment/apis/serving/v1alpha3"
)

func (src *BentoDeployment) ConvertToV1alpha3() *v1alpha3.BentoDeployment {
	dst := &v1alpha3.BentoDeployment{}
	dst.ObjectMeta = src.ObjectMeta
	dst.Spec.BentoTag = src.Spec.BentoTag
	dst.Spec.Resources = src.Spec.Resources
	dst.Spec.Autoscaling = src.Spec.Autoscaling
	dst.Spec.Envs = src.Spec.Envs
	dst.Spec.Runners = make([]v1alpha3.BentoDeploymentRunnerSpec, 0, len(src.Spec.Runners))
	for _, runner := range src.Spec.Runners {
		dst.Spec.Runners = append(dst.Spec.Runners, v1alpha3.BentoDeploymentRunnerSpec{
			Name:        runner.Name,
			Resources:   runner.Resources,
			Autoscaling: runner.Autoscaling,
			Envs:        runner.Envs,
		})
	}
	dst.Spec.Ingress = v1alpha3.BentoDeploymentIngressSpec{
		Enabled: src.Spec.Ingress.Enabled,
	}
	return dst
}

func (src *BentoDeployment) ConvertTo(dstRaw conversion.Hub) error {
	return src.ConvertToV1alpha3().ConvertTo(dstRaw)
}

func (dst *BentoDeployment) ConvertFrom(srcRaw conversion.Hub) error { //nolint:stylecheck
	src := v1alpha3.BentoDeployment{}
	dst.ObjectMeta = src.ObjectMeta
	dst.Spec.BentoTag = src.Spec.BentoTag
	dst.Spec.Resources = src.Spec.Resources
	dst.Spec.Autoscaling = src.Spec.Autoscaling
	dst.Spec.Envs = src.Spec.Envs
	dst.Spec.Runners = make([]BentoDeploymentRunnerSpec, 0, len(src.Spec.Runners))
	for _, runner := range src.Spec.Runners {
		dst.Spec.Runners = append(dst.Spec.Runners, BentoDeploymentRunnerSpec{
			Name:        runner.Name,
			Resources:   runner.Resources,
			Autoscaling: runner.Autoscaling,
			Envs:        runner.Envs,
		})
	}
	dst.Spec.Ingress = BentoDeploymentIngressSpec{
		Enabled: src.Spec.Ingress.Enabled,
	}
	return src.ConvertFrom(srcRaw)
}
