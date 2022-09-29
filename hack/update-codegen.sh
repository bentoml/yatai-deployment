#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

# 注意:
# 1. kubebuilder2.3.2版本生成的api目录结构code-generator无法直接使用(将api由api/${VERSION}移动至api/${GROUP}/${VERSION}即可)

# corresponding to go mod init <module>
MODULE=github.com/bentoml/yatai-deployment
# api package
APIS_PKG=apis
# generated output package
OUTPUT_PKG=generated/serving
# group-version such as foo:v1alpha1
GROUP=serving
GROUP_VERSIONS="${GROUP}:v1alpha2 ${GROUP}:v1alpha3"

SCRIPT_ROOT=$(dirname "${BASH_SOURCE[0]}")/..
CODEGEN_PKG=${CODEGEN_PKG:-${GOPATH}/pkg/mod/k8s.io/code-generator@v0.25.0}

# mkdir -p ./${APIS_PKG}/${GROUP}/

rm -rf ./github.com

trap "rm -rf ./github.com" EXIT

echo "Generating clientset, informers, listers for ${GROUP_VERSIONS}..."
# generate the code with:
# --output-base    because this script should also be able to run inside the vendor dir of
#                  k8s.io/kubernetes. The output-base is needed for the generators to output into the vendor dir
#                  instead of the $GOPATH directly. For normal projects this can be dropped.
#bash "${CODEGEN_PKG}"/generate-groups.sh "client,informer,lister" \
bash "${CODEGEN_PKG}"/generate-groups.sh all \
  ${MODULE}/${OUTPUT_PKG} ${MODULE}/${APIS_PKG} \
  "${GROUP_VERSIONS}" \
  --go-header-file "${SCRIPT_ROOT}"/hack/boilerplate.go.txt \
  --output-base "${SCRIPT_ROOT}"
#  --output-base "${SCRIPT_ROOT}/../../.."

echo "Generating clientset, informers, listers for ${GROUP_VERSIONS} done."

echo "Cleanup..."

rm -rf ./generated && mv ./${MODULE}/generated .
