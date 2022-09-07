#!/bin/bash

set -e

DEVEL=${DEVEL:-false}

# check if jq command exists
if ! command -v jq &> /dev/null; then
  # download jq from github by different arch
  case "$(uname -m)" in
    x86_64)
      JQ_ARCH=jq-linux64
      ;;
    aarch64)
      JQ_ARCH=jq-linux64
      ;;
    armv7l)
      JQ_ARCH=jq-linux32
      ;;
    Darwin)
      JQ_ARCH=jq-osx-amd64
      ;;
    *)
      echo "Unsupported architecture $(uname -m)"
      exit 1
      ;;
  esac
  echo "📥 downloading jq from github"
  curl -sL -o /tmp/yatai-jq "https://github.com/stedolan/jq/releases/download/jq-1.6/${JQ_ARCH}"
  echo "✅ downloaded jq to /tmp/yatai-jq"
  chmod +x /tmp/yatai-jq
  jq=/tmp/yatai-jq
else
  jq=$(which jq)
fi

# check if kubectl command exists
if ! command -v kubectl >/dev/null 2>&1; then
  echo "😱 kubectl command is not found, please install it first!" >&2
  exit 1
fi

KUBE_VERSION=$(kubectl version --output=json | jq '.serverVersion.minor')
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
  echo "😱 ingress controller is not found, please install it first!" >&2
  exit 1
fi

if ! kubectl -n yatai-system wait --for=condition=ready --timeout=10s pod -l app.kubernetes.io/name=yatai; then
  echo "😱 yatai is not ready, please wait for it to be ready!" >&2
  exit 1
fi

namespace=${namespace}
builders_namespace=yatai-builders
deployment_namespace=yatai

# check if namespace exists
if ! kubectl get namespace "$namespace" >/dev/null 2>&1; then
  echo "📥 creating namespace $namespace"
  kubectl create namespace "$namespace"
  echo "✅ namespace $namespace created"
fi

if ! kubectl get namespace "$builders_namespace" >/dev/null 2>&1; then
  echo "📥 creating namespace $builders_namespace"
  kubectl create namespace "$builders_namespace"
  echo "✅ namespace $builders_namespace created"
fi

if ! kubectl get namespace "$deployment_namespace" >/dev/null 2>&1; then
  echo "📥 creating namespace $deployment_namespace"
  kubectl create namespace "$deployment_namespace"
  echo "✅ namespace $deployment_namespace created"
fi

if [ $(kubectl get pod -A -l app=cert-manager 2> /dev/null | wc -l) = 0 ]; then
  echo "🤖 installing cert-manager..."
  kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.9.1/cert-manager.yaml
else
  echo "😀 cert-manager is already installed"
fi

echo "⏳ waiting for cert-manager to be ready..."
kubectl wait --for=condition=ready --timeout=600s pod -l app.kubernetes.io/instance=cert-manager -A
echo "✅ cert-manager is ready"
cat <<EOF > /tmp/cert-manager-test-resources.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: cert-manager-test
---
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: test-selfsigned
  namespace: cert-manager-test
spec:
  selfSigned: {}
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: selfsigned-cert
  namespace: cert-manager-test
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
if ! kubectl describe certificate -n cert-manager-test | grep "The certificate has been successfully issued"; then
  echo "😱 self-signed certificate is not issued, please check cert-manager installation!" >&2
  exit 1;
fi
kubectl delete -f /tmp/cert-manager-test-resources.yaml
echo "✅ cert-manager is working properly"

if [ $(kubectl get pod -A -l k8s-app=metrics-server 2> /dev/null | wc -l) = 0 ]; then
  echo "🤖 installing metrics-server..."
  kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
else
  echo "😀 metrics-server is already installed"
fi

echo "⏳ waiting for metrics-server to be ready..."
kubectl wait --for=condition=ready --timeout=600s pod -l k8s-app=metrics-server -A
echo "✅ metrics-server is ready"

helm repo add twuni https://helm.twun.io
helm repo update twuni
echo "🤖 installing docker-registry..."
helm upgrade --install docker-registry twuni/docker-registry -n ${namespace}

echo "⏳ waiting for docker-registry to be ready..."
kubectl -n ${namespace} wait --for=condition=ready --timeout=600s pod -l app=docker-registry
echo "✅ docker-registry is ready"

echo "🤖 installing docker-private-registry-proxy..."
cat <<EOF | kubectl apply -f -
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: docker-private-registry-proxy
  namespace: ${namespace}
  labels:
    app: docker-private-registry-proxy
spec:
  selector:
    matchLabels:
      app: docker-private-registry-proxy
  template:
    metadata:
      creationTimestamp: null
      labels:
        app: docker-private-registry-proxy
    spec:
      containers:
      - args:
        - tcp
        - "5000"
        - docker-registry.${namespace}.svc.cluster.local
        image: quay.io/bentoml/proxy-to-service:v2
        name: tcp-proxy
        ports:
        - containerPort: 5000
          hostPort: 5000
          name: tcp
          protocol: TCP
        resources:
          limits:
            cpu: 100m
            memory: 100Mi
EOF

echo "⏳ waiting for docker-private-registry-proxy to be ready..."
kubectl -n ${namespace} wait --for=condition=ready --timeout=600s pod -l app=docker-private-registry-proxy
echo "✅ docker-private-registry-proxy is ready"

DOCKER_REGISTRY_SERVER=127.0.0.1:5000
DOCKER_REGISTRY_IN_CLUSTER_SERVER=docker-registry.${namespace}.svc.cluster.local:5000
DOCKER_REGISTRY_USERNAME=''
DOCKER_REGISTRY_PASSWORD=''
DOCKER_REGISTRY_SECURE=false
DOCKER_REGISTRY_BENTO_REPOSITORY_NAME=yatai-bentos

helm repo add bentoml https://bentoml.github.io/charts
helm repo update bentoml
echo "🤖 installing yatai-deployment..."
helm upgrade --install yatai-deployment bentoml/yatai-deployment -n ${namespace} \
    --set dockerRegistry.server=$DOCKER_REGISTRY_SERVER \
    --set dockerRegistry.inClusterServer=$DOCKER_REGISTRY_IN_CLUSTER_SERVER \
    --set dockerRegistry.username=$DOCKER_REGISTRY_USERNAME \
    --set dockerRegistry.password=$DOCKER_REGISTRY_PASSWORD \
    --set dockerRegistry.secure=$DOCKER_REGISTRY_SECURE \
    --set dockerRegistry.bentoRepositoryName=$DOCKER_REGISTRY_BENTO_REPOSITORY_NAME \
    --set layers.network.ingressClass=$INGRESS_CLASS \
    --devel=$DEVEL

echo "⏳ waiting for job yatai-deployment-default-domain to be complete..."
kubectl -n ${namespace} wait --for=condition=complete --timeout=600s job/yatai-deployment-default-domain
echo "✅ job yatai-deployment-default-domain is complete"

echo "⏳ waiting for yatai-deployment to be ready..."
kubectl -n ${namespace} wait --for=condition=available --timeout=600s deploy/yatai-deployment
echo "✅ yatai-deployment is ready"
