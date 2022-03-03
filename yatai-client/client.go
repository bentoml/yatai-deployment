package yataiclient

import (
	"context"
	"fmt"

	"github.com/bentoml/yatai-deployment-operator/common/consts"
	"github.com/bentoml/yatai-deployment-operator/common/reqcli"
	"github.com/bentoml/yatai-deployment-operator/common/utils"
	"github.com/bentoml/yatai-schemas/schemasv1"
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

func (c *YataiClient) GetBento(ctx context.Context, bentoRepositoryName, bentoVersion string) (bento *schemasv1.BentoFullSchema, err error) {
	url_ := utils.UrlJoin(c.endpoint, fmt.Sprintf("/api/v1/bento_repositories/%s/bentos/%s", bentoRepositoryName, bentoVersion))
	_, err = c.getJsonReqBuilder().Method("GET").Url(url_).Result(bento).Do(ctx)
	return
}

func (c *YataiClient) GetBentoRepository(ctx context.Context, bentoRepositoryName string) (bentoRepository *schemasv1.BentoRepositorySchema, err error) {
	url_ := utils.UrlJoin(c.endpoint, fmt.Sprintf("/api/v1/bento_repositories/%s", bentoRepositoryName))
	_, err = c.getJsonReqBuilder().Method("GET").Url(url_).Result(bentoRepository).Do(ctx)
	return
}

func (c *YataiClient) CreateDeployment(ctx context.Context, clusterName string, schema *schemasv1.CreateDeploymentSchema) (deployment schemasv1.DeploymentSchema, err error) {
	url_ := utils.UrlJoin(c.endpoint, fmt.Sprintf("/api/v1/clusters/%s/deployments", clusterName))
	_, err = c.getJsonReqBuilder().Method("POST").Url(url_).Payload(schema).Result(deployment).Do(ctx)
	return
}
