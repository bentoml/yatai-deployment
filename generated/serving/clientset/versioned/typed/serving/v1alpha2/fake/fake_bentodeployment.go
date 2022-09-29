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
// Code generated by client-gen. DO NOT EDIT.

package fake

import (
	"context"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	labels "k8s.io/apimachinery/pkg/labels"
	schema "k8s.io/apimachinery/pkg/runtime/schema"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	testing "k8s.io/client-go/testing"

	v1alpha2 "github.com/bentoml/yatai-deployment/apis/serving/v1alpha2"
)

// FakeBentoDeployments implements BentoDeploymentInterface
type FakeBentoDeployments struct {
	Fake *FakeServingV1alpha2
	ns   string
}

var bentodeploymentsResource = schema.GroupVersionResource{Group: "serving.yatai.ai", Version: "v1alpha2", Resource: "bentodeployments"}

var bentodeploymentsKind = schema.GroupVersionKind{Group: "serving.yatai.ai", Version: "v1alpha2", Kind: "BentoDeployment"}

// Get takes name of the bentoDeployment, and returns the corresponding bentoDeployment object, and an error if there is any.
func (c *FakeBentoDeployments) Get(ctx context.Context, name string, options v1.GetOptions) (result *v1alpha2.BentoDeployment, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewGetAction(bentodeploymentsResource, c.ns, name), &v1alpha2.BentoDeployment{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha2.BentoDeployment), err
}

// List takes label and field selectors, and returns the list of BentoDeployments that match those selectors.
func (c *FakeBentoDeployments) List(ctx context.Context, opts v1.ListOptions) (result *v1alpha2.BentoDeploymentList, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewListAction(bentodeploymentsResource, bentodeploymentsKind, c.ns, opts), &v1alpha2.BentoDeploymentList{})

	if obj == nil {
		return nil, err
	}

	label, _, _ := testing.ExtractFromListOptions(opts)
	if label == nil {
		label = labels.Everything()
	}
	list := &v1alpha2.BentoDeploymentList{ListMeta: obj.(*v1alpha2.BentoDeploymentList).ListMeta}
	for _, item := range obj.(*v1alpha2.BentoDeploymentList).Items {
		if label.Matches(labels.Set(item.Labels)) {
			list.Items = append(list.Items, item)
		}
	}
	return list, err
}

// Watch returns a watch.Interface that watches the requested bentoDeployments.
func (c *FakeBentoDeployments) Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error) {
	return c.Fake.
		InvokesWatch(testing.NewWatchAction(bentodeploymentsResource, c.ns, opts))

}

// Create takes the representation of a bentoDeployment and creates it.  Returns the server's representation of the bentoDeployment, and an error, if there is any.
func (c *FakeBentoDeployments) Create(ctx context.Context, bentoDeployment *v1alpha2.BentoDeployment, opts v1.CreateOptions) (result *v1alpha2.BentoDeployment, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewCreateAction(bentodeploymentsResource, c.ns, bentoDeployment), &v1alpha2.BentoDeployment{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha2.BentoDeployment), err
}

// Update takes the representation of a bentoDeployment and updates it. Returns the server's representation of the bentoDeployment, and an error, if there is any.
func (c *FakeBentoDeployments) Update(ctx context.Context, bentoDeployment *v1alpha2.BentoDeployment, opts v1.UpdateOptions) (result *v1alpha2.BentoDeployment, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewUpdateAction(bentodeploymentsResource, c.ns, bentoDeployment), &v1alpha2.BentoDeployment{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha2.BentoDeployment), err
}

// UpdateStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
func (c *FakeBentoDeployments) UpdateStatus(ctx context.Context, bentoDeployment *v1alpha2.BentoDeployment, opts v1.UpdateOptions) (*v1alpha2.BentoDeployment, error) {
	obj, err := c.Fake.
		Invokes(testing.NewUpdateSubresourceAction(bentodeploymentsResource, "status", c.ns, bentoDeployment), &v1alpha2.BentoDeployment{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha2.BentoDeployment), err
}

// Delete takes name of the bentoDeployment and deletes it. Returns an error if one occurs.
func (c *FakeBentoDeployments) Delete(ctx context.Context, name string, opts v1.DeleteOptions) error {
	_, err := c.Fake.
		Invokes(testing.NewDeleteActionWithOptions(bentodeploymentsResource, c.ns, name, opts), &v1alpha2.BentoDeployment{})

	return err
}

// DeleteCollection deletes a collection of objects.
func (c *FakeBentoDeployments) DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error {
	action := testing.NewDeleteCollectionAction(bentodeploymentsResource, c.ns, listOpts)

	_, err := c.Fake.Invokes(action, &v1alpha2.BentoDeploymentList{})
	return err
}

// Patch applies the patch and returns the patched bentoDeployment.
func (c *FakeBentoDeployments) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *v1alpha2.BentoDeployment, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewPatchSubresourceAction(bentodeploymentsResource, c.ns, name, pt, data, subresources...), &v1alpha2.BentoDeployment{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha2.BentoDeployment), err
}