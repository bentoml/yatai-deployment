#!/bin/bash

set -xe

kubectl create ns yatai-system || true

echo "🚀 Installing yatai-image-builder..."
YATAI_ENDPOINT='empty' bash <(curl -s "https://raw.githubusercontent.com/bentoml/yatai-image-builder/main/scripts/quick-install-yatai-image-builder.sh")
echo "yatai-image-builder helm release values:"
helm get values yatai-image-builder -n yatai-image-builder
echo "🚀 Installing yatai-deployment..."
CHECK_YATAI_IMAGE_BUILDER=false YATAI_ENDPOINT='empty' USE_LOCAL_HELM_CHART=true IGNORE_INGRESS=true INGRESS_TLS_MODE='none' SKIP_METRICS_SERVER=true DOMAIN_SUFFIX='test.com' UPGRADE_CRDS=false bash ./scripts/quick-install-yatai-deployment.sh
echo "yatai-deployment helm release values:"
helm get values yatai-deployment -n yatai-deployment
