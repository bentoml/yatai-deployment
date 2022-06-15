package consts

const (
	DefaultNewsURL                         = "https://raw.githubusercontent.com/bentoml/yatai-homepage-news/main/news.json"
	DefaultETCDTimeoutSeconds              = 5
	DefaultETCDDialKeepaliveTimeSeconds    = 30
	DefaultETCDDialKeepaliveTimeoutSeconds = 10

	HPADefaultMaxReplicas = 10

	HPACPUDefaultAverageUtilization = 80

	YataiDebugImg             = "yatai.ai/yatai-infras/debug"
	YataiKubectlNamespace     = "default"
	YataiKubectlContainerName = "main"
	YataiKubectlImage         = "yatai.ai/yatai-infras/k8s"

	TracingContextKey = "tracing-context"
	// nolint: gosec
	YataiApiTokenHeaderName = "X-YATAI-API-TOKEN"

	BentoServicePort = 3000

	NoneStr = "None"

	AmazonS3Endpoint = "s3.amazonaws.com"
)
