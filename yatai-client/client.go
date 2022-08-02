package yataiclient

import (
	"context"
	"fmt"

	"github.com/bentoml/yatai-schemas/modelschemas"
	"github.com/bentoml/yatai-schemas/schemasv1"

	"github.com/bentoml/yatai-common/consts"
	"github.com/bentoml/yatai-common/reqcli"
	"github.com/bentoml/yatai-common/utils"
)

type YataiClient struct {
	endpoint string
	apiToken string
}

func NewYataiClient(endpoint, apiToken string) *YataiClient {
	return &YataiClient{
		endpoint: endpoint,
		apiToken: apiToken,
	}
}

func (c *YataiClient) getJsonReqBuilder() *reqcli.JsonRequestBuilder {
	return reqcli.NewJsonRequestBuilder().Headers(map[string]string{
		consts.YataiApiTokenHeaderName: c.apiToken,
	})
}

func (c *YataiClient) ListBentos(ctx context.Context, req schemasv1.ListQuerySchema) (bentos *schemasv1.BentoWithRepositoryListSchema, err error) {
	url_ := utils.UrlJoin(c.endpoint, "/api/v1/bentos", map[string]string{
		"start": fmt.Sprintf("%d", req.Start),
		"count": fmt.Sprintf("%d", req.Count),
		"q":     string(req.Q),
	})
	bentos = &schemasv1.BentoWithRepositoryListSchema{}
	_, err = c.getJsonReqBuilder().Method("GET").Url(url_).Result(bentos).Do(ctx)
	return
}

func (c *YataiClient) ListImageBuildStatusUnsyncedBentos(ctx context.Context) (bentos []*schemasv1.BentoWithRepositorySchema, err error) {
	url_ := utils.UrlJoin(c.endpoint, "/api/v1/image_build_status_unsynced_bentos")
	bentos = []*schemasv1.BentoWithRepositorySchema{}
	_, err = c.getJsonReqBuilder().Method("GET").Url(url_).Result(&bentos).Do(ctx)
	return
}

func (c *YataiClient) UpdateBentoImageBuildStatusSyncingAt(ctx context.Context, bentoRepositoryName, bentoVersion string) (err error) {
	url_ := utils.UrlJoin(c.endpoint, fmt.Sprintf("/api/v1/bento_repositories/%s/bentos/%s/update_image_build_status_syncing_at", bentoRepositoryName, bentoVersion))
	_, err = c.getJsonReqBuilder().Method("PATCH").Url(url_).Do(ctx)
	return
}

func (c *YataiClient) UpdateBentoImageBuildStatus(ctx context.Context, bentoRepositoryName, bentoVersion string, status modelschemas.ImageBuildStatus) (err error) {
	url_ := utils.UrlJoin(c.endpoint, fmt.Sprintf("/api/v1/bento_repositories/%s/bentos/%s/update_image_build_status", bentoRepositoryName, bentoVersion))
	_, err = c.getJsonReqBuilder().Method("PATCH").Payload(map[string]string{
		"image_build_status": string(status),
	}).Url(url_).Do(ctx)
	return
}

func (c *YataiClient) GetBento(ctx context.Context, bentoRepositoryName, bentoVersion string) (bento *schemasv1.BentoFullSchema, err error) {
	url_ := utils.UrlJoin(c.endpoint, fmt.Sprintf("/api/v1/bento_repositories/%s/bentos/%s", bentoRepositoryName, bentoVersion))
	bento = &schemasv1.BentoFullSchema{}
	_, err = c.getJsonReqBuilder().Method("GET").Url(url_).Result(bento).Do(ctx)
	return
}

func (c *YataiClient) GetBentoRepository(ctx context.Context, bentoRepositoryName string) (bentoRepository *schemasv1.BentoRepositorySchema, err error) {
	url_ := utils.UrlJoin(c.endpoint, fmt.Sprintf("/api/v1/bento_repositories/%s", bentoRepositoryName))
	bentoRepository = &schemasv1.BentoRepositorySchema{}
	_, err = c.getJsonReqBuilder().Method("GET").Url(url_).Result(bentoRepository).Do(ctx)
	return
}

func (c *YataiClient) GetDeployment(ctx context.Context, clusterName, namespace, deploymentName string) (deployment *schemasv1.DeploymentSchema, err error) {
	url_ := utils.UrlJoin(c.endpoint, fmt.Sprintf("/api/v1/clusters/%s/namespaces/%s/deployments/%s", clusterName, namespace, deploymentName))
	deployment = &schemasv1.DeploymentSchema{}
	_, err = c.getJsonReqBuilder().Method("GET").Url(url_).Result(deployment).Do(ctx)
	return
}

func (c *YataiClient) SyncDeploymentStatus(ctx context.Context, clusterName, namespace, deploymentName string) (deployment *schemasv1.DeploymentSchema, err error) {
	url_ := utils.UrlJoin(c.endpoint, fmt.Sprintf("/api/v1/clusters/%s/namespaces/%s/deployments/%s/sync_status", clusterName, namespace, deploymentName))
	deployment = &schemasv1.DeploymentSchema{}
	_, err = c.getJsonReqBuilder().Method("POST").Url(url_).Result(deployment).Do(ctx)
	return
}

func (c *YataiClient) CreateDeployment(ctx context.Context, clusterName string, schema *schemasv1.CreateDeploymentSchema) (deployment *schemasv1.DeploymentSchema, err error) {
	url_ := utils.UrlJoin(c.endpoint, fmt.Sprintf("/api/v1/clusters/%s/deployments", clusterName))
	deployment = &schemasv1.DeploymentSchema{}
	_, err = c.getJsonReqBuilder().Method("POST").Url(url_).Payload(schema).Result(deployment).Do(ctx)
	return
}

func (c *YataiClient) UpdateDeployment(ctx context.Context, clusterName, namespace, deploymentName string, schema *schemasv1.UpdateDeploymentSchema) (deployment *schemasv1.DeploymentSchema, err error) {
	url_ := utils.UrlJoin(c.endpoint, fmt.Sprintf("/api/v1/clusters/%s/namespaces/%s/deployments/%s", clusterName, namespace, deploymentName))
	deployment = &schemasv1.DeploymentSchema{}
	_, err = c.getJsonReqBuilder().Method("PATCH").Url(url_).Payload(schema).Result(deployment).Do(ctx)
	return
}

func (c *YataiClient) GetDockerRegistryRef(ctx context.Context, clusterName string) (registryRef *modelschemas.DockerRegistryRefSchema, err error) {
	url_ := utils.UrlJoin(c.endpoint, fmt.Sprintf("/api/v1/clusters/%s/docker_registry_ref", clusterName))
	registryRef = &modelschemas.DockerRegistryRefSchema{}
	_, err = c.getJsonReqBuilder().Method("GET").Url(url_).Result(registryRef).Do(ctx)
	return
}

func (c *YataiClient) GetMajorCluster(ctx context.Context) (cluster *schemasv1.ClusterFullSchema, err error) {
	url_ := utils.UrlJoin(c.endpoint, "/api/v1/current_org/major_cluster")
	cluster = &schemasv1.ClusterFullSchema{}
	_, err = c.getJsonReqBuilder().Method("GET").Url(url_).Result(cluster).Do(ctx)
	return
}

func (c *YataiClient) GetVersion(ctx context.Context) (version *schemasv1.VersionSchema, err error) {
	url_ := utils.UrlJoin(c.endpoint, "/api/v1/version")
	version = &schemasv1.VersionSchema{}
	_, err = c.getJsonReqBuilder().Method("GET").Url(url_).Result(version).Do(ctx)
	return
}

func (c *YataiClient) GetOrganization(ctx context.Context) (organization *schemasv1.OrganizationFullSchema, err error) {
	url_ := utils.UrlJoin(c.endpoint, "/api/v1/current_org")
	organization = &schemasv1.OrganizationFullSchema{}
	_, err = c.getJsonReqBuilder().Method("GET").Url(url_).Result(organization).Do(ctx)
	return
}

func (c *YataiClient) GetCluster(ctx context.Context, clusterName string) (cluster *schemasv1.ClusterFullSchema, err error) {
	url_ := utils.UrlJoin(c.endpoint, fmt.Sprintf("/api/v1/clusters/%s", clusterName))
	cluster = &schemasv1.ClusterFullSchema{}
	_, err = c.getJsonReqBuilder().Method("GET").Url(url_).Result(cluster).Do(ctx)
	return
}
