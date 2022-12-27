# yatai-deployment

yatai-deployment is a yatai component dedicated to deploying Bento to Kubernetes

## Description

yatai-deployment runs in k8s, it is the operator of `BentoDeployment` CRD, it is responsible for reconcile `BentoDeployment` CR and then create workloads and services for Bento. It relies on `Bento` CR to get the image and runners information, so it should install after the [yatai-image-builder](https://github.com/bentoml/yatai-image-builder) component installation.

## Installation

You should read the [installation guide](https://docs.bentoml.org/projects/yatai/en/latest/installation/yatai_deployment.html) to install yatai-deployment in a production environment.

## Contributing

Contributing code or documentation to the project by submitting a Github pull request. Check out the [Development Guide](https://github.com/bentoml/yatai-deployment/blob/main/DEVELOPMENT.md).

### How it works
This project aims to follow the Kubernetes [Operator pattern](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/)

It uses [Controllers](https://kubernetes.io/docs/concepts/architecture/controller/) 
which provides a reconcile function responsible for synchronizing resources untile the desired state is reached on the cluster 

### Test It Out
1. Install the CRDs into the cluster:

```sh
make install
```

2. Run your controller (this will run in the foreground, so switch to a new terminal if you want to leave it running):

```sh
make start-dev
```

**NOTE:** The more information you should check the [Development Guide](https://github.com/bentoml/yatai-deployment/blob/main/DEVELOPMENT.md).

### Modifying the API definitions
If you are editing the API definitions, generate the manifests such as CRs or CRDs using:

```sh
make manifests generate
```

**NOTE:** Run `make --help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

