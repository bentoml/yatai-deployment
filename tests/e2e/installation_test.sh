#!/bin/bash

set -xe

kubectl create ns yatai-system

echo "🚀 Installing yatai-image-builder..."
YATAI_ENDPOINT='empty' bash <(curl -s "https://raw.githubusercontent.com/bentoml/yatai-image-builder/main/scripts/quick-install-yatai-image-builder.sh")
echo "✅ yatai-image-builder is ready"
echo "🚀 Installing yatai-deployment..."
YATAI_ENDPOINT='empty' USE_LOCAL_HELM_CHART=true IGNORE_INGRESS=true SKIP_METRICS_SERVER=true DOMAIN_SUFFIX='test.com' UPGRADE_CRDS=false bash ./scripts/quick-install-yatai-deployment.sh
echo "✅ yatai-deployment is ready"

