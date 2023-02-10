package v1alpha1

import (
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	"github.com/bentoml/yatai-deployment/apis/serving/v1alpha3"
)

func (src *BentoDeployment) ConvertTo(dstRaw conversion.Hub) error {
	dst := &v1alpha3.BentoDeployment{}
	dst.ObjectMeta = src.ObjectMeta
	dst.Spec.BentoTag = src.Spec.BentoTag
	dst.Spec.Resources = src.Spec.Resources
	dst.Spec.Autoscaling = src.Spec.Autoscaling
	dst.Spec.Envs = src.Spec.Envs
	return dst.ConvertTo(dstRaw)
}

func (dst *BentoDeployment) ConvertFrom(srcRaw conversion.Hub) error { //nolint:stylecheck
	src := &v1alpha3.BentoDeployment{}
	dst.ObjectMeta = src.ObjectMeta
	dst.Spec.BentoTag = src.Spec.BentoTag
	dst.Spec.Resources = src.Spec.Resources
	dst.Spec.Autoscaling = src.Spec.Autoscaling
	dst.Spec.Envs = src.Spec.Envs
	return src.ConvertFrom(srcRaw)
}
