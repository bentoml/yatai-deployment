#!/bin/bash

CURRETN_CONTEXT=$(kubectl config current-context)
echo -e "\033[01;31mWarning: this will permanently delete all yatai-deployment resources, existing model deployments, docker image data. Note that external docker image will not be deleted.\033[00m"
echo -e "\033[01;31mWarning: this also means that all resources under the \033[00m\033[01;32myatai\033[00m, \033[00m\033[01;32myatai-deployment\033[00m, \033[00m\033[01;32myatai-builders\033[00m \033[01;31mnamespaces will be permanently deleted.\033[00m"
echo -e "\033[01;31mCurrent kubernetes context: \033[00m\033[01;32m$CURRETN_CONTEXT\033[00m"
read -p "Are you sure to delete yatai-deployment in cluster '$CURRETN_CONTEXT'? [y/n] " -n 1 -r
echo # move to a new line
read -p "(Double check) Are you sure to delete yatai-deployment in cluster '$CURRETN_CONTEXT'? [y/n] " -n 1 -r
echo # move to a new line
if [[ ! $REPLY =~ ^[Yy]$ ]]
then
    [[ "$0" = "$BASH_SOURCE" ]] && exit 1 || return 1 # handle exits from shell or function but don't exit interactive shell
fi

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
