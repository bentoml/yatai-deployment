#!/bin/bash

set -xe

# install ingress, needs to happen before yatai-deployment, otherwise quick-install-yatai-deployment.sh will complain
# INGRESS_CLASS should then be set automatically by the script if I understand correctly
kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.0.4/deploy/static/provider/kind/deploy.yaml
kubectl wait --namespace ingress-nginx --for=condition=ready pod --selector=app.kubernetes.io/component=controller --timeout=90s

# install yatai
kubectl create ns yatai-system || true

echo "🚀 Installing yatai-image-builder..."
YATAI_ENDPOINT='empty' bash <(curl -s "https://raw.githubusercontent.com/bentoml/yatai-image-builder/main/scripts/quick-install-yatai-image-builder.sh")
echo "yatai-image-builder helm release values:"
helm get values yatai-image-builder -n yatai-image-builder
echo "🚀 Installing yatai-deployment..."

# before we install yatai-deployment, we need to make sure that the network configmap is recreated by the quick-install script
# otherwise the ingress tls mode will not be set correctly in the next test action of the GHA job
kubectl delete configmap network -n yatai-deployment || true

INGRESS_TLS_MODE=${INGRESS_TLS_MODE} \
INGRESS_STATIC_TLS_SECRET_NAME=${INGRESS_STATIC_TLS_SECRET_NAME:-""} \
CHECK_YATAI_IMAGE_BUILDER=false \
YATAI_ENDPOINT='empty' \
USE_LOCAL_HELM_CHART=true \
IGNORE_INGRESS=false \
SKIP_METRICS_SERVER=true \
DOMAIN_SUFFIX='test.com' \
UPGRADE_CRDS=false \
bash ./scripts/quick-install-yatai-deployment.sh
echo "yatai-deployment helm release values:"
helm get values yatai-deployment -n yatai-deployment
