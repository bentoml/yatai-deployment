#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

# 注意:
# 1. kubebuilder2.3.2版本生成的api目录结构code-generator无法直接使用(将api由api/${VERSION}移动至api/${GROUP}/${VERSION}即可)

# corresponding to go mod init <module>
MODULE=github.com/bentoml/yatai-deployment-operator
# api package
APIS_PKG=apis
# generated output package
OUTPUT_PKG=generated/serving
# group-version such as foo:v1alpha1
GROUP=serving
VERSION=v1alpha3
GROUP_VERSION=${GROUP}:${VERSION}

SCRIPT_ROOT=$(dirname "${BASH_SOURCE[0]}")/..
CODEGEN_PKG=${CODEGEN_PKG:-${GOPATH}/pkg/mod/k8s.io/code-generator@v0.23.4}

# mkdir -p ./${APIS_PKG}/${GROUP}/

# ln -sf $PWD/${APIS_PKG}/${VERSION}/ ./${APIS_PKG}/${GROUP}/${VERSION}

rm -rf ${OUTPUT_PKG}/{clientset,informers,listers}

echo "Generating clientset for ${GROUP_VERSION}..."
# generate the code with:
# --output-base    because this script should also be able to run inside the vendor dir of
#                  k8s.io/kubernetes. The output-base is needed for the generators to output into the vendor dir
#                  instead of the $GOPATH directly. For normal projects this can be dropped.
#bash "${CODEGEN_PKG}"/generate-groups.sh "client,informer,lister" \
bash "${CODEGEN_PKG}"/generate-groups.sh all \
  ${MODULE}/${OUTPUT_PKG} ${MODULE}/${APIS_PKG} \
  ${GROUP_VERSION} \
  --go-header-file "${SCRIPT_ROOT}"/hack/boilerplate.go.txt \
  --output-base "${SCRIPT_ROOT}"
#  --output-base "${SCRIPT_ROOT}/../../.."

echo "Generating clientset for ${GROUP_VERSION} done."

echo "Generating clientset for ${GROUP} done."

echo "Cleanup..."

rm -rf ./generated && mv ./${MODULE}/generated .

# find ./generated/ -type f -not -path '*/\.*' -exec sed -i 's/github.com\/bentoml\/yatai-deployment-operator\/api\/serving\//github.com\/bentoml\/yatai-deployment-operator\/api\//g' {} \;

rm -rf ./github.com

# rm -rf ./${APIS_PKG}/${GROUP}
