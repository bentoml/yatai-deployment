#!/bin/bash

set -e

YATAI_ENDPOINT='' bash <(curl -s "https://raw.githubusercontent.com/bentoml/yatai-image-builder/main/scripts/quick-install-yatai-image-builder.sh")
YATAI_ENDPOINT='' USE_LOCAL_HELM_CHART=true bash ./scripts/quick-install-yatai-deployment.sh

kubectl apply -n yatai -f ./tests/e2e/example.yaml
sleep 5
kubectl -n yatai-deployment logs deploy/yatai-deployment
sleep 5
kubectl -n yatai wait --for=condition=available --timeout=600s deploy/test
kubectl -n yatai wait --for=condition=available --timeout=600s deploy/test-runner-0
kubectl -n yatai port-forward svc/test 3000:3000 &
PID=$!

function trap_handler {
 kill $PID
 kubectl delete -n yatai -f ./tests/e2e/example.yaml
}

trap trap_handler EXIT

sleep 5

output=$(curl --fail -X 'POST' http://localhost:3000/classify -d '[[0,1,2,3]]')
echo "output: '${output}'"
if [[ $output != *'[2]'* ]]; then
  echo "Test failed"
  exit 1
fi
