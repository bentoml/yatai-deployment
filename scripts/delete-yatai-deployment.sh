#!/bin/bash

CURRENT_CONTEXT=$(kubectl config current-context)
echo -e "\033[01;31mWarning: this will permanently delete all yatai-deployment resources, existing model deployments, docker image data. Note that external docker image will not be deleted.\033[00m"
echo -e "\033[01;31mWarning: this also means that all resources under the \033[00m\033[01;32myatai\033[00m, \033[00m\033[01;32myatai-deployment\033[00m, \033[00m\033[01;32myatai-builders\033[00m \033[01;31mnamespaces will be permanently deleted.\033[00m"
echo -e "\033[01;31mCurrent kubernetes context: \033[00m\033[01;32m$CURRENT_CONTEXT\033[00m"


while true; do
  echo -e -n "Are you sure to delete yatai-deployment in cluster \033[00m\033[01;32m${CURRENT_CONTEXT}\033[00m? [y/n] "
  read yn
  case $yn in
    [Yy]* ) break;;
    [Nn]* ) exit;;
    * ) echo "Please answer yes or no.";;
  esac
done

echo "Uninstalling yatai-deployment helm chart from cluster.."
set -x
helm list -n yatai-deployment | tail -n +2 | awk '{print $1}' | xargs -I{} helm -n yatai-deployment uninstall {}
set +x

echo "Removing additional yatai-deployment related namespaces and resources.."
set -x
kubectl delete namespace yatai-builders
kubectl delete namespace yatai
kubectl delete namespace yatai-deployment
set +x

echo "Done"
