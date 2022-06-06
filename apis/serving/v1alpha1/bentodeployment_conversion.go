package v1alpha1

import (
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	"github.com/bentoml/yatai-deployment-operator/apis/serving/v1alpha3"
)

func (src *BentoDeployment) ConvertTo(dstRaw conversion.Hub) error {
	dst := dstRaw.(*v1alpha3.BentoDeployment)
	dst.ObjectMeta = src.ObjectMeta
	dst.Spec.BentoTag = src.Spec.BentoTag
	dst.Spec.Resources = src.Spec.Resources
	dst.Spec.Autoscaling = src.Spec.Autoscaling
	dst.Spec.Envs = src.Spec.Envs
	return nil
}

func (dst *BentoDeployment) ConvertFrom(srcRaw conversion.Hub) error {
	src := srcRaw.(*v1alpha3.BentoDeployment)
	dst.ObjectMeta = src.ObjectMeta
	dst.Spec.BentoTag = src.Spec.BentoTag
	dst.Spec.Resources = src.Spec.Resources
	dst.Spec.Autoscaling = src.Spec.Autoscaling
	dst.Spec.Envs = src.Spec.Envs
	return nil
}
