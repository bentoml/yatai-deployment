#!/bin/bash

set -e

DEVEL=${DEVEL:-false}
DEVEL_HELM_REPO=${DEVEL_HELM_REPO:-false}

is_minikube=false
if kubectl config view --minify | grep 'minikube.sigs.k8s.io' > /dev/null; then
  is_minikube=true
fi

# check if jq command exists
if ! command -v jq &> /dev/null; then
  arch=$(uname -m)
  # download jq from github by different arch
  if [[ $arch == "x86_64" && $OSTYPE == 'darwin'* ]]; then
    jq_archived_name="gojq_v0.12.9_darwin_amd64"
  elif [[ $arch == "arm64" && $OSTYPE == 'darwin'* ]]; then
    jq_archived_name="gojq_v0.12.9_darwin_arm64"
  elif [[ $arch == "x86_64" && $OSTYPE == 'linux'* ]]; then
    jq_archived_name="gojq_v0.12.9_linux_amd64"
  elif [[ $arch == "aarch64" && $OSTYPE == 'linux'* ]]; then
    jq_archived_name="gojq_v0.12.9_linux_arm64"
  else
    echo "jq command not found, please install it first"
    exit 1
  fi
  echo "📥 downloading jq from github"
  if [[ $OSTYPE == 'darwin'* ]]; then
    curl -sL -o /tmp/yatai-jq.zip "https://github.com/itchyny/gojq/releases/download/v0.12.9/${jq_archived_name}.zip"
    echo "✅ downloaded jq to /tmp/yatai-jq.zip"
    echo "📦 extracting yatai-jq.zip"
    unzip -q /tmp/yatai-jq.zip -d /tmp
  else
    curl -sL -o /tmp/yatai-jq.tar.gz "https://github.com/itchyny/gojq/releases/download/v0.12.9/${jq_archived_name}.tar.gz"
    echo "✅ downloaded jq to /tmp/yatai-jq.tar.gz"
    echo "📦 extracting yatai-jq.tar.gz"
    tar zxf /tmp/yatai-jq.tar.gz -C /tmp
  fi
  echo "✅ extracted jq to /tmp/${jq_archived_name}"
  jq="/tmp/${jq_archived_name}/gojq"
else
  jq=$(which jq)
fi

# check if kubectl command exists
if ! command -v kubectl >/dev/null 2>&1; then
  echo "😱 kubectl command is not found, please install it first!" >&2
  exit 1
fi

KUBE_VERSION=$(kubectl version --output=json | $jq '.serverVersion.minor')
if [ ${KUBE_VERSION:1:2} -lt 20 ]; then
  echo "😱 install requires at least Kubernetes 1.20" >&2
  exit 1
fi

# check if helm command exists
if ! command -v helm >/dev/null 2>&1; then
  echo "😱 helm command is not found, please install it first!" >&2
  exit 1
fi

INGRESS_CLASS=$(kubectl get ingressclass -o jsonpath='{.items[0].metadata.name}' 2> /dev/null || true)
# check if ingress class is empty
if [ -z "$INGRESS_CLASS" ]; then
  if [ "$is_minikube" != "true" ]; then
    echo "😱 ingress controller is not found, please install it first!" >&2
    exit 1
  else
    echo "🤖 installing ingress for minikube"
    minikube addons enable ingress
    echo "✅ ingress installed"
  fi
fi

INGRESS_CLASS=$(kubectl get ingressclass -o jsonpath='{.items[0].metadata.name}' 2> /dev/null || true)
# check if ingress class is empty
if [ -z "$INGRESS_CLASS" ]; then
  echo "😱 ingress controller is not found, please install it first!" >&2
  exit 1
fi

echo "🧪 verifying that the yatai-image-builder is running"
if ! kubectl -n yatai-image-builder wait --for=condition=ready --timeout=10s pod -l app.kubernetes.io/name=yatai-image-builder; then
  echo "😱 yatai-image-builder is not ready, please wait for it to be ready!" >&2
  exit 1
fi
echo "✅ yatai-image-builder is ready"

namespace=yatai-deployment
bento_deployment_namespace=yatai

# check if namespace exists
if ! kubectl get namespace ${namespace} >/dev/null 2>&1; then
  echo "🤖 creating namespace ${namespace}"
  kubectl create namespace ${namespace}
  echo "✅ namespace ${namespace} created"
fi

if ! kubectl get namespace ${bento_deployment_namespace} >/dev/null 2>&1; then
  echo "🤖 creating namespace ${bento_deployment_namespace}"
  kubectl create namespace ${bento_deployment_namespace}
  echo "✅ namespace ${bento_deployment_namespace} created"
fi

new_cert_manager=0

if [ $(kubectl get pod -A -l app=cert-manager 2> /dev/null | wc -l) = 0 ]; then
  new_cert_manager=1
  echo "🤖 installing cert-manager..."
  kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.9.1/cert-manager.yaml
else
  echo "😀 cert-manager is already installed"
fi

echo "⏳ waiting for cert-manager to be ready..."
kubectl wait --for=condition=ready --timeout=600s pod -l app.kubernetes.io/instance=cert-manager -A
echo "✅ cert-manager is ready"

if [ ${new_cert_manager} = 1 ]; then
  echo "😴 sleep 10s to make cert-manager really work 🤷"
  sleep 10
  echo "✨ wake up"
fi

cat <<EOF > /tmp/cert-manager-test-resources.yaml
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: test-selfsigned
  namespace: ${namespace}
spec:
  selfSigned: {}
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: selfsigned-cert
  namespace: ${namespace}
spec:
  dnsNames:
    - example.com
  secretName: selfsigned-cert-tls
  issuerRef:
    name: test-selfsigned
EOF

kubectl apply -f /tmp/cert-manager-test-resources.yaml
echo "🧪 verifying that the cert-manager is working properly"
sleep 5
if ! kubectl -n ${namespace} wait --for=condition=ready --timeout=30s certificate selfsigned-cert; then
  echo "😱 self-signed certificate is not issued, please check cert-manager installation!" >&2
  exit 1;
fi
kubectl delete -f /tmp/cert-manager-test-resources.yaml
echo "✅ cert-manager is working properly"

if [ $(kubectl get pod -A -l k8s-app=metrics-server 2> /dev/null | wc -l) = 0 ]; then
  echo "🤖 installing metrics-server..."
  if [ "${is_minikube}" = "true" ]; then
    minikube addons enable metrics-server
  else
    kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
  fi
else
  echo "😀 metrics-server is already installed"
fi

echo "⏳ waiting for metrics-server to be ready..."
kubectl wait --for=condition=ready --timeout=600s pod -l k8s-app=metrics-server -A
echo "✅ metrics-server is ready"

helm_repo_name=bentoml
helm_repo_url=https://bentoml.github.io/helm-charts

# check if DEVEL_HELM_REPO is true
if [ "${DEVEL_HELM_REPO}" = "true" ]; then
  helm_repo_name=bentoml-devel
  helm_repo_url=https://bentoml.github.io/helm-charts-devel
fi

UPGRADE_CRDS=${UPGRADE_CRDS:-false}

if [ "${UPGRADE_CRDS}" = "true" ]; then
  echo "🤖 installing yatai-deployment CRDs..."
  kubectl apply --server-side -f https://raw.githubusercontent.com/bentoml/yatai-deployment/main/helm/yatai-deployment/crds/bentodeployment.yaml
  echo "⏳ waiting for yatai-deployment CRDs to be established..."
  kubectl wait --for condition=established --timeout=120s crd/bentodeployments.serving.yatai.ai
  echo "✅ yatai-deployment CRDs are established"
fi

helm repo remove ${helm_repo_name} 2> /dev/null || true
helm repo add ${helm_repo_name} ${helm_repo_url}
helm repo update ${helm_repo_name}
echo "🤖 installing yatai-deployment..."
helm upgrade --install yatai-deployment ${helm_repo_name}/yatai-deployment -n ${namespace} \
  --set layers.network.ingressClass=$INGRESS_CLASS \
  --skip-crds=${UPGRADE_CRDS} \
  --devel=${DEVEL}

echo "⏳ waiting for job yatai-deployment-default-domain to be complete..."
kubectl -n ${namespace} wait --for=condition=complete --timeout=600s job/yatai-deployment-default-domain
echo "✅ job yatai-deployment-default-domain is complete"

kubectl -n ${namespace} rollout restart deploy/yatai-deployment

echo "⏳ waiting for yatai-deployment to be ready..."
kubectl -n ${namespace} wait --for=condition=available --timeout=600s deploy/yatai-deployment
echo "✅ yatai-deployment is ready"
