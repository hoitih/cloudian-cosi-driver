#! /bin/bash

set -euxo pipefail
cd "$(dirname "$0")"

# Teardown old deployments if they exist, just ignore errors rather than being clever
k3d cluster delete cosi-driver || true

k3d cluster create --config ../utils/k3d/k3d-config.yaml

# Copy an up-to-date image to the cluster
pushd ..
    make image
    docker tag cloudian-cosi-driver:v0.0.0 quay.io/cloudian/cosi-driver:v0.0.0
    k3d image import -c cosi-driver quay.io/cloudian/cosi-driver:v0.0.0
popd

# Install the v1alpha1 APIs and controller matching the driver's proto release.
kubectl apply -k 'https://github.com/kubernetes-sigs/container-object-storage-interface//?ref=v0.2.2'
