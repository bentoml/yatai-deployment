# Developer Guide

I'm glad you can see this document and I'm looking forward to your contributions to the yatai-deployment.

yatai-deployment is a product based on a cloud-native architecture. It runs in a Kubernetes cluster, it is an operator for the BentoDeployment CRD, and it also acts as a client for Yatai to request Yatai's RESTful API.

As you know, Kubernetes has a complex network environment, so developing cloud-native products locally can be a challenge. But don't worry, this document will show you how to develop yatai-deployment locally easily, quickly and comfortably.

## Prequisites

- A yatai-deployment installed in the **development environment** for development and debugging

    > NOTE: Since you are developing, **you must not use the production environment**, so we recommend using the quick install script to install yatai and yatai-deployment in the local minikube

    A pre-installed yatai-deployment for development purposes is designed to provide an infrastructure that we can use directly

    You can start by reading this [installation document](https://docs.bentoml.org/projects/yatai/en/latest/installation/yatai_deployment.html) to install yatai-deployment. It is highly recommended to use the [quick install script](https://docs.bentoml.org/projects/yatai/en/latest/installation/yatai_deployment.html#quick-install) to install yatai-deployment

    Remember, **never use infrastructure from the production environment**, only use newly installed infrastructure in the cluster, such as SQL databases, blob storage, docker registry, etc. The [quick install script](https://docs.bentoml.org/projects/yatai/en/latest/installation/yatai_deployment.html#quick-install) mentioned above will prevent you from using the infrastructure in the production environment, this script will help you to install all the infrastructure from scratch, you can use it without any worries.

    If you have already installed it, please verify that your kubectl context is correct with the following command:

    ```bash
    kubectl config current-context
    ```

- [jq](https://stedolan.github.io/jq/)

    Used to parse json from the command line

- [Go language compiler](https://go.dev/)

    yatai-deployment is implemented by Go Programming Language

- [Telepresence](https://www.telepresence.io/)

    The most critical dependency in this document for bridging the local network and the Kubernetes cluster network

## Start Developing

<details>
1. Fork the yatai-deployment project on [GitHub](https://github.com/bentoml/yatai-deployment)

2. Clone the source code from your fork of yatai-deployment's GitHub repository:

    ```bash
    git clone git@github.com:${your github username}/yatai-deployment.git && cd yatai-deployment
    ```

3. Add the yatai-deployment upstream remote to your local yatai-deployment clone:

    ```bash
    git remote add upstream git@github.com:bentoml/yatai-deployment.git
    ```

4. Installing Go dependencies

    ```bash
    go mod download
    ```
</details>

## Making Changes

<details>
1. Make sure you're on the main branch.

   ```bash
   git checkout main
   ```

2. Use the git pull command to retrieve content from the BentoML Github repository.

   ```bash
   git pull upstream main -r
   ```

3. Create a new branch and switch to it.

   ```bash
   git checkout -b your-new-branch-name
   ```

4. Make your changes!

5. Use the git add command to save the state of files you have changed.

   ```bash
   git add <names of the files you have changed>
   ```

6. Commit your changes.

   ```bash
   git commit -m 'your commit message'
   ```

7. Synchronize upstream changes

    ```bash
    git pull upstream main -r
    ```

8. Push all changes to your forked repo on GitHub.

   ```bash
   git push origin your-new-branch-name
   ```
</details>

## Run yatai-deployment

1. Connect to the Kubernetes cluster network

    ```bash
    telepresence connect
    ```

2. Shut down yatai-deployment running in the Kubernetes cluster

    > NOTE: The following command will stop the BentoDeployment scheduling, so remember to restart yatai-deployment in the cluster after development is finished: `kubectl -n yatai-deployment patch deploy/yatai-deployment -p '{"spec":{"replicas":1}}'`

    ```bash
    kubectl -n yatai-deployment patch deploy/yatai-deployment -p '{"spec":{"replicas":0}}'
    ```

3. Run yatai-deployment

    > NOTE: The following command uses the infrastructure of the Kubernetes environment in the current kubectl context and replaces the behavior of yatai-deployment in the current Kubernetes environment, so please proceed with caution

    ```bash
    env $(kubectl -n yatai-deployment get secret env -o jsonpath='{.data}' | jq 'to_entries|map("\(.key)=\(.value|@base64d)")|.[]' | xargs) SYSTEM_NAMESPACE=yatai-deployment DISABLE_WEBHOOKS=true make run
    ```

4. ✨ Enjoy it!
